package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
)

// DroveApps struct for our apps nested with tasks.
type DroveApps struct {
	Status string `json:"status"`
	Apps   []struct {
		ID    string            `json:"appId"`
		Vhost string            `json:"vhost"`
		Tags  map[string]string `json:"tags"`
		Hosts []struct {
			Host     string `json:"host"`
			Port     int32  `json:"port"`
			PortType string `json:"portType"`
		} `json:"hosts"`
	} `json:"data"`
	Message string `json:"message"`
}

type DroveEvents struct {
	Status string `json:"status"`
	Events []struct {
		Type     string                 `json:"type"`
		Id       string                 `json:"id"`
		Time     int64                  `json:"time"`
		Metadata map[string]interface{} `json:"metadata"`
	} `json:"data"`
	Message string `json:"message"`
}

type CurrSyncPoint struct {
	sync.RWMutex
	LastSyncTime int64
}

func leaderController(endpoint string) *LeaderController {
	if endpoint == "" {
		return nil
	}
	var controllerHost = new(LeaderController)
	controllerHost.Endpoint = endpoint
	var parsedUrl, err = url.Parse(endpoint)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
			"url":   endpoint,
		}).Error("could not parse endpoint url")
		return nil
	}
	var host, port, splitErr = net.SplitHostPort(parsedUrl.Host)
	if splitErr != nil {
		logger.WithFields(logrus.Fields{
			"error": splitErr.Error(),
			"url":   endpoint,
		}).Error("could not parse endpoint url")
		return nil
	}
	controllerHost.Host = host
	var iPort, _ = strconv.Atoi(port)
	controllerHost.Port = int32(iPort)
	return controllerHost
}

func fetchRecentEvents(client *http.Client, events *DroveEvents, syncPoint *CurrSyncPoint) error {
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
	//if config.apiTimeout != 0 {
	//    timeout = config.apiTimeout
	//}
	currTime := time.Now().UnixMilli()
	// fetch all apps and tasks with a single request.
	req, err := http.NewRequest("GET", endpoint+"/apis/v1/cluster/events?lastSyncTime="+fmt.Sprint(syncPoint.LastSyncTime), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if config.User != "" {
		req.SetBasicAuth(config.User, config.Pass)
	}
	if config.AccessToken != "" {
		req.Header.Add("Authorization", config.AccessToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&events)
	if err != nil {
		return err
	}
	syncPoint.LastSyncTime = currTime
	return nil
}

func refreshLeaderData() {
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
		return
	}
	if endpoint != config.Leader.Endpoint {
		logger.WithFields(logrus.Fields{
			"new": endpoint,
			"old": config.Leader.Endpoint,
		}).Info("Looks like master shifted. Will resync app")
		var newLeader = leaderController(endpoint)
		if newLeader != nil {
			config.Leader = *newLeader
			logger.WithFields(logrus.Fields{
				"leader": config.Leader,
			}).Info("New leader being set")
			reloadAllApps(true)
		} else {
			logrus.Warn("Leade struct generation failed")
		}
	}
}

func pollEvents() {
	go func() {
		client := &http.Client{
			Timeout:   0 * time.Second,
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		syncData := CurrSyncPoint{}
		refreshInterval := 2
		if config.EventRefreshIntervalSec != 0 {
			refreshInterval = config.EventRefreshIntervalSec
		}
		ticker := time.NewTicker(time.Duration(refreshInterval) * time.Second)
		for range ticker.C {
			func() {
				logger.WithFields(logrus.Fields{
					"at": time.Now(),
				}).Info("Syncing...")
				syncData.Lock()
				defer syncData.Unlock()
				refreshLeaderData()
				var newEvents = DroveEvents{}
				err := fetchRecentEvents(client, &newEvents, &syncData)
				if err != nil {
					logger.WithFields(logrus.Fields{
						"error": err.Error(),
					}).Error("unable to sync events from drove")
				} else {
					if len(newEvents.Events) > 0 {
						logger.WithFields(logrus.Fields{
							"events": newEvents.Events,
						}).Debug("Events received")
						reloadNeeded := false
						for _, event := range newEvents.Events {
							switch eventType := event.Type; eventType {
							case "APP_STATE_CHANGE", "INSTANCE_STATE_CHANGE":
								{
									reloadNeeded = true
								}
							default:
								{
									logger.WithFields(logrus.Fields{
										"type": eventType,
									}).Info("Event ignored")
								}
							}
						}
						if reloadNeeded {
							select {
							case eventqueue <- true: // add reload to our queue channel, unless it is full of course.
							default:
								logger.Warn("queue is full")
							}
						}
					}
				}
				return
			}()
		}
	}()
}

func eventWorker() {
	go func() {
		// a ticker channel to limit reloads to drove, 1s is enough for now.
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

func endpointHealth() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		for {
			select {
			case <-ticker.C:
				//logger.WithFields(logrus.Fields{
				//            "health": health,
				//}).Info("Reloading endpoint health")
				for i, es := range health.Endpoints {
					client := &http.Client{
						Timeout:   5 * time.Second,
						Transport: tr,
						CheckRedirect: func(req *http.Request, via []*http.Request) error {
							return http.ErrUseLastResponse
						},
					}
					req, err := http.NewRequest("GET", es.Endpoint+"/apis/v1/ping", nil)
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
					if config.AccessToken != "" {
						req.Header.Add("Authorization", config.AccessToken)
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
						health.Endpoints[i].Healthy = false
						health.Endpoints[i].Message = resp.Status
						continue
					}
					health.Endpoints[i].Healthy = true
					health.Endpoints[i].Message = "OK"
					logger.WithFields(logrus.Fields{"host": es.Endpoint}).Debug(" Endpoint is healthy")
				}
			}
		}
	}()
}

func fetchApps(jsonapps *DroveApps) error {
	var endpoint string
	var timeout int = 5
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
	if config.apiTimeout != 0 {
		timeout = config.apiTimeout
	}
	client := &http.Client{
		Timeout:   time.Duration(timeout) * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// fetch all apps and tasks with a single request.
	req, err := http.NewRequest("GET", endpoint+"/apis/v1/endpoints", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if config.User != "" {
		req.SetBasicAuth(config.User, config.Pass)
	}
	if config.AccessToken != "" {
		req.Header.Add("Authorization", config.AccessToken)
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

func syncApps(jsonapps *DroveApps) bool {
	config.Lock()
	defer config.Unlock()
	apps := make(map[string]App)
	for _, app := range jsonapps.Apps {
		var newapp = App{}
		for _, task := range app.Hosts {
			var newtask = Host{}
			newtask.Host = task.Host
			newtask.Port = task.Port
			newtask.PortType = strings.ToLower(task.PortType)
			newapp.Hosts = append(newapp.Hosts, newtask)
		}
		// Lets ignore apps if no tasks are available
		if len(newapp.Hosts) > 0 {
			var toAppend = len(config.Realm) == 0 || strings.HasSuffix(app.Vhost, config.Realm)
			if toAppend {
				newapp.ID = app.Vhost
				newapp.Vhost = app.Vhost
				newapp.Tags = app.Tags
				if existingApp, ok := apps[app.Vhost]; ok {
					newapp.Hosts = append(newapp.Hosts, existingApp.Hosts...)
				}
				apps[app.Vhost] = newapp
			} else {
				logger.WithFields(logrus.Fields{
					"realm": config.Realm,
					"vhost": app.Vhost,
				}).Debug("Host ignored due to realm mismath")
			}
		}
	}
	// Not all events bring changes, so lets see if anything is new.
	eq := reflect.DeepEqual(apps, config.Apps)
	if eq {
		return true
	}
	config.Apps = apps
	return false
}

func nginxPlus() error {
	config.RLock()
	defer config.RUnlock()
	logger.WithFields(logrus.Fields{}).Info("Updating upstreams for the whitelisted drove tags")
	for _, app := range config.Apps {
		var newFormattedServers []string
		for _, t := range app.Hosts {
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
			hostAndPortMapping = ipRecord + ":" + fmt.Sprint(t.Port)
			newFormattedServers = append(newFormattedServers, hostAndPortMapping)

		}

		logger.WithFields(logrus.Fields{
			"vhost": app.Vhost,
		}).Info("app.vhost")

		logger.WithFields(logrus.Fields{
			"upstreams": newFormattedServers,
		}).Info("nginx upstreams")

		logger.WithFields(logrus.Fields{
			"nginx": config.Nginxplusapiaddr,
		}).Info("endpoint")

		endpoint := "http://" + config.Nginxplusapiaddr + "/api"

		tr := &http.Transport{
			MaxIdleConns:       30,
			DisableCompression: true,
		}

		client := &http.Client{Transport: tr}
		c := NginxClient{endpoint, client}
		newclient, error := NewNginxClient(c.httpClient, c.apiEndpoint)

		upstreamtocheck := app.Vhost
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
			return error
		}
	}
	return nil
}

func reload() error {
	return reloadAllApps(false)
}

func reloadAllApps(leaderShifted bool) error {
	logger.Debug("Reloading config")
	start := time.Now()
	jsonapps := DroveApps{}
	err := fetchApps(&jsonapps)
	if err != nil || jsonapps.Status != "SUCCESS" {
		if err != nil {
			logger.WithFields(logrus.Fields{
				"error": err.Error(),
			}).Error("unable to sync from drove")
		}
		go statsCount("reload.failed", 1)
		go countFailedReloads.Inc()
		return err
	}
	equal := syncApps(&jsonapps)
	if equal && !leaderShifted {
		logger.Debug("no config changes")
		return nil
	}
	config.LastUpdates.LastSync = time.Now()
	err = nginxPlus()
	if err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("unable to generate nginx config")
		go statsCount("reload.failed", 1)
		go countFailedReloads.Inc()
		return err
	}
	elapsed := time.Since(start)
	logger.WithFields(logrus.Fields{
		"took": elapsed,
	}).Info("config updated")
	go statsCount("reload.success", 1)
	go statsTiming("reload.time", elapsed)
	go countSuccessfulReloads.Inc()
	go observeReloadTimeMetric(elapsed)
	config.LastUpdates.LastNginxReload = time.Now()
	return nil
}
