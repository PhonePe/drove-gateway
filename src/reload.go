package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/sirupsen/logrus"
)

type NamespaceRenderingData struct {
	LeaderVHost string
	Leader      LeaderController
	RoutingTag  string `json:"-"`
}

type RenderingData struct {
	Xproxy                                string
	ProxyPlatform                         string                            `json:"-"`
	LeftDelimiter                         string                            `json:"-" toml:"left_delimiter"`
	RightDelimiter                        string                            `json:"-" toml:"right_delimiter"`
	NginxMaxFailsUpstream                 int                               `json:"-" toml:"max_fails"`
	NginxFailTimeoutUpstream              string                            `json:"-" toml:"nginx_fail_timeout"`
	NginxSlowStartUpstream                string                            `json:"-" toml:"nginx_slow_start"`
	HaproxySocketAddr                     string                            `json:"-" toml:"haproxy_socket_addr"`
	HaproxyAddServerAttributesString      string                            `json:"-" toml:"haproxy_add_server_attributes_string"`
	HaproxyAddServerSSLAttributesString   string                            `json:"-" toml:"haproxy_add_server_ssl_attributes_string"`
	HaproxyServerNamePrefix               string                            `json:"-" toml:"haproxy_server_name_prefix"`
	HaproxyServerNameHostPortSeparator    string                            `json:"-" toml:"haproxy_server_name_host_port_delimiter"`
	HaproxyBackendNameSeparator           string                            `json:"-" toml:"haproxy_backend_name_separator"`
	HaproxyBackendIncludeRoutingTagSuffix bool                              `json:"-" toml:"haproxy_backend_include_routing_tag_suffix"`
	Namespaces                            map[string]NamespaceRenderingData `json:"namespaces"`
	Apps                                  map[string]App
	currentBackendNames                   map[string]bool `json:"-"`
}

var tmplCache *template.Template
var tmplCacheErr error
var tmplCacheOnce sync.Once

func reload() error {
	start := time.Now()
	var err error
	data := RenderingData{}
	createRenderingData(&data)
	config.LastUpdates.LastSync = time.Now()

	if !GlobalProxyManager.IsRuntimeAPIUpstreamUpdateEnabled() {
		logger.Debug("Runtime API calls to update upstreams are disabled")

		GlobalProxyManager.UpdateAPIUpdatesHealthStatus(true, "OK: Not in use, full reloads are enabled")

		// Any use of runtime API is disabled, so we must perform a full reload.
		// We still need to calculate backend names to update the database.
		err = updateAndReloadConfig(&data)
		_ = db.UpdateReloadTimestamps(start)
		if err != nil {
			logger.WithFields(logrus.Fields{
				"error": err.Error(),
			}).Error("unable to reload " + data.ProxyPlatform + " config")
			go Metrics.CountFailedReloads.Inc()
			return err
		}
	} else {
		logger.Debug("Runtime API calls to update upstreams are enabled")
		//Use of runtime API is enabled
		//For HAProxy, if config reload is disabled, only use API to update backends. it is responsibility fo config to maintain state across restarts e.g. with global-server-state-file. TO-DO: possible to update config only
		//For Nginx+, ngx http_api maintains it's own state files if referenced in the running nginx config. Hence no templating is done at all when reload is disabled
		if ConfigReloadDisabled {
			logger.Warn(data.ProxyPlatform + ":  reload has been disabled")
		} else {
			vhosts := db.ReadAllKnownVhosts()
			lastKnownVhosts := db.ReadLastKnownVhosts()

			// Generate a set of current backend names to detect changes.
			currentBackendNames := data.currentBackendNames
			lastKnownBackendNames := db.ReadLastKnownBackends()

			// A reload is needed if vhosts have changed OR if the set of backend names has changed.
			// A change in backend names implies a new routingTagValue has been introduced,
			// requiring a new backend block in the config.
			if !reflect.DeepEqual(vhosts, lastKnownVhosts) || !reflect.DeepEqual(currentBackendNames, lastKnownBackendNames) {
				if !reflect.DeepEqual(vhosts, lastKnownVhosts) {
					logger.Info("Vhost changes detected. Need to reload config")
				} else {
					logger.Info("Routing tag changes detected, resulting in new backend names. Need to reload config")
				}

				err = updateAndReloadConfig(&data)
				_ = db.UpdateReloadTimestamps(start)
				if err != nil {
					logger.WithFields(logrus.Fields{
						"error": err.Error(),
					}).Error("unable to update and reload " + data.ProxyPlatform + " config. Runtime api calls to update upstreams will be skipped.")
					return err
				}
			} else {
				logger.Debug("No changes detected in vhosts or backend names. No config update is necessary. Upstream updates will happen via " + config.ProxyPlatform + " apis")
			}
		}
		logger.Debug("Updating upstreams via " + data.ProxyPlatform + " api")
		if GlobalProxyManager != nil {
			err = GlobalProxyManager.Reconcile(&data)
			_ = db.UpdateUpstreamAPIUpdateTimestamps(start)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error": err.Error(),
				}).Error("unable to update upstreams via proxy manager api")
				GlobalProxyManager.UpdateAPIUpdatesHealthStatus(false, err.Error())
				logger.WithFields(logrus.Fields{
					"error": err.Error(),
				}).Error("unable to update upstreams via " + data.ProxyPlatform + " api")
				go Metrics.CountFailedReloads.Inc()
			} else {
				GlobalProxyManager.UpdateAPIUpdatesHealthStatus(true, "OK")
			}
		}
	}
	elapsed := time.Since(start)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("unable to complete reload/reconciliation")
		go Metrics.CountFailedReloads.Inc()
		return err
	}
	logger.WithFields(logrus.Fields{
		"took": elapsed,
	}).Debug("reload worker completed")

	return nil

}

func updateProxyConfig(data *RenderingData) error {
	logger.Debug("Updating " + data.ProxyPlatform + " config")
	config.LastUpdates.LastSync = time.Now()
	err := writeConf(data)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("unable to write " + data.ProxyPlatform + " config")
		go Metrics.CountFailedReloads.Inc()
		return err
	}
	config.LastUpdates.LastConfigValid = time.Now()
	return nil
}

func updateAndReloadConfig(data *RenderingData) error {
	logger.Debug("Updating config with reload")
	start := time.Now()
	vhosts := db.ReadAllKnownVhosts()
	err := updateProxyConfig(data)
	if err != nil {
		return err
	}

	err = GlobalProxyManager.Reload()

	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("unable to reload " + data.ProxyPlatform)
		go Metrics.CountFailedReloads.Inc()
	} else {
		elapsed := time.Since(start)
		go Metrics.CountSuccessfulReloads.Inc()
		go func() {
			Metrics.HistogramReloadDuration.Observe(float64(elapsed) / float64(time.Second))
		}()
		config.LastUpdates.LastProxyProgramReload = time.Now()
		db.UpdateLastKnownVhosts(vhosts)
		db.UpdateLastKnownBackends(data.currentBackendNames)
		//sleep some time for the reload to stabilize
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func createRenderingData(data *RenderingData) {
	namespaceData := db.ReadAllNamespace()
	staticData := db.ReadStaticData()

	data.Xproxy = staticData.Xproxy
	data.ProxyPlatform = config.ProxyPlatform
	data.LeftDelimiter = staticData.LeftDelimiter
	data.RightDelimiter = staticData.RightDelimiter
	data.NginxFailTimeoutUpstream = staticData.NginxFailTimeoutUpstream
	data.NginxMaxFailsUpstream = staticData.NginxMaxFailsUpstream
	data.NginxSlowStartUpstream = staticData.NginxSlowStartUpstream
	data.HaproxySocketAddr = staticData.HaproxySocketAddr
	data.HaproxyAddServerAttributesString = staticData.HaproxyAddServerAttributesString
	data.HaproxyAddServerSSLAttributesString = staticData.HaproxyAddServerSSLAttributesString
	data.HaproxyServerNamePrefix = staticData.HaproxyServerNamePrefix
	data.HaproxyServerNameHostPortSeparator = staticData.HaproxyServerNameHostPortSeparator
	data.HaproxyBackendNameSeparator = staticData.HaproxyBackendNameSeparator
	data.HaproxyBackendIncludeRoutingTagSuffix = staticData.HaproxyBackendIncludeRoutingTagSuffix
	data.Namespaces = make(map[string]NamespaceRenderingData)
	data.currentBackendNames = make(map[string]bool)

	allApps := make(map[string]App)

	for name, nmData := range namespaceData {
		data.Namespaces[name] = NamespaceRenderingData{
			LeaderVHost: nmData.Drove.LeaderVHost,
			Leader:      nmData.Leader,
			RoutingTag:  nmData.Drove.RoutingTag,
		}

		//Merging App if already exists
		for appId, appData := range nmData.Apps {
			appData.RoutingTagKey = nmData.Drove.RoutingTag
			if existingAppData, ok := allApps[appId]; ok {
				//Appending hosts
				existingAppData.Hosts = append(existingAppData.Hosts, appData.Hosts...)

				//adding same as that of app refresh logic in drove client
				if existingAppData.Tags == nil {
					existingAppData.Tags = make(map[string]string)
				}
				if appData.Tags != nil {
					for tagK, tagV := range appData.Tags {
						existingAppData.Tags[tagK] = tagV
					}
				}

				if existingAppData.Groups == nil {
					existingAppData.Groups = make(map[string]HostGroup)
				}

				if appData.Groups != nil {
					for groupName, groupData := range appData.Groups {
						if existingGroup, ok := existingAppData.Groups[groupName]; ok {

							//Appending hosts
							existingGroup.Hosts = append(existingGroup.Hosts, groupData.Hosts...)

							if existingGroup.Tags == nil {
								existingGroup.Tags = make(map[string]string)
							}

							if groupData.Tags != nil {
								for tn, tv := range groupData.Tags {
									existingGroup.Tags[tn] = tv
								}
							}
							existingAppData.Groups[groupName] = existingGroup
						} else {
							existingAppData.Groups[groupName] = groupData
						}
					}
				}
				allApps[appId] = existingAppData
			} else {
				allApps[appId] = appData
			}
		}
	}

	data.Apps = allApps

	for _, app := range data.Apps {
		if app.Vhost != "" {
			for groupName, groupData := range app.Groups {
				logrus.Debug("Group: " + groupName + " Data: " + fmt.Sprintf("%+v", groupData))
				backendName := GlobalProxyManager.GenerateStableBackendName(app, groupName)
				if backendName != "" {
					data.currentBackendNames[backendName] = true
				}
			}
		}
	}

	logger.WithFields(logrus.Fields{
		"data": data,
	}).Trace("Rendering data generated")
	return
}

func renderConfigFromTemplate(tmpl *template.Template, data *RenderingData, file *os.File) error {
	start := time.Now()
	err := tmpl.Execute(file, data)
	duration := time.Since(start)
	resultLabel := "success"
	Metrics.TemplateRenderDuration.WithLabelValues(resultLabel).Observe(float64(duration) / float64(time.Second))
	if err != nil {
		resultLabel = "error"
	}
	return err
}

func writeConf(data *RenderingData) error {
	config.RLock()
	defer config.RUnlock()

	template, err := getTmpl(templatePath)
	if err != nil {
		return err
	}

	parent := filepath.Dir(ConfigPath)
	tmpFile, err := os.CreateTemp(parent, GlobalProxyManager.GetTempFilePattern())
	if err != nil {
		return err
	}
	defer tmpFile.Close()
	defer os.Remove(tmpFile.Name())
	lastConfig = tmpFile.Name()

	err = renderConfigFromTemplate(template, data, tmpFile)
	if err != nil {
		updateHealthSection("Config", false, err.Error())
		return err
	}
	config.LastUpdates.LastConfigRendered = time.Now()
	err = GlobalProxyManager.CheckConfig(tmpFile.Name())
	if err != nil {
		updateHealthSection("Config", false, err.Error())
		logger.Error("Error in config generated")
	} else {
		updateHealthSection("Config", true, "OK")
	}
	if err != nil {
		return err
	}

	logger.WithFields(logrus.Fields{
		"file": ConfigPath,
	}).Info("Writing new config")
	err = os.Rename(tmpFile.Name(), ConfigPath)
	if err != nil {
		return err
	}
	lastConfig = ConfigPath
	return nil
}

func IsUnixSocketAddr(addr string) bool {
	if strings.HasPrefix(addr, "ipv4@") || strings.HasPrefix(addr, "ipv6@") {
		return false
	}
	if strings.Contains(addr, ":") {
		return false
	}
	return true
}

func isHTTPHostGroup(hosts []Host) bool {
	for _, host := range hosts {
		if host.PortType != "http" && host.PortType != "https" {
			return false
		}
	}
	return true
}

func resolveWithIPFallback(hostname string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.DnsResolutionTimeoutSec)*time.Second)
	defer cancel()
	ip, err := resolveHostnameToIP(ctx, hostname)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"hostname": hostname,
			"error":    err,
		}).Warning("DNS resolution failed, falling back to hostname")
		updateHealthSection("ResolverHealth", false, fmt.Sprintf("DNS resolution failed for %s: %v", hostname, err))
		return hostname, err
	}
	updateHealthSection("ResolverHealth", true, "OK")
	return ip, nil
}

func resolveHostnameToIP(ctx context.Context, hostname string) (string, error) {
	resolver := net.Resolver{}
	ips, err := resolver.LookupHost(ctx, hostname)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no IP addresses found for hostname: %s", hostname)
	}
	return ips[0], nil
}

func getTmpl(proxyTemplatePath string) (*template.Template, error) {
	tmplCacheOnce.Do(func() {
		logger.WithFields(logrus.Fields{
			"file": proxyTemplatePath,
		}).Info("Reading template")
		tmplCache, tmplCacheErr = template.New(filepath.Base(proxyTemplatePath)).
			Delims(config.LeftDelimiter, config.RightDelimiter).
			Funcs(template.FuncMap{
				"hasPrefix": strings.HasPrefix,
				"hasSuffix": strings.HasSuffix,
				"contains":  strings.Contains,
				"split":     strings.Split,
				"join":      strings.Join,
				"trim":      strings.Trim,
				"replace":   strings.Replace,
				"tolower":   strings.ToLower,
				"getenv":    os.Getenv,
				"datetime":  time.Now,
			}).
			ParseFiles(proxyTemplatePath)
		if tmplCacheErr != nil {
			logger.WithFields(logrus.Fields{
				"error": tmplCacheErr,
				"file":  proxyTemplatePath,
			}).Error("unable to read template")
			updateHealthSection("Template", false, tmplCacheErr.Error())
		} else {
			logger.WithFields(logrus.Fields{
				"file": proxyTemplatePath,
			}).Info("Template read successfully")
			updateHealthSection("Template", true, "OK")
		}
	})
	return tmplCache, tmplCacheErr
}

func reloadWorker() {
	go func() {
		// a ticker channel to limit reloads to drove, 1s is enough for now.
		ticker := time.NewTicker(1 * time.Second)
		for {
			select {
			case <-ticker.C:
				<-appsConfigUpdateSignalQueue
				reload()
			}
		}
	}()
}
