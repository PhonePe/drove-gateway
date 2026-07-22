package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	runtime_misc "github.com/haproxytech/client-native/v5/misc"
	runtime_models "github.com/haproxytech/client-native/v5/models"
	runtime_api "github.com/haproxytech/client-native/v5/runtime"
	runtime_options "github.com/haproxytech/client-native/v5/runtime/options"
)

type HaproxyManager struct {
	client                           runtime_api.Runtime
	add_server_attributes_string     string
	add_server_ssl_attributes_string string
	socket_addr                      string
	manage_global_server_state_file  bool
	global_server_state_file_path    string
}

func NewHaproxyManager(ctx context.Context, haproxySocketAddr string, disableLargeBackendCountOptimisation bool, addServerAttributesString string, addServerSSLAttributesString string, manageGlobalServerStateFile bool, globalServerStateFilePath string) (*HaproxyManager, error) {
	logger.WithField("haproxy_socket", haproxySocketAddr).Debug("Preparing to connect to HAProxy runtime API")

	if haproxySocketAddr == "" {
		return nil, errors.New("HAProxy socket address is not configured")
	}

	if IsUnixSocketAddr(haproxySocketAddr) {
		if _, err := os.Stat(haproxySocketAddr); os.IsNotExist(err) {
			return nil, fmt.Errorf("HAProxy socket file does not exist: %s", haproxySocketAddr)
		}
	}

	runtimeClient, err := runtime_api.New(ctx, runtime_options.Sockets(map[int]string{1: haproxySocketAddr}))
	if err != nil {
		return nil, fmt.Errorf("error initializing HAProxy runtime client: %w", err)
	}

	if _, err := runtimeClient.GetInfo(); err != nil {
		return nil, fmt.Errorf("error connecting to HAProxy socket: %w", err)
	}

	logger.Info("Successfully connected to HAProxy runtime API")
	return &HaproxyManager{
		client:                           runtimeClient,
		add_server_attributes_string:     addServerAttributesString,
		add_server_ssl_attributes_string: addServerSSLAttributesString,
		socket_addr:                      haproxySocketAddr,
		manage_global_server_state_file:  manageGlobalServerStateFile,
		global_server_state_file_path:    globalServerStateFilePath,
	}, nil
}

func (manager *HaproxyManager) ReconcileAllBackends(data *RenderingData, disableLargeBackendCountOptimisation bool) error {
	start := time.Now()
	var resultLabel string
	defer func() {
		duration := time.Since(start)
		Metrics.HaproxyReconcileAllBackendsDuration.WithLabelValues(resultLabel).Observe(duration.Seconds())
	}()

	// Aggregate all desired hosts for each unique backend from our configuration. We don't want to make excess API calls
	backendsToReconcile := make(map[string][]Host)
	for _, app := range data.Apps {
		if app.Vhost == "" {
			continue
		}
		for groupName, groupData := range app.Groups {
			if len(groupData.Hosts) == 0 {
				continue
			}

			if !isHTTPHostGroup(groupData.Hosts) {
				logger.WithFields(logrus.Fields{
					"Vhost": app.Vhost,
					"group": groupName,
					"hosts": groupData.Hosts,
				}).Warning("Non-HTTP/HTTPS host group detected. Skipping HAProxy runtime API update for this group.")
				continue
			}

			backendName := GlobalProxyManager.GenerateStableBackendName(app, groupName)
			if backendName != "" {
				backendsToReconcile[backendName] = append(backendsToReconcile[backendName], groupData.Hosts...)
			}

		}
	}

	// Reconcile each unique backend. GetServersState (i.e. show servers state %backendName ) can only be called once per backend. Runtime API library doesn't return backend names
	// when calling GetServersState as models.RuntimeServers is a list of []*RuntimeServer without backend context returned by models.ParseRuntimeServer()
	// If number of backends is large, this could take a while and instead we will use equivalent of "show servers state <all>" which returns all backends and servers in one call
	// we will only call GetServersState if we see a diff in server names expected for a backend

	//For clusters with small number of backends, calling GetServersState for each backend is not a big overhead
	//but for a lot of backends, this can take a lot of time and we could have stale data for backends processed later in the loop
	//E.g. For a heavily loaded HAProxy with 1000 backends and >4000 RPS, calling GetServersState for each backend could take upwards of 200 seconds
	//So we will call GetServersState only if we don't find the backend in aggregated call to our own implementation of GetServersStateWithBackend
	//This way we optimize for both small and large number of backends

	//Upstream runtime doesn't expose the backend name when calling GetServersState API
	//so we have our own implementation which calls "show servers state" command and parses the output to get servers grouped by backend name

	//We could also use GetStats API but that would require more processing to convert stats to server state
	//and stats don't show servers in maintenance mode unless they are enabled. So a disabled server in maintenance mode
	//would not be visible in stats output but it is visible in "show servers state" output

	//Despite the fact that "show servers state" is reliable, making the use configurable in case of any issues of changes to models in future versions of HAProxy client runtime
	logger.WithField("count", len(backendsToReconcile)).Debug("Reconciling all unique HAProxy backends")
	reconciledBackends := make(map[string]bool)
	reconciliationFailedBackends := make(map[string]bool)
	allServersState := make(map[string]runtime_models.RuntimeServers)
	err := error(nil)
	if !disableLargeBackendCountOptimisation {
		logger.Debug("HAProxy large backend count optimisation is enabled. Using aggregated call to get all backends and servers state")
		allServersState, err = manager.getServersStateWithBackend()
		if err != nil {
			resultLabel = "error"
			logger.WithFields(logrus.Fields{
				"error": err,
			}).Error("Failed to get HAProxy servers state for all backends")
			Metrics.HaproxyAPICallsFailed.WithLabelValues("get_servers_state_all_backends").Inc()
			err = fmt.Errorf("failed to get HAProxy servers state for all backends: %w", err)
			GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, err.Error())
			return err
		}
		Metrics.HaproxyAPICallsSuccessful.WithLabelValues("get_servers_state_all_backends").Inc()
	}

	for backend, hosts := range backendsToReconcile {

		currentServersForBackend := []*runtime_models.RuntimeServer{}
		backendErr := error(nil)
		// If we have state for this backend from the aggregated call, use it directly.
		// This avoids making an additional API call per backend.
		if !disableLargeBackendCountOptimisation || allServersState[backend] != nil {
			currentServersForBackend = allServersState[backend]
		} else {
			if allServersState[backend] != nil {
				logger.Warn("Consider disabling large backend count optimisation with haproxy_disable_large_backend_count_optimisation set to true")
			}
			logger.WithField("backend", backend).Debug("No existing servers found for backend in aggregated state. Trying to get state for the backend directly")
			currentServersForBackend, backendErr = manager.client.GetServersState(backend)
			if backendErr != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				logger.WithField("backend", backend).Warn("Retrying to get servers state for backend after brief wait. Some backends might take time to exist after a reload")
			waitBackendGroup:
				select {
				case <-ctx.Done():
					logger.WithFields(logrus.Fields{"backend": backend}).Error("Context timeout waiting to retry get servers state for backend")
					break waitBackendGroup
				case <-time.After(500 * time.Millisecond):
					currentServersForBackend, backendErr = manager.client.GetServersState(backend)
					if backendErr == nil {
						break waitBackendGroup
					} else {
						logger.WithFields(logrus.Fields{"backend": backend, "error": backendErr}).Warn("Retry to get servers state for backend failed, will retry until timeout")
					}
				}
			}
		}

		if err != nil {
			reconciliationFailedBackends[backend] = true
			// This error is often not fatal; it can mean the backend doesn't exist yet.
			logger.WithFields(logrus.Fields{
				"backend": backend,
				"error":   backendErr,
			}).Warning("Could not get servers for backend, it may be new. Proceeding with reconciliation.")
			Metrics.HaproxyAPICallsFailed.WithLabelValues("get_servers_state").Inc()
			// Pass an empty slice so reconciliation can proceed to add servers.
			currentServersForBackend = []*runtime_models.RuntimeServer{}
		} else {
			Metrics.HaproxyAPICallsSuccessful.WithLabelValues("get_servers_state").Inc()
		}

		if backendErr = manager.reconcileBackend(backend, hosts, currentServersForBackend); backendErr != nil {
			reconciliationFailedBackends[backend] = true
			logger.WithFields(logrus.Fields{
				"backend": backend,
				"error":   backendErr,
			}).Error("Failed to reconcile HAProxy backend")
			// Continue with other backends instead of failing completely
		} else {
			reconciledBackends[backend] = true
		}
	}

	if len(reconciliationFailedBackends) > 0 || len(reconciledBackends) == 0 {
		resultLabel = "error"
		if len(reconciliationFailedBackends) > 0 {
			logger.WithField("failed_backends", reconciliationFailedBackends).Error("Failed to reconcile some HAProxy backends")
			err = errors.Join(err, errors.New("failed to reconcile some HAProxy backends: "+fmt.Sprintf("%v", reconciliationFailedBackends)))
			GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, err.Error())
		}
		if len(reconciledBackends) == 0 {
			logger.WithField("reconciled_backends", reconciledBackends).Error("Failed to reconcile any HAProxy backends")
			GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, errors.Join(errors.New("failed to reconcile any HAProxy backends"), err).Error())
		}
		return errors.Join(errors.New("Reconciliation failed for some or all HAProxy backends"), err)
	} else if len(reconciliationFailedBackends) == 0 {
		resultLabel = "success"
		logger.Info("Successfully reconciled all HAProxy backends")
		GlobalProxyManager.UpdateAPIUpdatesHealthStatus(true, "OK")
		// After a fully successful reconcile, persist the current server state to the configured
		// global server state file so HAProxy can restore dynamic server state on the next reload/restart.
		// Equivalent to: echo "show servers state" | socat stdio unix-connect:<socket> > <stateFile>
		if err := manager.writeGlobalServerStateFile(); err != nil {
			Metrics.HaproxyAPICallsFailed.WithLabelValues("write_global_server_state_file").Inc()
			logger.WithField("error", err).Warning("Failed to write HAProxy global server state file")
			updateHealthSection("ServerStateFileUpdate", false, err.Error())
		} else if manager.manage_global_server_state_file {
			Metrics.HaproxyAPICallsSuccessful.WithLabelValues("write_global_server_state_file").Inc()
			updateHealthSection("ServerStateFileUpdate", true, "OK")
		}
	}
	return nil
}

func (manager *HaproxyManager) reconcileBackend(backend string, desiredHosts []Host, currentServers []*runtime_models.RuntimeServer) error {
	var errs []string
	logger.WithField("backend", backend).Debug("Reconciling HAProxy backend")

	desiredServerMap := manager.buildDesiredServerMap(desiredHosts)
	currentServerMap := manager.buildCurrentServerMap(currentServers)

	if manager.areServerMapsIdentical(desiredServerMap, currentServerMap) {
		logger.WithField("backend", backend).Debug("HAProxy backend is already in the desired state. No changes needed.")
		return nil
	}

	if err := manager.addOrUpdateServers(backend, desiredServerMap, currentServerMap); err != nil {
		errs = append(errs, fmt.Sprintf("add/update servers: %v", err))
	}
	//add or update servers first to avoid downtime in case of complete replacement of servers
	if err := manager.removeStaleServers(backend, currentServers, desiredServerMap); err != nil {
		errs = append(errs, fmt.Sprintf("remove stale servers: %v", err))
	}

	if len(errs) > 0 {
		logger.WithField("backend", backend).Error("Errors occurred during reconciliation: " + fmt.Sprintf("%v", errs))
		return fmt.Errorf("failed to reconcile HAProxy backend: %v", errs)
	}

	logger.WithField("backend", backend).Info("Successfully reconciled HAProxy backend")
	return nil
}

func (manager *HaproxyManager) areServerMapsIdentical(desiredMap map[string]Host, currentMap map[string]runtime_models.RuntimeServer) bool {
	if len(desiredMap) != len(currentMap) {
		return false
	}
	for serverName, desiredHost := range desiredMap {
		currentServer, exists := currentMap[serverName]
		if !exists {
			return false
		}
		match, _, _ := manager.compareServerConfigs(desiredHost, currentServer)
		if !match {
			return false
		}
	}
	return true
}

func (manager *HaproxyManager) compareServerConfigs(desiredHost Host, current runtime_models.RuntimeServer) (bool, string, string) {
	// Resolve desired hostname to IP for a reliable comparison with HAProxy's runtime state.
	portMatches := current.Port != nil && *current.Port == int64(desiredHost.Port)
	addressMatches := current.Address == desiredHost.Host
	adminStateMatches := current.AdminState == "ready"
	// We only check for 'up' or 'maint' as operational state can fluctuate (e.g., 'going down').
	// A server in 'maint' is operationally down but administratively configured, so we consider it a match if we want it 'up'.
	opStateMatches := current.OperationalState == "up" || current.OperationalState == "maint"
	serverConfigsMatch := portMatches && addressMatches && adminStateMatches && opStateMatches
	return serverConfigsMatch, desiredHost.Host, current.Address
}

// Update Host Addr with IP to ensure correct comparison as dynamic server state is always stored as an IP and FQDN is not tracked by client-native library
// Local DNS resolver on Host should be reliable and maybe even serve stale records like in RFC8767 to ensure reliability, we are not going to handle DNS exceptions here and will try adding FQDN anyway
func (manager *HaproxyManager) updateHostWithIP(host Host) Host {
	ip, err := resolveWithIPFallback(host.Host)
	if err != nil {
		logger.WithFields(logrus.Fields{"host": host.Host}).Error("Error during dns resolution")
	}
	host.Host = ip
	return host
}

func (manager *HaproxyManager) buildDesiredServerMap(desiredHosts []Host) map[string]Host {
	serverMap := make(map[string]Host)
	for _, host := range desiredHosts {
		serverName := GlobalProxyManager.GenerateStableServerName(host)
		serverMap[serverName] = manager.updateHostWithIP(host)
	}
	return serverMap
}

func (manager *HaproxyManager) buildCurrentServerMap(currentServers []*runtime_models.RuntimeServer) map[string]runtime_models.RuntimeServer {
	serverMap := make(map[string]runtime_models.RuntimeServer)
	for _, srv := range currentServers {
		serverMap[srv.Name] = *srv
	}
	return serverMap
}

func (manager *HaproxyManager) removeStaleServers(backend string, currentServers []*runtime_models.RuntimeServer, desiredServerMap map[string]Host) error {
	var errs []string
	for _, srv := range currentServers {
		if _, exists := desiredServerMap[srv.Name]; !exists {
			logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name}).Info("Disabling and deleting stale server")
			if err := manager.client.SetServerState(backend, srv.Name, "drain"); err != nil {
				Metrics.HaproxyAPICallsFailed.WithLabelValues("set_server_state_drain").Inc()
				logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name, "error": err}).Error("Failed to set server to drain")
			}
			time.Sleep(200 * time.Millisecond) // wait briefly to ensure HAProxy processes the state change
			if err := manager.client.SetServerState(backend, srv.Name, "maint"); err != nil {
				Metrics.HaproxyAPICallsFailed.WithLabelValues("set_server_state_maint").Inc()
				logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name, "error": err}).Error("Failed to disable server")
				errs = append(errs, fmt.Sprintf("disable %s: %v", srv.Name, err))
			} else {
				Metrics.HaproxyAPICallsSuccessful.WithLabelValues("set_server_state_maint").Inc()
				time.Sleep(200 * time.Millisecond) // wait briefly to ensure HAProxy processes the state change
			}
			if srvr, err := manager.client.GetServerState(backend, srv.Name); err != nil {
				errs = append(errs, fmt.Sprintf("refresh after disable %s: %v", srv.Name, err))
			} else {
				logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name, "state": srvr}).Debug("Refreshed server state after disabling server")
				if srvr.OperationalState != "down" {
					logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name, "OperationalState": srvr.OperationalState}).Info("Server not yet down state after disabling. Waiting to ensure in-flight connections are terminated.")
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
				waitDownGroup:
					for err != nil {
						select {
						case <-ctx.Done():
							logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name}).Error("Context timeout waiting for server to exit 'stopping' state")
							break waitDownGroup
						default:
							time.Sleep(100 * time.Millisecond)
							srvr, err = manager.client.GetServerState(backend, srv.Name)
							if err == nil {
								if srvr.OperationalState == "down" {
									break waitDownGroup
								}
							}
						}
					}
				}
				if srvr.OperationalState != "down" {
					logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name, "operational_state": srvr.OperationalState}).Warning("Server operational state is not 'down' after disabling. Proceeding with deletion anyway.")
				}
			}
			if err := manager.client.DeleteServer(backend, srv.Name); err != nil {
				Metrics.HaproxyAPICallsFailed.WithLabelValues("delete_server").Inc()
				logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name, "error": err}).Error("Failed to delete server")
				time.Sleep(500 * time.Millisecond)
				//even if server is down, deletion might fail for some time
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
			waitDelGroup:
				for err != nil {
					logger.Warn("Retrying server deletion")
					select {
					case <-ctx.Done():
						logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name}).Error("Context timeout waiting to retry server deletion")
						break waitDelGroup
					default:
						time.Sleep(500 * time.Millisecond)
						err = manager.client.DeleteServer(backend, srv.Name)
						if err == nil {
							logger.WithFields(logrus.Fields{"backend": backend, "server": srv.Name}).Info("Successfully deleted server after retry")
							Metrics.HaproxyAPICallsSuccessful.WithLabelValues("delete_server").Inc()
							break waitDelGroup
						}
					}
				}
				if err != nil {
					errs = append(errs, fmt.Sprintf("delete %s: %v", srv.Name, err))
				}
			} else {
				Metrics.HaproxyAPICallsSuccessful.WithLabelValues("delete_server").Inc()
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors disabling stale servers: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (manager *HaproxyManager) addOrUpdateServers(backend string, desiredServerMap map[string]Host, currentServerMap map[string]runtime_models.RuntimeServer) error {
	var errs []string
	for serverName, host := range desiredServerMap {
		if _, exists := currentServerMap[serverName]; !exists {
			logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "host": host}).Debug("Required server not found, adding new server")
			if err := manager.addNewServer(backend, serverName, host, currentServerMap[serverName]); err != nil {
				errs = append(errs, fmt.Sprintf("add %s: %v", serverName, err))
			}
		} else {
			logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "host": host, "desired": desiredServerMap[serverName], "current": currentServerMap[serverName]}).Debug("Updating existing server")
			if err := manager.updateExistingServer(backend, serverName, host, currentServerMap[serverName]); err != nil {
				errs = append(errs, fmt.Sprintf("update %s: %v", serverName, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(fmt.Sprintf("errors in Add/Update servers: %s", strings.Join(errs, "; ")))
	}
	return nil
}

// settings from default-server statement in config are not applied when adding server via runtime api
// all default-server params should be added explictly here
// only use IP while adding server, not fqdn. HAProxy resolves the hostname only at startup/reload and dynamic servers don't support fqdn resolution
func (manager *HaproxyManager) addNewServer(backend, serverName string, host Host, currentServer runtime_models.RuntimeServer) error {
	logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "host": host}).Info("Adding fresh server")
	// Ensure you resolve desired hostname to IP for HAProxy server addition. Done while building desiredServerMap
	if host.PortType != "http" && host.PortType != "https" {
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName}).Warning("Non-HTTP/HTTPS port type detected. Skipping addition of this server via HAProxy runtime API.")
		return nil
	}

	attributes_string := manager.add_server_attributes_string
	logger.WithFields(logrus.Fields{"attributes": attributes_string, "backend": backend, "server": serverName}).Debug("Adding attributes to server")

	if host.PortType == "https" {
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName}).Debug("HTTPS port type detected. HAProxy runtime API does not support adding SSL parameters via runtime. Ensure SSL settings like ciphers are configured in the static config.")
		attributes_string = fmt.Sprintf("%s %s", attributes_string, manager.add_server_ssl_attributes_string)
		logger.WithFields(logrus.Fields{"attributes": attributes_string, "backend": backend, "server": serverName}).Info("Adding SSL attributes to server")

	}

	if err := manager.client.AddServer(backend, serverName, fmt.Sprintf("%s:%d %s", host.Host, host.Port, attributes_string)); err != nil {
		srvr, runErr := manager.client.GetServerState(backend, serverName)
		if runErr == nil {
			logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "srvr": srvr}).Info("Server already exists after failed add attempt. Proceeding to update existing server.")
			return manager.updateExistingServer(backend, serverName, host, currentServer)
		} else {
			//wait for backend to exist in case of recent reload
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			logger.WithFields(logrus.Fields{"backend": backend, "server": serverName}).Debug("Retrying to add server after brief wait. Some backends might take time to exist after a reload")
		waitAddGroup:
			select {
			case <-ctx.Done():
				logger.WithFields(logrus.Fields{"backend": backend, "server": serverName}).Error("Context timeout waiting to retry add server")
				break waitAddGroup
			case <-time.After(500 * time.Millisecond):
				err = manager.client.AddServer(backend, serverName, fmt.Sprintf("%s:%d %s", host.Host, host.Port, attributes_string))
				time.Sleep(10 * time.Millisecond)
				srvr, runErr = manager.client.GetServerState(backend, serverName)
				if err == nil {
					break waitAddGroup
				} else if runErr == nil {
					logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "srvr": srvr}).Info("Server added successfully after retry")
					err = nil
					break waitAddGroup
				} else {
					logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "error": err}).Warn("Retry to add server failed, will retry until timeout")
				}
			}
		}
		Metrics.HaproxyAPICallsFailed.WithLabelValues("add_server").Inc()
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "error": err}).Error("Failed to add server")
		return err
	}
	Metrics.HaproxyAPICallsSuccessful.WithLabelValues("add_server").Inc()

	if err := manager.client.SetServerState(backend, serverName, "ready"); err != nil {
		Metrics.HaproxyAPICallsFailed.WithLabelValues("set_server_state_ready").Inc()
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "error": err}).Error("Failed to set new server state to ready")
		return err
	}
	Metrics.HaproxyAPICallsSuccessful.WithLabelValues("set_server_state_ready").Inc()
	return nil
}

func (manager *HaproxyManager) updateExistingServer(backend, serverName string, host Host, currentServer runtime_models.RuntimeServer) error {
	match, desiredIP, _ := manager.compareServerConfigs(host, currentServer)
	if match {
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName}).Debug("Server is up-to-date, no update needed")
		return nil
	}

	logger.WithFields(logrus.Fields{
		"backend":           backend,
		"server":            serverName,
		"current_address":   currentServer.Address,
		"current_port":      currentServer.Port,
		"current_admin":     currentServer.AdminState,
		"current_operstate": currentServer.OperationalState,
		"desired_address":   desiredIP,
		"desired_port":      host.Port,
	}).Info("Updating existing server")

	var errs []string

	if err := manager.client.SetServerAddr(backend, serverName, desiredIP, int(host.Port)); err != nil {
		Metrics.HaproxyAPICallsFailed.WithLabelValues("set_server_addr").Inc()
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "error": err}).Error("Failed to set server address")
		errs = append(errs, fmt.Sprintf("set address: %v", err))
	} else {
		Metrics.HaproxyAPICallsSuccessful.WithLabelValues("set_server_addr").Inc()
	}

	if err := manager.client.SetServerHealth(backend, serverName, "up"); err != nil {
		Metrics.HaproxyAPICallsFailed.WithLabelValues("set_server_health_up").Inc()
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "error": err}).Error("Failed to set server health to up")
		errs = append(errs, fmt.Sprintf("set health: %v", err))
	} else {
		Metrics.HaproxyAPICallsSuccessful.WithLabelValues("set_server_health_up").Inc()
	}

	if err := manager.client.SetServerState(backend, serverName, "ready"); err != nil {
		Metrics.HaproxyAPICallsFailed.WithLabelValues("set_server_state_ready").Inc()
		logger.WithFields(logrus.Fields{"backend": backend, "server": serverName, "error": err}).Error("Failed to set server state to ready")
		errs = append(errs, fmt.Sprintf("set state: %v", err))
	} else {
		Metrics.HaproxyAPICallsSuccessful.WithLabelValues("set_server_state_ready").Inc()
	}

	if len(errs) > 0 {
		return fmt.Errorf("haproxyUpdateExistingServer errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Start of custom functions not available in HAProxy runtime client library
// getServersStateWithBackend calls "show servers state" command and parses the output to get servers grouped by backend name

func (manager *HaproxyManager) getServersStateWithBackend() (map[string]runtime_models.RuntimeServers, error) {
	cmd := "show servers state"
	result, err := manager.executeWithResponse(cmd)
	if err != nil {
		return nil, err
	}
	return manager.parseRuntimeServersWithBackend(result)
}

// writeGlobalServerStateFile fetches the current server state from HAProxy over the runtime API
// socket and writes it to the configured global server state file. This is the equivalent of:
//
//	echo "show servers state" | socat stdio unix-connect:<socket> > <stateFile>
//
// where <socket> is manager.socket_addr and <stateFile> is manager.global_server_state_file_path.
// HAProxy loads this file on the next reload/restart (via the global "server-state-file" directive
// and "load-server-state-from-file") to preserve dynamic server state applied through the runtime API.
func (manager *HaproxyManager) writeGlobalServerStateFile() error {
	if !manager.manage_global_server_state_file {
		return nil
	}
	if manager.global_server_state_file_path == "" {
		return errors.New("global server state file management is enabled but no file path is configured")
	}

	output, err := manager.executeWithResponse("show servers state")
	if err != nil {
		return fmt.Errorf("failed to get servers state for global server state file: %w", err)
	}

	// HAProxy expects a trailing newline when parsing the server state file.
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}

	// Write atomically via a temp file + rename so HAProxy never reads a partially written file.
	dir := filepath.Dir(manager.global_server_state_file_path)
	tmpFile, err := os.CreateTemp(dir, ".server_state.tmp-")
	if err != nil {
		return fmt.Errorf("failed to create temp file for global server state in %q: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(output); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write global server state to temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp global server state file: %w", err)
	}
	if err := os.Rename(tmpPath, manager.global_server_state_file_path); err != nil {
		return fmt.Errorf("failed to move global server state file into place at %q: %w", manager.global_server_state_file_path, err)
	}

	logger.WithFields(logrus.Fields{
		"path":   manager.global_server_state_file_path,
		"socket": manager.socket_addr,
	}).Debug("Wrote HAProxy global server state file")
	return nil
}

func (manager *HaproxyManager) executeWithResponse(command string) (string, error) {
	rawdata, err := manager.client.ExecuteRaw(command)
	if err != nil {
		return "", fmt.Errorf("%w [%s]", err, command)
	}
	output := strings.Join(rawdata, "\n")
	if len(output) > 4 {
		switch output[0:4] {
		case "[3]:", "[2]:", "[1]:", "[0]:":
			return "", fmt.Errorf("[%c] %s [%s]", output[1], output[4:], command)
		}
	}
	return output, nil
}

func (manager *HaproxyManager) parseRuntimeServersWithBackend(output string) (map[string]runtime_models.RuntimeServers, error) {
	lines := strings.Split(output, "\n")
	result := make(map[string]runtime_models.RuntimeServers)

	if strings.TrimSpace(lines[0]) != "1" {
		return nil, fmt.Errorf("unsupported output format version, supporting format version 1")
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "1" {
			continue
		}
		backend, server := manager.parseRuntimeServerWithBackend(line)
		if server != nil && backend != "" {
			if _, ok := result[backend]; !ok {
				result[backend] = []*runtime_models.RuntimeServer{}
			}
			result[backend] = append(result[backend], server)
		}
	}
	return result, nil
}

// Example input string to parseRuntimeServerWithBackend:
//# be_id be_name srv_id srv_name srv_addr srv_op_state srv_admin_state srv_uweight srv_iweight srv_time_since_last_change srv_check_status srv_check_result srv_check_health srv_check_state srv_agent_state bk_f_forced_id srv_f_forced_id srv_fqdn srv_port srvrecord srv_use_ssl srv_check_port srv_check_addr srv_agent_addr srv_agent_port
//5 app-client.drove.svc.cluster.local_app-client.drove.svc.cluster.local 1 server_prod-clusterdroveexecutor048.cluster.local_29291 10.57.50.208 2 0 1 1 31 1 0 2 0 0 0 0 prod-clusterdroveexecutor048.cluster.local 29291 - 0 0 - - 0
//6 app-control-panel.drove.svc.cluster.local_app-control-panel.drove.svc.cluster.local 1 server_prod-clusterdroveexecutor015.cluster.local_26757 10.57.49.166 2 0 1 1 31 1 0 2 0 0 0 0 prod-clusterdroveexecutor015.cluster.local 26757 - 0 0 - - 0

func (manager *HaproxyManager) parseRuntimeServerWithBackend(line string) (backend string, server *runtime_models.RuntimeServer) {
	fields := strings.Split(line, " ")

	if len(fields) < 19 {
		return "", nil
	}

	p, err := strconv.ParseInt(fields[18], 10, 64)
	var port *int64
	if err == nil {
		port = &p
	}

	admState, _ := runtime_misc.GetServerAdminState(fields[6])

	var opState string
	switch fields[5] {
	case "0":
		opState = "down"
	case "3":
		opState = "stopping"
	case "1", "2":
		opState = "up"
	}

	return fields[1], &runtime_models.RuntimeServer{
		Name:             fields[3],
		Address:          fields[4],
		Port:             port,
		ID:               fields[2],
		AdminState:       admState,
		OperationalState: opState,
	}
}
