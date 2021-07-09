package main

import (
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
	"github.com/gorilla/mux"
	"github.com/peterbourgon/g2s"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Task struct
type Task struct {
	AppID        string
	Host         string
	ID           string
	Ports        []int64
	ServicePorts []int64
	SlaveID      string
	StagedAt     string
	StartedAt    string
	State        string
	Version      string
	Labels       map[string]string
}

// PortDefinitions struct
type PortDefinitions struct {
	Port     int64
	Protocol string
	Name     string
	Labels   map[string]string
}

// PortMappings struct
type PortMappings struct {
	ContainerPort int64
	HostPort      int64
	Labels        map[string]string
	Protocol      string
	ServicePort   int64
}

// Container struct
type Container struct {
	PortMappings []PortMappings
}

// HealthCheck struct
type HealthCheck struct {
	Path string
}

// App struct
type App struct {
	Tasks           []Task
	Labels          map[string]string
	Env             map[string]string
	Hosts           []string
	PortDefinitions []PortDefinitions
	HealthChecks    []HealthCheck
	Container       Container
}

// Config struct used by the template engine
type Config struct {
	sync.RWMutex
	Xproxy              string
	Realm               string
	Port                string   `json:"-"`
	Marathon            []string `json:"-"`
	Traefikbackend      []string `json:"-"`
	Nginxplusapiaddr    string   `json:"-"`
	TraefikLabel        string   `json:"-"`
	User                string   `json:"-"`
	Pass                string   `json:"-"`
	NginxConfig         string   `json:"-" toml:"nginx_config"`
	NginxTemplate       string   `json:"-" toml:"nginx_template"`
	NginxCmd            string   `json:"-" toml:"nginx_cmd"`
	NginxIgnoreCheck    bool     `json:"-" toml:"nginx_ignore_check"`
	LeftDelimiter       string   `json:"-" toml:"left_delimiter"`
	RightDelimiter      string   `json:"-" toml:"right_delimiter"`
	MaxFailsUpstream    *int     `json:"max_fails,omitempty"`
	FailTimeoutUpstream string   `json:"fail_timeout,omitempty"`
	SlowStartUpstream   string   `json:"slow_start,omitempty"`
	Statsd              StatsdConfig
	LastUpdates         Updates
	Apps                map[string]App
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
var version = "0.6"                       //set by ldflags
var date = "Fri Jul  9 17:56:01 IST 2021" //set by ldflags
var commit = "SREINFRA-1124"              //set by ldflags
var config = Config{LeftDelimiter: "{{", RightDelimiter: "}}"}
var statsd g2s.Statter
var health Health
var lastConfig string
var logger = logrus.New()

// Eventqueue with buffer of two, because we dont really need more.
var eventqueue = make(chan bool, 2)

// Global http transport for connection reuse
var tr = &http.Transport{MaxIdleConnsPerHost: 10}

func mergeApps(apps []App) App {
	tasks := make([]Task, 0)
	labels := make(map[string]string, 0)
	env := make(map[string]string, 0)
	hosts := make([]string, 0)
	portDefs := make([]PortDefinitions, 0)
	seenPorts := make(map[int64]bool, 0)

	for _, app := range apps {
		for k, v := range app.Labels {
			labels[k] = v
		}
		for k, v := range app.Env {
			env[k] = v
		}
		for _, h := range app.Hosts {
			logger.WithFields(logrus.Fields{
				"app.Hosts": h,
			}).Info("app.Hosts")
			hosts = append(hosts, h)
		}
		for _, t := range app.Tasks {
			logger.WithFields(logrus.Fields{
				"app.Tasks": t,
			}).Info("app.Tasks")
			t.Labels = app.Labels
			tasks = append(tasks, t)
		}
		for _, def := range app.PortDefinitions {
			if _, seen := seenPorts[def.Port]; !seen {
				seenPorts[def.Port] = true
				portDefs = append(portDefs, def)
			}
		}
	}
	return App{
		Tasks:           tasks,
		Labels:          labels,
		Env:             env,
		Hosts:           hosts,
		PortDefinitions: portDefs,
		HealthChecks:    apps[0].HealthChecks,
		Container:       Container{},
	}
}

func newHealth() Health {
	var h Health
	for _, ep := range config.Marathon {
		var s EndpointStatus
		s.Endpoint = ep
		s.Healthy = true
		s.Message = "OK"
		h.Endpoints = append(h.Endpoints, s)
	}

	return h
}

func nixyReload(w http.ResponseWriter, r *http.Request) {
	logger.WithFields(logrus.Fields{
		"client": r.RemoteAddr,
	}).Info("marathon reload triggered")
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
	eventStream()
	eventWorker()
	logger.Info("starting nixy on :" + config.Port)
	err = s.ListenAndServe()
	if err != nil {
		log.Fatal(err)
	}
}
