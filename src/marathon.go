package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
)

// MarathonApps struct for our apps nested with tasks.
type MarathonApps struct {
	Apps []struct {
		ID              string            `json:"id"`
		Labels          map[string]string `json:"labels"`
		Env             map[string]string `json:"env"`
		PortDefinitions []struct {
			Port     int64             `json:"port"`
			Protocol string            `json:"protocol"`
			Name     string            `json:"name"`
			Labels   map[string]string `json:"labels"`
		} `json:"portDefinitions"`
		Container struct {
			PortMappings []struct {
				ContainerPort int64             `json:"containerPort"`
				HostPort      int64             `json:"hostPort"`
				Labels        map[string]string `json:"labels"`
				Protocol      string            `json:"protocol"`
				ServicePort   int64             `json:"servicePort"`
			} `json:"portMappings"`
		} `json:"container"`
		Tasks []struct {
			AppID              string `json:"appId"`
			HealthCheckResults []struct {
				Alive bool `json:"alive"`
			} `json:"healthCheckResults"`
			Host         string  `json:"host"`
			ID           string  `json:"id"`
			Ports        []int64 `json:"ports"`
			ServicePorts []int64 `json:"servicePorts"`
			SlaveID      string  `json:"slaveId"`
			StagedAt     string  `json:"stagedAt"`
			StartedAt    string  `json:"startedAt"`
			State        string  `json:"state"`
			Version      string  `json:"version"`
		} `json:"tasks"`
		HealthChecks []struct {
			Path string `json:"path"`
		} `json:"healthChecks"`
	} `json:"apps"`
}

func eventStream() {
	go func() {
		client := &http.Client{
			Timeout:   0 * time.Second,
			Transport: tr,
		}
		ticker := time.NewTicker(1 * time.Second)
		for range ticker.C {
			var endpoint string
			for _, es := range health.Endpoints {
				if es.Healthy == true {
					endpoint = es.Endpoint
					break
				}
			}
			if endpoint == "" {
				logger.Error("all endpoints are down")
				go countAllEndpointsDownErrors.Inc()
				continue
			}
			req, err := http.NewRequest("GET", endpoint+"/v2/events", nil)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error":    err.Error(),
					"endpoint": endpoint,
				}).Error("unable to create event stream request")
				go countMarathonStreamErrors.Inc()
				continue
			}
			req.Header.Set("Accept", "text/event-stream")
			if config.User != "" {
				req.SetBasicAuth(config.User, config.Pass)
			}
			// Using new context package from Go 1.7
			ctx, cancel := context.WithCancel(context.TODO())
			// initial request cancellation timer of 15s
			timer := time.AfterFunc(15*time.Second, func() {
				cancel()
				logger.Warn("No data for 15s, event stream request was cancelled")
				go countMarathonStreamNoDataWarnings.Inc()
			})
			req = req.WithContext(ctx)
			resp, err := client.Do(req)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error":    err.Error(),
					"endpoint": endpoint,
				}).Error("unable to access Marathon event stream")
				go countMarathonStreamErrors.Inc()
				// expire request cancellation timer immediately
				timer.Reset(100 * time.Millisecond)
				continue
			}
			reader := bufio.NewReader(resp.Body)
			for {
				// reset request cancellation timer to 15s (should be >10s to avoid unnecessary reconnects
				// since ~10s seems to be the rate for dummy/keepalive events on the marathon event stream
				timer.Reset(15 * time.Second)
				line, err := reader.ReadString('\n')
				if err != nil {
					logger.WithFields(logrus.Fields{
						"error":    err.Error(),
						"endpoint": endpoint,
					}).Error("error reading Marathon event stream")
					go countMarathonStreamErrors.Inc()
					resp.Body.Close()
					break
				}
				if !strings.HasPrefix(line, "event: ") {
					continue
				}

				go countMarathonEventsReceived.Inc()
				select {
				case eventqueue <- true: // add reload to our queue channel, unless it is full of course.
				default:
					//logger.Warn("queue is full")
				}
			}
			resp.Body.Close()
			logger.Warn("event stream connection was closed, re-opening")
		}
	}()
}

func endpointHealth() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		for {
			select {
			case <-ticker.C:
				for i, es := range health.Endpoints {
					client := &http.Client{
						Timeout:   5 * time.Second,
						Transport: tr,
					}
					req, err := http.NewRequest("GET", es.Endpoint+"/ping", nil)
					if err != nil {
						logger.WithFields(logrus.Fields{
							"error":    err.Error(),
							"endpoint": es.Endpoint,
						}).Error("an error occurred creating endpoint health request")
						health.Endpoints[i].Healthy = false
						health.Endpoints[i].Message = err.Error()
						continue
					}
					if config.User != "" {
						req.SetBasicAuth(config.User, config.Pass)
					}
					resp, err := client.Do(req)
					if err != nil {
						logger.WithFields(logrus.Fields{
							"error":    err.Error(),
							"endpoint": es.Endpoint,
						}).Error("endpoint is down")
						go countEndpointDownErrors.Inc()
						health.Endpoints[i].Healthy = false
						health.Endpoints[i].Message = err.Error()
						continue
					}
					resp.Body.Close()
					if resp.StatusCode != 200 {
						logger.WithFields(logrus.Fields{
							"status":   resp.StatusCode,
							"endpoint": es.Endpoint,
						}).Error("endpoint check failed")
						go countEndpointCheckFails.Inc()
						health.Endpoints[i].Healthy = false
						health.Endpoints[i].Message = resp.Status
						continue
					}
					health.Endpoints[i].Healthy = true
					health.Endpoints[i].Message = "OK"
				}
			}
		}
	}()
}

func eventWorker() {
	go func() {
		// a ticker channel to limit reloads to marathon, 1s is enough for now.
		ticker := time.NewTicker(1 * time.Second)
		for {
			select {
			case <-ticker.C:
				<-eventqueue
				reload()
			}
		}
	}()
}

func fetchApps(jsonapps *MarathonApps) error {
	var endpoint string
	for _, es := range health.Endpoints {
		if es.Healthy == true {
			endpoint = es.Endpoint
			break
		}
	}
	if endpoint == "" {
		err := errors.New("all endpoints are down")
		return err
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: tr,
	}
	// fetch all apps and tasks with a single request.
	req, err := http.NewRequest("GET", endpoint+"/v2/apps?embed=apps.tasks", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if config.User != "" {
		req.SetBasicAuth(config.User, config.Pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&jsonapps)
	if err != nil {
		return err
	}
	return nil
}

func syncApps(jsonapps *MarathonApps) bool {
	config.Lock()
	defer config.Unlock()
	apps := make(map[string]App)
	for _, app := range jsonapps.Apps {
		traefikBackendLabel := app.Labels[config.TraefikLabel]
		for _, traefikBackend := range config.Traefikbackend {
			if traefikBackend == traefikBackendLabel {
				var newapp = App{}
				if config.Realm != "" && app.Labels["NIXY_REALM"] != config.Realm {
					continue
				}
				for _, task := range app.Tasks {
					// lets skip tasks that does not expose any ports.
					if len(task.Ports) == 0 {
						continue
					}
					// also skip of there is no host set.
					if task.Host == "" {
						continue
					}
					// ignore tasks that are not explicitly running (staging, starting, killing, unreachable, etc)
					if task.State != "TASK_RUNNING" {
						continue
					}
					if len(app.HealthChecks) > 0 {
						if len(task.HealthCheckResults) == 0 {
							// this means tasks is being deployed but not yet monitored as alive. Assume down.
							continue
						}
						alive := true
						for _, health := range task.HealthCheckResults {
							// check if health check is alive
							if health.Alive == false {
								alive = false
							}
						}
						if alive != true {
							// at least one health check has failed. Assume down.
							continue
						}
					}
					var newtask = Task{}
					newtask.AppID = task.AppID
					newtask.Host = task.Host
					newtask.ID = task.ID
					newtask.Ports = task.Ports
					newtask.ServicePorts = task.ServicePorts
					newtask.SlaveID = task.SlaveID
					newtask.StagedAt = task.StagedAt
					newtask.StartedAt = task.StartedAt
					newtask.State = task.State
					newtask.Version = task.Version
					newapp.Tasks = append(newapp.Tasks, newtask)
				}
				// Lets ignore apps if no tasks are available
				if len(newapp.Tasks) > 0 {
					if s, ok := app.Labels["subdomain"]; ok {
						hosts := strings.Split(s, " ")
						r, _ := regexp.Compile(`^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]*[a-zA-Z0-9])\.)*([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9\-]*[A-Za-z0-9])$`)
						for _, host := range hosts {
							if r.MatchString(host) {
								newapp.Hosts = append(newapp.Hosts, host)
							} else {
								logger.WithFields(logrus.Fields{
									"app":       app.ID,
									"subdomain": host,
								}).Warn("invalid subdomain label")
								go countInvalidSubdomainLabelWarnings.Inc()
							}
						}
					} else {
						domains := strings.Split(app.ID[1:], "/")
						for i, j := 0, len(domains)-1; i < j; i, j = i+1, j-1 {
							domains[i], domains[j] = domains[j], domains[i]
						}
						host := strings.Join(domains, ".")
						newapp.Hosts = append(newapp.Hosts, host)
					}
					// Check for duplicated subdomain labels
					for _, confapp := range apps {
						for _, host := range confapp.Hosts {
							for _, newhost := range newapp.Hosts {
								if newhost == host {
									logger.WithFields(logrus.Fields{
										"app":       app.ID,
										"subdomain": host,
									}).Warn("duplicate subdomain label")
									go countDuplicateSubdomainLabelWarnings.Inc()
									// reset hosts if duplicate.
									newapp.Hosts = nil
								}
							}
						}
					}
					// Got duplicated subdomains, lets ignore this one.
					if len(newapp.Hosts) == 0 {
						continue
					}
					newapp.Labels = app.Labels
					newapp.Env = app.Env
					for _, healthcheck := range app.HealthChecks {
						hc := HealthCheck{
							Path: healthcheck.Path,
						}
						newapp.HealthChecks = append(newapp.HealthChecks, hc)
					}
					for _, pds := range app.PortDefinitions {
						pd := PortDefinitions{
							Port:     pds.Port,
							Protocol: pds.Protocol,
							Name:     pds.Name,
							Labels:   pds.Labels,
						}
						newapp.PortDefinitions = append(newapp.PortDefinitions, pd)
					}

					for _, pms := range app.Container.PortMappings {
						pm := PortMappings{
							ContainerPort: pms.ContainerPort,
							HostPort:      pms.HostPort,
							Labels:        pms.Labels,
							Protocol:      pms.Protocol,
							ServicePort:   pms.ServicePort,
						}
						newapp.Container.PortMappings = append(newapp.Container.PortMappings, pm)
					}

					apps[app.ID] = newapp
				}
			}
		}
	}
	// Not all events bring changes, so lets see if anything is new.
	eq := reflect.DeepEqual(apps, config.Apps)

	if eq {
		return true
	}
	config.Apps = apps
	mergeAppsByTraefikBackendLabels(apps)

	return false
}

func mergeAppsByTraefikBackendLabels(apps map[string]App) {
	logger.WithFields(logrus.Fields{}).Info("Merging apps based on traefik.backend marathon label")
	newApps := make(map[string]App, 0)
	labeledApps := make(map[string][]App, 0)

	for appID, app := range apps {
		if labelValue, has := app.Labels[config.TraefikLabel]; has {
			labeledApps[labelValue] = append(labeledApps[labelValue], app)
		} else {
			apps[appID] = app
		}
	}

	for id, appGroup := range labeledApps {
		newApps[id] = mergeApps(appGroup)
	}

	nginxPlus(newApps)
}

func nginxPlus(apps map[string]App) {
	logger.WithFields(logrus.Fields{}).Info("Updating upstreams for the whitelisted marathon labels")
	for _, app := range apps {
		traefikBackendLabel := app.Labels[config.TraefikLabel]
		var newFormattedServers []string
		for _, t := range app.Tasks {
			var hostAndPortMapping string
			ipRecords, error := net.LookupHost(string(t.Host))
			if error != nil {
				logger.WithFields(logrus.Fields{
					"error":    error,
					"hostname": t.Host,
				}).Error("dns lookup failed !! skipping the hostname")
				continue
			}
			ipRecord := ipRecords[0]
			hostAndPortMapping = ipRecord + ":" + strconv.Itoa(int(t.Ports[0]))
			newFormattedServers = append(newFormattedServers, hostAndPortMapping)

		}

		logger.WithFields(logrus.Fields{
			"traefik.backend": traefikBackendLabel,
		}).Info("traefik.backend")

		logger.WithFields(logrus.Fields{
			"upstreams": newFormattedServers,
		}).Info("nginx upstreams")

		endpoint := "http://" + config.Nginxplusapiaddr + "/api"

		tr := &http.Transport{
			MaxIdleConns:       30,
			DisableCompression: true,
		}

		client := &http.Client{Transport: tr}
		c := NginxClient{endpoint, client}
		newclient, error := NewNginxClient(c.httpClient, c.apiEndpoint)

		upstreamtocheck := traefikBackendLabel
		var finalformattedServers []UpstreamServer

		for _, server := range newFormattedServers {
			formattedServer := UpstreamServer{Server: server, MaxFails: config.MaxFailsUpstream, FailTimeout: config.FailTimeoutUpstream, SlowStart: config.SlowStartUpstream}
			finalformattedServers = append(finalformattedServers, formattedServer)
		}

		added, deleted, updated, error := newclient.UpdateHTTPServers(upstreamtocheck, finalformattedServers)

		if added != nil {
			logger.WithFields(logrus.Fields{
				"nginx upstreams added": added,
			}).Info("nginx upstreams added")
		}
		if deleted != nil {
			logger.WithFields(logrus.Fields{
				"nginx upstreams deleted": deleted,
			}).Info("nginx upstreams deleted")
		}
		if updated != nil {
			logger.WithFields(logrus.Fields{
				"nginx upsteams updated": updated,
			}).Info("nginx upstreams updated")
		}
		if error != nil {
			logger.WithFields(logrus.Fields{
				"error": error,
			}).Error("unable to update nginx upstreams")
		}
	}
}

func reload() {
	start := time.Now()
	jsonapps := MarathonApps{}
	err := fetchApps(&jsonapps)

	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("unable to sync from marathon")
		go statsCount("reload.failed", 1)
		go countFailedReloads.Inc()
		return
	}
	equal := syncApps(&jsonapps)
	if equal {
		logger.Info("no config changes")
		return
	}
	config.LastUpdates.LastSync = time.Now()
	config.LastUpdates.LastConfigValid = time.Now()

	elapsed := time.Since(start)
	logger.WithFields(logrus.Fields{
		"took": elapsed,
	}).Info("config updated")
	go statsCount("reload.success", 1)
	go statsTiming("reload.time", elapsed)
	go countSuccessfulReloads.Inc()
	go observeReloadTimeMetric(elapsed)
	config.LastUpdates.LastNginxReload = time.Now()
	return
}
