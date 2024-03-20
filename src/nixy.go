package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/avast/retry-go"
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
	ID     string
	Vhost  string
	Hosts  []Host
	Tags   map[string]string
	Groups map[string]HostGroup
}

type LeaderController struct {
	Endpoint string
	Host     string
	Port     int32
}

type Vhosts struct {
	Vhosts map[string]bool
}

// Config struct used by the template engine
type Config struct {
	sync.RWMutex
	Xproxy                  string
	Realm                   string
	RealmSuffix             string   `json:"-" toml:"realm_suffix"`
	Address                 string   `json:"-"`
	Port                    string   `json:"-"`
	PortWithTLS             bool     `json:"-" toml:"port_use_tls"`
	TLScertFile             string   `json:"-" toml:"port_tls_certfile"`
	TLSkeyFile              string   `json:"-" toml:"port_tls_keyfile"`
	Drove                   []string `json:"-"`
	EventRefreshIntervalSec int      `json:"-"  toml:"event_refresh_interval_sec"`
	User                    string   `json:"-"`
	Pass                    string   `json:"-"`
	AccessToken             string   `json:"-" toml:"access_token"`
	Nginxplusapiaddr        string   `json:"-"`
	NginxReloadDisabled     bool     `json:"-" toml:"nginx_reload_disabled"`
	NginxConfig             string   `json:"-" toml:"nginx_config"`
	NginxTemplate           string   `json:"-" toml:"nginx_template"`
	NginxCmd                string   `json:"-" toml:"nginx_cmd"`
	NginxIgnoreCheck        bool     `json:"-" toml:"nginx_ignore_check"`
	LeftDelimiter           string   `json:"-" toml:"left_delimiter"`
	RightDelimiter          string   `json:"-" toml:"right_delimiter"`
	MaxFailsUpstream        *int     `json:"max_fails,omitempty"`
	FailTimeoutUpstream     string   `json:"fail_timeout,omitempty"`
	SlowStartUpstream       string   `json:"slow_start,omitempty"`
	LogLevel                string   `json:"-" toml:"loglevel"`

	apiTimeout  int    `json:"-" toml:"api_timeout"`
	LeaderVHost string `json:"-" toml:"leader_vhost"`
	RoutingTag  string `json:"-" toml:"routing_tag"`
	Leader      LeaderController
	Statsd      StatsdConfig
	LastUpdates Updates
	Apps        map[string]App
	KnownVHosts Vhosts
}

// Updates timings used for metrics
type Updates struct {
	LastSync           time.Time
	LastConfigRendered time.Time
	LastConfigValid    time.Time
	LastNginxReload    time.Time
}

// StatsdConfig statsd stuct
type StatsdConfig struct {
	Addr       string
	Namespace  string
	SampleRate int `toml:"sample_rate"`
}

// Status health status struct
type Status struct {
	Healthy bool
	Message string
}

// EndpointStatus health status struct
type EndpointStatus struct {
	Endpoint string
	Healthy  bool
	Message  string
}

// Health struct
type Health struct {
	Config    Status
	Template  Status
	Endpoints []EndpointStatus
}

// Global variables
var version = "master" //set by ldflags
var date string        //set by ldflags
var commit string      //set by ldflags
var config = Config{LeftDelimiter: "{{", RightDelimiter: "}}"}
var statsd g2s.Statter
var health Health
var lastConfig string
var logger = logrus.New()

//set log level
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
		logger.Error("unknown loglevel")
		logLevel = logrus.InfoLevel
	}

	logger.SetLevel(logLevel)
}

// Eventqueue with buffer of two, because we dont really need more.
var eventqueue = make(chan bool, 2)

// Global http transport for connection reuse
var tr = &http.Transport{MaxIdleConnsPerHost: 10, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}

func newHealth() Health {
	var h Health
	for _, ep := range config.Drove {
		var s EndpointStatus
		s.Endpoint = ep
		s.Healthy = false
		s.Message = "OK"
		h.Endpoints = append(h.Endpoints, s)
	}
	return h
}

func nixyReload(w http.ResponseWriter, r *http.Request) {
	logger.WithFields(logrus.Fields{
		"client": r.RemoteAddr,
	}).Info("drove reload triggered")
	select {
	case eventqueue <- true: // Add reload to our queue channel, unless it is full of course.
		w.WriteHeader(202)
		fmt.Fprintln(w, "queued")
		return
	default:
		w.WriteHeader(202)
		fmt.Fprintln(w, "queue is full")
		return
	}
}

func nixyHealth(w http.ResponseWriter, r *http.Request) {
	if config.NginxReloadDisabled {
		health.Template.Message = "Templating disabled"
		health.Template.Healthy = true
		health.Config.Message = "Config templating disabled"
		health.Config.Healthy = true
	} else {
		err := checkTmpl()
		if err != nil {
			health.Template.Message = err.Error()
			health.Template.Healthy = false
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			health.Template.Message = "OK"
			health.Template.Healthy = true
		}
		err = checkConf(lastConfig)
		if err != nil {
			health.Config.Message = err.Error()
			health.Config.Healthy = false
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			health.Config.Message = "OK"
			health.Config.Healthy = true
		}
	}
	allBackendsDown := true
	for _, endpoint := range health.Endpoints {
		if endpoint.Healthy {
			allBackendsDown = false
			break
		}
	}
	if allBackendsDown {
		w.WriteHeader(http.StatusInternalServerError)
	}
	w.Header().Add("Content-Type", "application/json; charset=utf-8")
	b, _ := json.MarshalIndent(health, "", "  ")
	w.Write(b)
	return
}

func nixyConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json; charset=utf-8")
	b, _ := json.MarshalIndent(&config, "", "  ")
	w.Write(b)
	return
}

func nixyVersion(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "version: "+version)
	fmt.Fprintln(w, "commit: "+commit)
	fmt.Fprintln(w, "date: "+date)
	return
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
	file, err := ioutil.ReadFile(*configtoml)
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
	// Lets default empty Xproxy to hostname.
	if config.Xproxy == "" {
		config.Xproxy, _ = os.Hostname()
	}
	setloglevel()
	statsd, err = setupStatsd()
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("unable to Dial statsd")
		statsd = g2s.Noop() //fallback to Noop.
	}
	setupPrometheusMetrics()
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
	health = newHealth()
	endpointHealth()
	err = retry.Do(reload)
	eventWorker()
	pollEvents()
	logger.Info("Address:" + config.Address)
	if config.PortWithTLS {
		logger.Info("starting nixy on https://" + config.Address + ":" + config.Port)
		err = s_tls.ListenAndServeTLS(config.TLScertFile, config.TLSkeyFile)
	} else {
		logger.Info("starting nixy on http://" + config.Address + ":" + config.Port)
		err = s.ListenAndServe()
	}
	if err != nil {
		log.Fatal(err)
	}
}
