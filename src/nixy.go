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
	"github.com/Sirupsen/logrus"
	"github.com/avast/retry-go"
	"github.com/gorilla/mux"
	"github.com/peterbourgon/g2s"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Host struct
type Host struct {
	Host     string
	Port     int32
	PortType string
}

// App struct
type App struct {
	ID    string
	Vhost string
	Hosts []Host
	Tags  map[string]string
}

type LeaderController struct {
	Endpoint string
	Host     string
	Port     int32
}

// Config struct used by the template engine
type Config struct {
	sync.RWMutex
	Xproxy                  string
	Realm                   string
	Port                    string   `json:"-"`
	Drove                   []string `json:"-"`
	EventRefreshIntervalSec int      `json:"-"  toml:"event_refresh_interval_sec"`
	User                    string   `json:"-"`
	Pass                    string   `json:"-"`
	AccessToken             string   `json:"-" toml:"access_token"`
	Nginxplusapiaddr        string   `json:"-"`
	MaxFailsUpstream        *int     `json:"max_fails,omitempty"`
	FailTimeoutUpstream     string   `json:"fail_timeout,omitempty"`
	SlowStartUpstream       string   `json:"slow_start,omitempty"`
	LeftDelimiter           string   `json:"-" toml:"left_delimiter"`
	RightDelimiter          string   `json:"-" toml:"right_delimiter"`
	apiTimeout              int      `json:"-" toml:"api_timeout"`
	LeaderVHost             string   `json:"-" toml:"leader_vhost"`
	Leader                  LeaderController
	Statsd                  StatsdConfig
	LastUpdates             Updates
	Apps                    map[string]App
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
	s := &http.Server{
		Addr:    ":" + config.Port,
		Handler: mux,
	}
	health = newHealth()
	endpointHealth()
	err = retry.Do(reload)
	eventWorker()
	pollEvents()
	logger.Info("starting nixy on :" + config.Port)
	err = s.ListenAndServe()
	if err != nil {
		log.Fatal(err)
	}
}
