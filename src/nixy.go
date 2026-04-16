package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gorilla/mux"
	"github.com/peterbourgon/g2s"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

// Host struct
type Host struct {
	Host     string
	Port     int32
	PortType string
}

type HostGroup struct {
	Hosts []Host
	Tags  map[string]string
}

// App struct
type App struct {
	ID            string
	Vhost         string
	Hosts         []Host
	Tags          map[string]string
	Groups        map[string]HostGroup
	RoutingTagKey string `json:"-"`
}

type LeaderController struct {
	Endpoint string
	Host     string
	Port     int32
}

type Vhosts struct {
	Vhosts map[string]bool
}

type DroveNamespace struct {
	Name        string   `json:"-" toml:"name"`
	Drove       []string `json:"-"`
	User        string   `json:"-"`
	Pass        string   `json:"-"`
	AccessToken string   `json:"-" toml:"access_token"`
	Realm       string
	RealmSuffix string `json:"-" toml:"realm_suffix"`
	RoutingTag  string `json:"-" toml:"routing_tag"`
	LeaderVHost string `json:"-" toml:"leader_vhost"`
}

// Config struct used by the template engine
type Config struct {
	sync.RWMutex
	Xproxy                                      string
	Address                                     string           `json:"-"`
	Port                                        string           `json:"-"`
	PortWithTLS                                 bool             `json:"-" toml:"port_use_tls"`
	TLScertFile                                 string           `json:"-" toml:"port_tls_certfile"`
	TLSkeyFile                                  string           `json:"-" toml:"port_tls_keyfile"`
	DroveNamespaces                             []DroveNamespace `json:"-" toml:"namespaces"`
	EventRefreshIntervalSec                     int              `json:"-" toml:"event_refresh_interval_sec"`
	ProxyPlatform                               string           `json:"-" toml:"proxy_platform"`
	Nginxplusapiaddr                            string           `json:"-" toml:"nginxplusapiaddr"`
	NginxReloadDisabled                         bool             `json:"-" toml:"nginx_reload_disabled"`
	NginxConfig                                 string           `json:"-" toml:"nginx_config"`
	NginxTemplate                               string           `json:"-" toml:"nginx_template"`
	NginxCmd                                    string           `json:"-" toml:"nginx_cmd"`
	NginxIgnoreCheck                            bool             `json:"-" toml:"nginx_ignore_check"`
	HaproxySocketAddr                           string           `json:"-" toml:"haproxysocketaddr"`
	HaproxyDisableLargeBackendCountOptimisation bool             `json:"-" toml:"haproxy_disable_large_backend_count_optimisation"`
	HaproxyReloadDisabled                       bool             `json:"-" toml:"haproxy_reload_disabled"`
	HaproxyConfig                               string           `json:"-" toml:"haproxy_config"`
	HaproxyTemplate                             string           `json:"-" toml:"haproxy_template"`
	HaproxyReloadCmd                            string           `json:"-" toml:"haproxy_reload_cmd"`
	HaproxyCmd                                  string           `json:"-" toml:"haproxy_cmd"`
	HaproxyIgnoreCheck                          bool             `json:"-" toml:"haproxy_ignore_check"`
	HaproxyServerNamePrefix                     string           `json:"-" toml:"haproxy_server_name_prefix"`
	HaproxyServerNameHostPortSeparator          string           `json:"-" toml:"haproxy_server_name_host_port_delimiter"`
	HaproxyBackendIncludeRoutingTagSuffix       bool             `json:"-" toml:"haproxy_backend_include_routing_tag_suffix"`
	HaproxyBackendNameSeparator                 string           `json:"-" toml:"haproxy_backend_name_separator"`
	HaproxyAddServerAttributesString            string           `json:"-" toml:"haproxy_add_server_attributes_string"`
	HaproxyAddServerSSLAttributesString         string           `json:"-" toml:"haproxy_add_server_ssl_attributes_string"`
	LeftDelimiter                               string           `json:"-" toml:"left_delimiter"`
	RightDelimiter                              string           `json:"-" toml:"right_delimiter"`
	NginxMaxFailsUpstream                       int              `json:"-" toml:"nginx_max_fails"`
	NginxFailTimeoutUpstream                    string           `json:"-" toml:"nginx_fail_timeout"`
	NginxSlowStartUpstream                      string           `json:"-" toml:"nginx_slow_start"`
	NginxMaxFailsUpstreamCompatibility          *int             `json:"-" toml:"maxfailsupstream,omitempty"`
	NginxFailTimeoutUpstreamCompatibility       *string          `json:"-" toml:"failtimeoutupstream,omitempty"`
	NginxSlowStartUpstreamCompatibility         *string          `json:"-" toml:"slowstartupstream,omitempty"`
	LogLevel                                    string           `json:"-" toml:"loglevel"`
	DnsResolutionTimeoutSec                     int              `json:"-" toml:"dns_resolution_timeout_sec"`
	apiTimeout                                  int              `json:"-" toml:"api_timeout"`
	LastUpdates                                 Updates
}

// Updates timings used for metrics
type Updates struct {
	LastSync               time.Time
	LastConfigRendered     time.Time
	LastConfigValid        time.Time
	LastProxyProgramReload time.Time
}

// Status health status struct
type Status struct {
	Healthy bool
	Message string
}

// EndpointStatus health status struct
type EndpointStatus struct {
	Namespace string
	Endpoint  string
	Healthy   bool
	Message   string
}

type NamespaceStatus struct {
	Namespace string
	Healthy   bool
	Message   string
}

// Health struct
type Health struct {
	sync.RWMutex
	Config                Status
	Template              Status
	UpstreamUpdatesViaAPI Status
	ResolverHealth        Status
	NamespaceHealth       map[string]NamespaceStatus
	NamespaceEndpoints    map[string][]EndpointStatus
}

// Global variables
var version = "master" //set by ldflags
var date string        //set by ldflags
var commit string      //set by ldflags
var config = Config{LeftDelimiter: "{{", RightDelimiter: "}}"}
var statsd g2s.Statter
var health Health
var lastConfig string
var db DataManager
var logger = logrus.New()

var ConfigPath string
var templatePath string
var ReloadCmd string
var ProgramCmd string
var ConfigReloadDisabled bool
var ProgramCmdConfFileArg string
var ProgramCmdConfTestArg string

var Metrics DroveGatewayPrometheusMetrics

// Global proxy manager instance
var GlobalProxyManager ProxyManager

// set log level
func setloglevel() {
	logLevel := logrus.InfoLevel
	switch config.LogLevel {
	case "trace":
		logLevel = logrus.TraceLevel
	case "debug":
		logLevel = logrus.DebugLevel
	case "info":
		logLevel = logrus.InfoLevel
	case "warn":
		logLevel = logrus.WarnLevel
	case "error":
		logLevel = logrus.ErrorLevel
	default:
		logger.Warn("unknown loglevel. Defaulting to info")
		logLevel = logrus.InfoLevel
	}

	logger.SetLevel(logLevel)
}

// set DataManager
func setupDataManager() {
	db = *NewDataManager(config.Xproxy, config.ProxyPlatform, config.LeftDelimiter, config.RightDelimiter,
		config.NginxMaxFailsUpstream, config.NginxFailTimeoutUpstream, config.NginxSlowStartUpstream,
		config.HaproxySocketAddr, config.HaproxyAddServerAttributesString, config.HaproxyAddServerSSLAttributesString, config.HaproxyServerNamePrefix, config.HaproxyServerNameHostPortSeparator, config.HaproxyBackendNameSeparator, config.HaproxyBackendIncludeRoutingTagSuffix)
	for _, nsConfig := range config.DroveNamespaces {
		db.CreateNamespace(nsConfig.Name, nsConfig.Drove, nsConfig.User, nsConfig.Pass,
			nsConfig.AccessToken, nsConfig.Realm, nsConfig.RealmSuffix, nsConfig.RoutingTag, nsConfig.LeaderVHost)
	}
}

// Reload signal with buffer of two, because we dont really need more.
var appsConfigUpdateSignalQueue = make(chan bool, 2)
var eventRefreshSignalQueue = make(chan bool, 2)

// Global http transport for connection reuse
var tr = &http.Transport{MaxIdleConnsPerHost: 10, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

func newHealth() {
	health.NamespaceEndpoints = make(map[string][]EndpointStatus)
	health.NamespaceHealth = make(map[string]NamespaceStatus)
	health.UpstreamUpdatesViaAPI = Status{
		Healthy: false,
		Message: "pending first check",
	}
	health.ResolverHealth = Status{
		Healthy: true,
		Message: "pending first check",
	}
	if ConfigReloadDisabled {
		health.Config.Message = "Config Reload disabled"
		health.Config.Healthy = true
		health.Template.Message = "Templating disabled"
		health.Template.Healthy = true
	} else {
		health.Config = Status{
			Healthy: false,
			Message: "pending first check",
		}
		health.Template = Status{
			Healthy: false,
			Message: "pending first check",
		}
	}
	for _, nsConfig := range config.DroveNamespaces {
		e := []EndpointStatus{}
		for _, ep := range nsConfig.Drove {
			var s EndpointStatus
			s.Endpoint = ep
			s.Namespace = nsConfig.Name
			s.Healthy = false
			s.Message = "OK"
			e = append(e, s)
		}
		health.NamespaceEndpoints[nsConfig.Name] = e
		health.NamespaceHealth[nsConfig.Name] = NamespaceStatus{
			Namespace: nsConfig.Name,
			Healthy:   false,
			Message:   "pending first check",
		}
	}
}

func setupDefaultConfig() {
	// Lets default empty Xproxy to hostname.
	if config.Xproxy == "" {
		config.Xproxy, _ = os.Hostname()
	}

	//default refresh interval
	if config.EventRefreshIntervalSec <= 0 {
		config.EventRefreshIntervalSec = 2
	}

	//default apiTimeout
	if config.apiTimeout <= 0 {
		config.apiTimeout = 10
	}

	if config.DnsResolutionTimeoutSec <= 0 {
		config.DnsResolutionTimeoutSec = 2
	}

	//default proxyplatform as nginx
	if config.ProxyPlatform == "" {
		config.ProxyPlatform = "nginx"
	}

	//set proxy platform parameters
	if config.ProxyPlatform == "nginx" {
		ConfigPath = config.NginxConfig
		templatePath = config.NginxTemplate
		ReloadCmd = config.NginxCmd
		ProgramCmd = config.NginxCmd
		ConfigReloadDisabled = config.NginxReloadDisabled
		if ConfigReloadDisabled {
			logger.Warn("Nginx reloads are disabled. Although reloads won't be done it is important that the configured config uses upstream state file block in upstream block so as to allow nginx_http_api to update upstreams to state files")
		}
		ProgramCmdConfFileArg = "-c"
		ProgramCmdConfTestArg = "-t"
		//default NginxMaxFailsUpstream is 0 as omitempty is not present
		//Older versions of nixy used different naming for these parameters, so we support both for backward compatibility
		if config.NginxMaxFailsUpstreamCompatibility != nil {
			config.NginxMaxFailsUpstream = *config.NginxMaxFailsUpstreamCompatibility
			logger.Warn("maxfailsupstream is deprecated, please use nginx_max_fails instead")
		}
		if config.NginxFailTimeoutUpstreamCompatibility != nil {
			config.NginxFailTimeoutUpstream = *config.NginxFailTimeoutUpstreamCompatibility
			logger.Warn("failtimeoutupstream is deprecated, please use nginx_fail_timeout instead")
		}
		if config.NginxSlowStartUpstreamCompatibility != nil {
			config.NginxSlowStartUpstream = *config.NginxSlowStartUpstreamCompatibility
			logger.Warn("slowstartupstream is deprecated, please use nginx_slow_start instead")
		}
		if config.NginxFailTimeoutUpstream == "" || !regexp.MustCompile(`^\d+s$`).MatchString(config.NginxFailTimeoutUpstream) {
			config.NginxFailTimeoutUpstream = "0s"
			logger.Error("Invalid input to failtimeoutupstream, defaulting to " + config.NginxFailTimeoutUpstream)
		}
		if config.NginxSlowStartUpstream == "" || !regexp.MustCompile(`^\d+s$`).MatchString(config.NginxSlowStartUpstream) {
			config.NginxSlowStartUpstream = "0s"
			logger.Error("Invalid input to slowstartupstream, defaulting to " + config.NginxSlowStartUpstream)
		}
		logger.WithFields(logrus.Fields{"max_fails": config.NginxMaxFailsUpstream, "fail_timeout": config.NginxFailTimeoutUpstream, "slow_start": config.NginxSlowStartUpstream}).Debug("Nginx upstream healthcheck parameters set")
	} else if config.ProxyPlatform == "haproxy" {
		ConfigPath = config.HaproxyConfig
		templatePath = config.HaproxyTemplate
		ReloadCmd = config.HaproxyReloadCmd
		ProgramCmd = config.HaproxyCmd
		ConfigReloadDisabled = config.HaproxyReloadDisabled
		if ConfigReloadDisabled {
			logger.Warn("Haproxy reloads are DISABLED. Unlike in nginx, This is NOT a recommended configuration as haproxy is dependent on config to maintain state across restarts. haproxy's reload from global state file feature should be used and restarts accordingly handled\n")
		}
		ProgramCmdConfFileArg = "-f"
		ProgramCmdConfTestArg = "-c"
		if config.HaproxyBackendNameSeparator == "" {
			config.HaproxyBackendNameSeparator = "_"
		}
		if config.HaproxyServerNamePrefix == "" {
			config.HaproxyServerNamePrefix = "server"
		}
		if config.HaproxyBackendIncludeRoutingTagSuffix {
			logger.Info("Haproxy backend names will include routing tag suffix")
		}
		if config.HaproxyServerNameHostPortSeparator == "" {
			config.HaproxyServerNameHostPortSeparator = "_"
		}
		if config.HaproxyAddServerAttributesString == "" {
			config.HaproxyAddServerAttributesString = ""
			//E.g check inter 2000 fall 3 rise 2. Not enabling healthchecks by default.
		}
		if config.HaproxyAddServerSSLAttributesString == "" {
			config.HaproxyAddServerSSLAttributesString = "ssl verify none"
			//E.g ssl verify required ca-file ca-certificates.crt
		}
	}
}

func validateConfig() error {
	for _, nsConfig := range config.DroveNamespaces {
		if nsConfig.Name == "" {
			return errors.New("drove namespace name is mandatory")
		}
	}

	if config.ProxyPlatform == "haproxy" {
		if (config.HaproxyReloadDisabled) && (config.HaproxySocketAddr == "") {
			return errors.New("haproxy socket address is mandatory when reloads are disabled, can't update runtime servers")
		}
		if config.HaproxyIgnoreCheck {
			logger.Warn("Haproxy config check is disabled, this may lead to invalid configs being applied")
		}
	}

	if config.ProxyPlatform == "nginx" {
		if (config.NginxReloadDisabled) && (config.Nginxplusapiaddr == "") {
			return errors.New("nginx-plus api address is mandatory when reloads are disabled. can't update upstreams")
		}
		if config.NginxIgnoreCheck {
			logger.Warn("Nginx config check is disabled, this may lead to invalid configs being applied")
		}
	}

	return nil
}

func nixyReload(w http.ResponseWriter, r *http.Request) {
	logger.WithFields(logrus.Fields{
		"client": r.RemoteAddr,
	}).Info("Reload triggered via /v1/reload")
	queued := true
	select {
	case eventRefreshSignalQueue <- true: // Add referesh to our signal channel, unless it is full of course.
	default:
		queued = false
	}
	if queued {
		w.WriteHeader(202)
		fmt.Fprintln(w, "queued")
		return
	}
	w.WriteHeader(202)
	fmt.Fprintln(w, "queue is full")
	return
}

func nixyHealth(w http.ResponseWriter, r *http.Request) {
	anyNamespaceDown := false
	for _, nsEnpoint := range health.NamespaceEndpoints {
		allBackendsDownForGivenNS := true
		for _, endpoint := range nsEnpoint {
			if endpoint.Healthy {
				allBackendsDownForGivenNS = false
				break
			}
		}
		anyNamespaceDown = anyNamespaceDown || allBackendsDownForGivenNS
	}

	// the health is set by the respective workers, we just read it here.
	if !health.Template.Healthy || !health.Config.Healthy || !health.ResolverHealth.Healthy || !health.UpstreamUpdatesViaAPI.Healthy || anyNamespaceDown {
		w.WriteHeader(http.StatusInternalServerError)
	}

	w.Header().Add("Content-Type", "application/json; charset=utf-8")
	b, _ := json.MarshalIndent(&health, "", "  ")
	w.Write(b)
	return
}

func nixyConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json; charset=utf-8")
	b, _ := json.MarshalIndent(&db, "", "  ")
	w.Write(b)
	return
}

func nixyVersion(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "version: "+version)
	fmt.Fprintln(w, "commit: "+commit)
	fmt.Fprintln(w, "date: "+date)
	return
}

func updateHealthSection(section string, status bool, message string) {
	var statusFloat float64
	if status {
		statusFloat = 1.0
	} else {
		statusFloat = 0.0
	}
	health.Lock()
	switch section {
	case "ResolverHealth":
		health.ResolverHealth.Healthy = status
		health.ResolverHealth.Message = message
		Metrics.GaugeResolverHealthy.Set(statusFloat)
	case "Config":
		health.Config.Healthy = status
		health.Config.Message = message
		Metrics.GaugeConfigGenerationHealthy.Set(statusFloat)
	case "Template":
		health.Template.Healthy = status
		health.Template.Message = message
		Metrics.GaugeTemplateRenderingHealthy.Set(statusFloat)
	case "UpstreamUpdatesViaAPI":
		health.UpstreamUpdatesViaAPI.Healthy = status
		health.UpstreamUpdatesViaAPI.Message = message
		Metrics.GaugeUpstreamUpdatesViaAPIHealthy.Set(statusFloat)
	default:
		return
	}
	health.Unlock()
}

// Implement ProxyManager interface for NginxAPIManager
func (manager *NginxAPIManager) Reconcile(data *RenderingData) error {
	return manager.ReconcileAllVhosts(data)
}

// Implement ProxyManager interface for HaproxyManager
func (manager *HaproxyManager) Reconcile(data *RenderingData) error {
	return manager.ReconcileAllBackends(data, config.HaproxyDisableLargeBackendCountOptimisation)
}

func main() {
	configtoml := flag.String("f", "nixy.toml", "Path to config. (default nixy.toml)")
	versionflag := flag.Bool("v", false, "prints current nixy version")
	flag.Parse()
	if *versionflag {
		fmt.Printf("version: %s\n", version)
		fmt.Printf("commit: %s\n", commit)
		fmt.Printf("date: %s\n", date)
		os.Exit(0)
	}
	file, err := os.ReadFile(*configtoml)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Fatal("problem opening toml config")
	}
	err = toml.Unmarshal(file, &config)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Fatal("problem parsing config")
	}
	setloglevel()
	err = validateConfig()
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Fatal("problem in config")
	}

	setupDefaultConfig()
	setupPrometheusMetrics()
	setupDataManager()
	GlobalProxyManager = setupGlobalProxyManager()
	if GlobalProxyManager == nil {
		logger.Fatal("Failed to setup global proxy manager, exiting nixy")
	}

	mux := mux.NewRouter()
	mux.HandleFunc("/", nixyVersion)
	mux.HandleFunc("/v1/reload", nixyReload)
	mux.HandleFunc("/v1/config", nixyConfig)
	mux.HandleFunc("/v1/health", nixyHealth)
	mux.Handle("/v1/metrics", promhttp.Handler())
	var s_tls *http.Server
	var s *http.Server
	if config.PortWithTLS {
		cfg := &tls.Config{
			MinVersion:               tls.VersionTLS12,
			CurvePreferences:         []tls.CurveID{tls.CurveP521, tls.CurveP384, tls.CurveP256},
			PreferServerCipherSuites: true,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			},
		}
		s_tls = &http.Server{
			Addr:         config.Address + ":" + config.Port,
			Handler:      mux,
			TLSConfig:    cfg,
			TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler), 0),
		}
	} else {
		s = &http.Server{
			Addr:    config.Address + ":" + config.Port,
			Handler: mux,
		}
	}
	newHealth()
	setupEndpointHealth()
	setupPollEvents()
	reloadWorker() //Reloader
	// forceReload()
	logger.Info("Address:" + config.Address)
	if config.PortWithTLS {
		logger.Info("starting nixy on https://" + config.Address + ":" + config.Port)
		err = s_tls.ListenAndServeTLS(config.TLScertFile, config.TLSkeyFile)
	} else {
		logger.Info("starting nixy on http://" + config.Address + ":" + config.Port)
		err = s.ListenAndServe()
	}
	if err != nil {
		logger.Fatal(err)
	}
}
