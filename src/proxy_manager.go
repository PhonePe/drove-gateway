package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type ProxyManager interface {
	GenerateStableBackendName(app App, groupName string) string
	GenerateStableServerName(host Host) string
	CheckConfig(testConfigPath string) error
	GetTempFilePattern() string
	Reconcile(data *RenderingData) error
	IsRuntimeAPIUpstreamUpdateEnabled() bool
	IsControlPlaneResponsive() bool
	UpdateAPIUpdatesHealthStatus(status bool, message string)
	Reload() error
}

type NginxProxyManager struct {
	config             *Config
	apiManagerDisabled bool
	apiManager         *NginxAPIManager
}

func (pmgr *NginxProxyManager) CheckConfig(testConfigPath string) error {
	if pmgr.config.NginxIgnoreCheck {
		logger.Warn("Skipping test of generated config as NginxIgnoreCheck is set to true")
		return nil
	}
	// This is to allow arguments as well. Example "docker exec nginx..."
	args := strings.Fields(ProgramCmd)
	head := args[0]
	args = args[1:]
	args = append(args, ProgramCmdConfFileArg, testConfigPath, ProgramCmdConfTestArg)
	return runCommand(head, args...)
}

func (pmgr *NginxProxyManager) GetTempFilePattern() string {
	return ".nginx.conf.tmp-"
}

func (pmgr *NginxProxyManager) Reconcile(data *RenderingData) error {
	return pmgr.apiManager.ReconcileAllVhosts(data)
}

func (pmgr *NginxProxyManager) UpdateAPIUpdatesHealthStatus(status bool, message string) {
	updateHealthSection("UpstreamUpdatesViaAPI", status, message)
}

func (pmgr *NginxProxyManager) IsRuntimeAPIUpstreamUpdateEnabled() bool {
	return !pmgr.apiManagerDisabled
}

func (pmgr *NginxProxyManager) IsControlPlaneResponsive() bool {
	// NGINX Plus runtime API mode: wait until the API socket is reachable.
	if pmgr.config.Nginxplusapiaddr != "" {
		conn, err := net.DialTimeout("tcp", pmgr.config.Nginxplusapiaddr, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	// NGINX OSS mode: ensure command binary is available before proceeding.
	return isCommandResponsive(pmgr.config.NginxCmd)
}

func (pmgr *NginxProxyManager) Reload() error {
	// This is to allow arguments as well. Example "docker exec nginx..."
	args := strings.Fields(pmgr.config.NginxCmd)
	head := args[0]
	args = args[1:]
	args = append(args, "-s", "reload")
	logger.WithFields(logrus.Fields{"cmd": head, "args": args}).Info("Reloading nginx")
	return runCommand(head, args...)
}

// GenerateStableBackendName returns a stable backend name for the given app and group.
func (pmgr *NginxProxyManager) GenerateStableBackendName(app App, groupName string) string {
	// For NGINX, backend name is just the vhost
	return app.Vhost
}

// GenerateStableServerName returns a stable server name for the given host (NGINX).
func (pmgr *NginxProxyManager) GenerateStableServerName(host Host) string {
	return net.JoinHostPort(host.Host, strconv.Itoa(int(host.Port)))
}

type HAProxyManager struct {
	config             *Config
	apiManagerDisabled bool
	apiManager         *HaproxyManager
}

func (pmgr *HAProxyManager) CheckConfig(testConfigPath string) error {
	if pmgr.config.HaproxyIgnoreCheck {
		logger.Warn("Skipping test of generated config as HaproxyIgnoreCheck is set to true")
		return nil
	}
	// This is to allow arguments as well. Example "docker exec nginx..."
	args := strings.Fields(ProgramCmd)
	head := args[0]
	args = args[1:]
	args = append(args, ProgramCmdConfFileArg, testConfigPath, ProgramCmdConfTestArg)
	return runCommand(head, args...)
}

func (pmgr *HAProxyManager) GetTempFilePattern() string {
	return ".haproxy.conf.tmp-"
}

func (pmgr *HAProxyManager) Reconcile(data *RenderingData) error {
	//if config reload is disabled, only use API to update backends. it is responsibility fo config to maintain state across restarts e.g. with global-server-state-file
	if !ConfigReloadDisabled {
		if err := updateProxyConfig(data); err != nil {
			return err
		}
	}

	return pmgr.apiManager.ReconcileAllBackends(data, config.HaproxyDisableLargeBackendCountOptimisation)
}

func (pmgr *HAProxyManager) UpdateAPIUpdatesHealthStatus(status bool, message string) {
	updateHealthSection("UpstreamUpdatesViaAPI", status, message)
}

func (pmgr *HAProxyManager) IsRuntimeAPIUpstreamUpdateEnabled() bool {
	return !pmgr.apiManagerDisabled
}

func (pmgr *HAProxyManager) IsControlPlaneResponsive() bool {
	if pmgr.config.HaproxySocketAddr == "" {
		return false
	}
	if IsUnixSocketAddr(pmgr.config.HaproxySocketAddr) {
		conn, err := net.DialTimeout("unix", pmgr.config.HaproxySocketAddr, 500*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	addr := strings.TrimPrefix(strings.TrimPrefix(pmgr.config.HaproxySocketAddr, "ipv4@"), "ipv6@")
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (pmgr *HAProxyManager) Reload() error {
	// This is to allow arguments as well. Example "docker exec nginx..." or SIGUSR2 to master worker
	args := strings.Fields(pmgr.config.HaproxyReloadCmd)
	head := args[0]
	args = args[1:]
	logger.WithFields(logrus.Fields{"cmd": head, "args": args}).Info("Reloading haproxy")
	return runCommand(head, args...)
}

// GenerateStableBackendName returns a stable backend name for the given app and group (HAProxy).
func (pmgr *HAProxyManager) GenerateStableBackendName(app App, groupName string) string {
	vhost := app.Vhost
	routingTagKey := app.RoutingTagKey
	routingTagValue := groupName
	// Only add suffix if the feature is enabled and a routing tag key is configured.
	if pmgr.config.HaproxyBackendIncludeRoutingTagSuffix && routingTagKey != "" {
		return fmt.Sprintf("%s%s%s", vhost, pmgr.config.HaproxyBackendNameSeparator, routingTagValue)
	}
	// Fallback to just the vhost if no routing tag is found or configured.
	return vhost
}

// GenerateStableServerName returns a stable server name for the given host (HAProxy).
func (pmgr *HAProxyManager) GenerateStableServerName(host Host) string {
	// Replace characters that are invalid in HAProxy server names.
	sanitizer := strings.NewReplacer(":", pmgr.config.HaproxyServerNameHostPortSeparator)
	return fmt.Sprintf("%s%s%s%s%d", pmgr.config.HaproxyServerNamePrefix, pmgr.config.HaproxyServerNameHostPortSeparator, sanitizer.Replace(host.Host), pmgr.config.HaproxyServerNameHostPortSeparator, host.Port)
}

func setupGlobalProxyManager() ProxyManager {
	var pm ProxyManager
	// Conditionally initialize runtime API manager at startup
	switch config.ProxyPlatform {
	case "nginx":
		logger.Debug("Platform:" + config.ProxyPlatform)

		if len(config.Nginxplusapiaddr) > 0 {
			logger.Debug("Platform: " + config.ProxyPlatform + " API addr: " + config.Nginxplusapiaddr)
			mgr, err := NewNginxAPIManager(
				config.Nginxplusapiaddr,
				time.Duration(config.apiTimeout)*time.Second,
				config.NginxMaxFailsUpstream,
				config.NginxFailTimeoutUpstream,
				config.NginxSlowStartUpstream,
			)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error": err.Error(),
				}).Fatal("unable to create nginx api manager at startup; exiting nixy")
			}
			pm = &NginxProxyManager{config: &config, apiManagerDisabled: false, apiManager: mgr}
			logger.Info("Nginx http api client initialized at startup")
		} else {
			logger.Info("No runtime API based upstream updates enabled at startup as nginx api address is not configured")
			pm = &NginxProxyManager{config: &config, apiManagerDisabled: true, apiManager: nil}
			logger.Info("Nginx http api client not initialized at startup as nginx api address is not configured")
		}
	case "haproxy":
		logger.Debug("Platform:" + config.ProxyPlatform)

		if len(config.HaproxySocketAddr) > 0 {
			logger.Debug("Platform: " + config.ProxyPlatform + " Socket addr: " + config.HaproxySocketAddr +
				" Runtime API add server default-server attributes:" + config.HaproxyAddServerAttributesString +
				" Runtime API add server ssl attributes:" + config.HaproxyAddServerSSLAttributesString)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.apiTimeout)*time.Second)
			defer cancel()
			mgr, err := NewHaproxyManager(ctx, config.HaproxySocketAddr, config.HaproxyDisableLargeBackendCountOptimisation, config.HaproxyAddServerAttributesString, config.HaproxyAddServerSSLAttributesString, config.HaproxyManageGlobalServerStateFile, config.HaproxyGlobalServerStateFilePath)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error": err.Error(),
				}).Fatal("unable to create haproxy manager at startup; exiting nixy")
			}
			pm = &HAProxyManager{config: &config, apiManagerDisabled: false, apiManager: mgr}
			logger.Info("Haproxy Runtime API client initialized at startup")
		} else {
			logger.Info("No runtime API based upstream updates enabled at startup as haproxy socket address is not configured")
			pm = &HAProxyManager{config: &config, apiManagerDisabled: true, apiManager: nil}
			logger.Info("Haproxy Runtime API client not initialized at startup as haproxy socket address is not configured")
		}
	default:
		logger.WithFields(logrus.Fields{
			"platform":            config.ProxyPlatform,
			"nginx_plus_api_addr": config.Nginxplusapiaddr,
			"haproxy_socket_addr": config.HaproxySocketAddr,
		}).Fatal("Invalid configuration. Exiting nixy")
		return nil
	}
	return pm
}

// runCommand executes a command and returns a formatted error if it fails.
func runCommand(head string, args ...string) error {
	cmd := exec.Command(head, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := fmt.Sprint(err) + ": " + stderr.String()
		return errors.New(msg)
	}
	return nil
}

func isCommandResponsive(commandLine string) bool {
	parts := strings.Fields(commandLine)
	if len(parts) == 0 {
		return false
	}
	_, err := exec.LookPath(parts[0])
	return err == nil
}
