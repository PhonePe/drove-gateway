package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	nplus "github.com/nginxinc/nginx-plus-go-client/client"
	"github.com/sirupsen/logrus"
)

type NginxAPIManager struct {
	client             *nplus.NginxClient
	apiAddr            string
	apiTimeout         time.Duration
	upstreamParameters struct {
		MaxFails    int
		FailTimeout string
		SlowStart   string
	}
}

func NewNginxAPIManager(nginxPlusApiAddr string, apiTimeout time.Duration, MaxFails int, FailTimeout string, SlowStart string) (*NginxAPIManager, error) {
	endpoint := "http://" + nginxPlusApiAddr + "/api"
	tr := &http.Transport{
		MaxIdleConns:       30,
		DisableCompression: true,
	}
	client := &http.Client{Transport: tr}
	nginxClient, err := nplus.NewNginxClient(endpoint, nplus.WithHTTPClient(client), nplus.WithAPIVersion(8))
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("unable to make call to nginx plus")
		GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, err.Error())
		return nil, err
	}
	logger.WithFields(logrus.Fields{"NginxAPIManager configuration and upstream parameters": fmt.Sprintf("%+v", nginxClient)}).Debug("NginxAPIManager initialized")
	return &NginxAPIManager{client: nginxClient, apiAddr: nginxPlusApiAddr, apiTimeout: apiTimeout, upstreamParameters: struct {
		MaxFails    int
		FailTimeout string
		SlowStart   string
	}{
		MaxFails:    MaxFails,
		FailTimeout: FailTimeout,
		SlowStart:   SlowStart,
	}}, nil
}

func (manager *NginxAPIManager) UnmarshalServerStructs(servers []nplus.UpstreamServer) string {
	jsonData := []string{}
	for _, server := range servers {
		jsonData = append(jsonData, manager.UnmarshalServerStruct(server))
	}
	return fmt.Sprintf("[" + strings.Join(jsonData, ",") + "]")
}

func (manager *NginxAPIManager) UnmarshalServerStruct(server nplus.UpstreamServer) string {
	jsonData, err := json.Marshal(server)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("unable to marshal upstream server struct")
		return ""
	}
	return fmt.Sprintf("%s", jsonData)
}

// ReconcileAllVhosts updates all HTTP vhosts using the NGINX Plus API.
func (manager *NginxAPIManager) ReconcileAllVhosts(data *RenderingData) error {
	start := time.Now()
	var resultLabel string
	defer func() {
		duration := time.Since(start)
		Metrics.NginxPlusReconcileAllBackendsDuration.WithLabelValues(resultLabel).Observe(duration.Seconds())
	}()

	logger.WithFields(logrus.Fields{
		"nginx": manager.apiAddr,
	}).Debug("NGINX Plus API endpoint")

	reconciledApps := make(map[string]bool)
	reconciliationFailedApps := make(map[string]bool)
	err := error(nil)

	for _, app := range data.Apps {
		if !isHTTPHostGroup(app.Hosts) || app.Vhost == "" || len(app.Hosts) == 0 {
			logger.WithFields(logrus.Fields{"vhost": app.Vhost}).Debug("Skipping non-HTTP/S vhost update")
			continue
		}

		var newFormattedServers []string
		for _, t := range app.Hosts {
			if t.PortType == "http" || t.PortType == "https" {
				// Update Host Addr with IP to ensure correct comparison as dynamic server state is always stored as an IP and FQDN is not tracked by nginx+
				// Local DNS resolver on Host should be reliable and maybe even serve stale records like in RFC8767 to ensure reliability, we are not going to handle DNS exceptions here and will try adding FQDN anyway
				ipRecord, err := resolveWithIPFallback(t.Host)
				if err != nil {
					logger.WithFields(logrus.Fields{
						"error":    err,
						"hostname": t.Host,
					}).Error("dns lookup failed, skipping the hostname")
					reconciliationFailedApps[app.Vhost] = true
					continue
				}
				hostAndPortMapping := formatNginxUpstreamEndpoint(ipRecord, t.Port)
				newFormattedServers = append(newFormattedServers, hostAndPortMapping)
			}
		}

		logger.WithFields(logrus.Fields{
			"vhost":     app.Vhost,
			"upstreams": newFormattedServers,
		}).Debug("nginx upstreams")

		upstreamtocheck := app.Vhost
		var finalformattedServers []nplus.UpstreamServer
		for _, server := range newFormattedServers {
			finalformattedServers = append(finalformattedServers, nplus.UpstreamServer{
				Server:      server,
				MaxFails:    &manager.upstreamParameters.MaxFails,
				FailTimeout: manager.upstreamParameters.FailTimeout,
				SlowStart:   manager.upstreamParameters.SlowStart,
			})
		}

		// If upstream has no servers, UpdateHTTPServers returns error as in-line GetHTTPServers returns error. server ID 0 needs to be explicitly initiated by a PATCH
		err = manager.client.CheckIfUpstreamExists(upstreamtocheck)
		if err != nil {
			Metrics.NginxAPICallsFailed.WithLabelValues("check_if_upstream_exists").Inc()
			// First add atleast one server to initialise upstream to support UpdateHTTPServers
			logger.WithFields(logrus.Fields{
				"Adding fresh upstream for": upstreamtocheck,
			}).Info("Adding first server for upstream")
			if len(finalformattedServers) == 0 {
				logger.WithFields(logrus.Fields{
					"vhost": upstreamtocheck,
				}).Warn("No servers to add for new upstream")
				continue
			}
			//Adding first server for server ID 0. ID 0 needs to be updated if state file is resurrected when a vhost gets resurrected. Create ID 0 otherwise.
			err = manager.client.UpdateHTTPServer(upstreamtocheck, finalformattedServers[0])
			logger.WithFields(logrus.Fields{"Adding upstream": manager.UnmarshalServerStruct(finalformattedServers[0]), "vhost": upstreamtocheck}).Debug("Adding first upstream server")
			// Now upstream should have servers, update earlier state to let UpdateHTTPServers take over
			//But wait from some time for nginx to actually update it's state. Consecutive calls would still return a 404 if you don't wait long enough
			// Wait for upstream to exist
			if err != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				err = manager.client.CheckIfUpstreamExists(upstreamtocheck)
			waitGroup:
				for err != nil {
					select {
					case <-ctx.Done():
						logger.WithFields(logrus.Fields{
							"Adding fresh upstream for": upstreamtocheck,
						}).Error("Context timeout waiting for CheckIfUpstreamExists")
						break waitGroup
					default:
						time.Sleep(5 * time.Millisecond)
						err = manager.client.CheckIfUpstreamExists(upstreamtocheck)
						if err == nil {
							break waitGroup
						}
					}
				}
				cancel()
				if err != nil {
					logger.Error("unable to add initial server to new upstream: ", err)
					reconciliationFailedApps[app.Vhost] = true
					GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, err.Error())
					continue
				}
				Metrics.NginxAPICallsFailed.WithLabelValues("add_http_server").Inc()
			} else {
				logger.WithFields(logrus.Fields{
					"Added fresh upstream for": upstreamtocheck,
					"upstream":                 finalformattedServers[0].Server,
				}).Warn("Successfully added first server for new upstream")
				Metrics.NginxAPICallsSuccessful.WithLabelValues("add_http_server").Inc()
			}
		}

		if err == nil {
			Metrics.NginxAPICallsSuccessful.WithLabelValues("check_if_upstream_exists").Inc()
			logger.WithFields(logrus.Fields{"updating UpdateHTTPServers for": upstreamtocheck}).Debug("upstream exists, updating servers")
			added, deleted, updated, updateErr := manager.client.UpdateHTTPServers(upstreamtocheck, finalformattedServers)
			if updateErr != nil {
				Metrics.NginxAPICallsFailed.WithLabelValues("update_http_servers").Inc()
			} else {
				Metrics.NginxAPICallsSuccessful.WithLabelValues("update_http_servers").Inc()
			}
			if added != nil {
				logger.WithFields(logrus.Fields{
					"vhost":           upstreamtocheck,
					"upstreams added": manager.UnmarshalServerStructs(added),
				}).Info("nginx upstreams added")
			}
			if deleted != nil {
				logger.WithFields(logrus.Fields{
					"vhost":             upstreamtocheck,
					"upstreams deleted": manager.UnmarshalServerStructs(deleted),
				}).Info("nginx upstreams deleted")
			}
			if updated != nil {
				logger.WithFields(logrus.Fields{
					"vhost":             upstreamtocheck,
					"upstreams updated": manager.UnmarshalServerStructs(updated),
				}).Info("nginx upstreams updated")
			}
			if updateErr != nil {
				logger.WithFields(logrus.Fields{
					"vhost": upstreamtocheck,
					"error": updateErr,
				}).Error("unable to update nginx upstreams")
				err = errors.Join(err, updateErr)
			}
		} else {
			reconciliationFailedApps[app.Vhost] = true
			logger.WithFields(logrus.Fields{
				"vhost": app.Vhost,
				"error": err,
			}).Error("unable to check if upstream exists in nginx plus")
		}
		reconciledApps[app.Vhost] = true
	}

	if len(reconciliationFailedApps) > 0 || len(reconciledApps) == 0 {
		resultLabel = "error"
		if len(reconciliationFailedApps) > 0 {
			logger.WithField("failed_apps", reconciliationFailedApps).Error("Failed to reconcile some nginx plus vhosts")
			GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, errors.New("failed to reconcile some nginx plus vhosts: "+fmt.Sprintf("%v", reconciliationFailedApps)).Error())
		} else if len(reconciledApps) == 0 {
			resultLabel = "error"
			GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, errors.New("failed to reconcile any nginx plus vhosts").Error())
		}
		return errors.Join(errors.New("failed to reconcile nginx plus vhosts"), err)
	} else if len(reconciliationFailedApps) == 0 {
		resultLabel = "success"
		logger.Info("Successfully reconciled all nginx plus vhosts")
		GlobalProxyManager.UpdateAPIUpdatesHealthStatus(true, "OK")
	}
	return nil
}

func formatNginxUpstreamEndpoint(host string, port int32) string {
	trimmedHost := strings.TrimSpace(host)
	if strings.HasPrefix(trimmedHost, "[") && strings.HasSuffix(trimmedHost, "]") {
		trimmedHost = strings.TrimPrefix(strings.TrimSuffix(trimmedHost, "]"), "[")
	}
	return net.JoinHostPort(trimmedHost, strconv.Itoa(int(port)))
}
