package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type DroveConfig struct {
	Name        string
	Drove       []string `json:"-"`
	User        string   `json:"-"`
	Pass        string   `json:"-"`
	AccessToken string   `json:"-" toml:"access_token"`
	Realm       string
	RealmSuffix string `json:"-" toml:"realm_suffix"`
	RoutingTag  string `json:"-" toml:"routing_tag"`
	LeaderVHost string `json:"-" toml:"leader_vhost"`
}

// NamespaceData holds the data and metadata for each namespace
type NamespaceData struct {
	Drove       DroveConfig
	Leader      LeaderController
	Apps        map[string]App
	KnownVHosts Vhosts
	Timestamp   time.Time // Timestamp of creation or modification
}

type StaticConfig struct {
	Xproxy                                string
	ProxyPlatform                         string `json:"-" toml:"proxy_platform"`
	LeftDelimiter                         string `json:"-" toml:"left_delimiter"`
	RightDelimiter                        string `json:"-" toml:"right_delimiter"`
	NginxMaxFailsUpstream                 int    `json:"-" toml:"max_fails"`
	NginxFailTimeoutUpstream              string `json:"-" toml:"nginx_fail_timeout"`
	NginxSlowStartUpstream                string `json:"-" toml:"nginx_slow_start"`
	HaproxySocketAddr                     string `json:"-" toml:"haproxy_socket_addr"`
	HaproxyAddServerAttributesString      string `json:"-" toml:"haproxy_add_server_attributes_string"`
	HaproxyAddServerSSLAttributesString   string `json:"-" toml:"haproxy_add_server_ssl_attributes_string"`
	HaproxyServerNamePrefix               string `json:"-" toml:"haproxy_server_name_prefix"`
	HaproxyServerNameHostPortSeparator    string `json:"-" toml:"haproxy_server_name_host_port_delimiter"`
	HaproxyBackendNameSeparator           string `json:"-" toml:"haproxy_backend_name_separator"`
	HaproxyBackendIncludeRoutingTagSuffix bool   `json:"-" toml:"haproxy_backend_include_routing_tag_suffix"`
}

// DataManager manages namespaces and data for those namespaces
type DataManager struct {
	mu                             sync.RWMutex             // Mutex for concurrency control
	namespaces                     map[string]NamespaceData // Map of namespaces to NamespaceData
	LastKnownVhosts                Vhosts
	LastKnownBackends              map[string]bool
	LastReloadTimestamp            time.Time // Timestamp of creation or modification
	LastUpstreamAPIUpdateTimestamp time.Time
	StaticData                     StaticConfig
}

// NewDataManager creates a new instance of DataManager
func NewDataManager(inXproxy string, inProxyPlatform string, inLeftDelimiter string, inRightDelimiter string,
	inNginxMaxFailsUpstream int, inNginxFailTimeoutUpstream string, inNginxSlowStartUpstream string, inHaproxySocketAddr string, inHaproxyAddServerAttributesString string, inHaproxyAddServerSSLAttributesString string,
	inHaproxyServerNamePrefix string, inHaproxyServerNameHostPortSeparator string, inHaproxyBackendNameSeparator string, inHaproxyBackendIncludeRoutingTagSuffix bool) *DataManager {
	emptyLastKnownVhosts := Vhosts{}
	emptyLastKnownVhosts.Vhosts = make(map[string]bool)
	return &DataManager{
		namespaces: make(map[string]NamespaceData),
		StaticData: StaticConfig{Xproxy: inXproxy, ProxyPlatform: inProxyPlatform, LeftDelimiter: inLeftDelimiter, RightDelimiter: inRightDelimiter,
			NginxMaxFailsUpstream: inNginxMaxFailsUpstream, NginxFailTimeoutUpstream: inNginxFailTimeoutUpstream, NginxSlowStartUpstream: inNginxSlowStartUpstream,
			HaproxySocketAddr: inHaproxySocketAddr, HaproxyAddServerAttributesString: inHaproxyAddServerAttributesString, HaproxyAddServerSSLAttributesString: inHaproxyAddServerSSLAttributesString,
			HaproxyServerNamePrefix: inHaproxyServerNamePrefix, HaproxyServerNameHostPortSeparator: inHaproxyServerNameHostPortSeparator,
			HaproxyBackendNameSeparator: inHaproxyBackendNameSeparator, HaproxyBackendIncludeRoutingTagSuffix: inHaproxyBackendIncludeRoutingTagSuffix},
		LastKnownVhosts:                emptyLastKnownVhosts,
		LastKnownBackends:              make(map[string]bool),
		LastReloadTimestamp:            time.Time{},
		LastUpstreamAPIUpdateTimestamp: time.Time{},
	}
}

// Create inserts data into a namespace
func (dm *DataManager) CreateNamespace(namespace string, inDrove []string, inUser string, inPass string, inAccessToken string,
	inRealm string, inRealmSuffix string, inRoutingTag string, inLeaderVhost string) error {
	dm.mu.Lock()         // Lock to ensure concurrent writes are handled
	defer dm.mu.Unlock() // Ensure the lock is always released

	// Start the operation log
	logger.WithFields(logrus.Fields{
		"operation": "create",
		"namespace": namespace,
	}).Info("Attempting to create Namespace")

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		dm.namespaces[namespace] = NamespaceData{
			Drove: DroveConfig{
				Name:        namespace,
				Drove:       inDrove,
				User:        inUser,
				Pass:        inPass,
				AccessToken: inAccessToken,
				Realm:       inRealm,
				RealmSuffix: inRealmSuffix,
				RoutingTag:  inRoutingTag,
				LeaderVHost: inLeaderVhost,
			},
			Timestamp: time.Now(),
		}
	}

	// Log success
	logger.WithFields(logrus.Fields{
		"operation": "create",
		"namespace": namespace,
	}).Info("Namespace created successfully")
	return nil
}

func (dm *DataManager) ReadDroveConfig(namespace string) (DroveConfig, error) {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadDroveConfig"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return DroveConfig{}, err
	}

	ns := dm.namespaces[namespace]

	// Log success
	logger.WithFields(logrus.Fields{
		"operation": operation,
		"namespace": namespace,
	}).Trace("ReadDroveConfig successfully")

	return ns.Drove, nil //returning copy
}

func (dm *DataManager) ReadLastTimestamp(namespace string) (time.Time, error) {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadLastTimestamps"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return time.Time{}, err
	}

	ns := dm.namespaces[namespace]

	// Log success
	logger.WithFields(logrus.Fields{
		"operation": operation,
		"namespace": namespace,
		"Timestamp": ns.Timestamp,
	}).Trace("ReadLastTimestamps successfully")

	return ns.Timestamp, nil //returning copy
}

func (dm *DataManager) ReadLeader(namespace string) (LeaderController, error) {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadLeader"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return LeaderController{}, err
	}

	ns := dm.namespaces[namespace]

	// Log success
	logger.WithFields(logrus.Fields{
		"operation": operation,
		"namespace": namespace,
		"leader":    ns.Leader,
	}).Trace("ReadLeader successfully")

	return ns.Leader, nil //returning copy
}

func (dm *DataManager) UpdateLeader(namespace string, leader LeaderController) error {
	dm.mu.Lock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.Unlock() // Ensure the lock is always released
	operation := "UpdateLeader"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return err
	}

	ns := dm.namespaces[namespace]
	ns.Leader = leader
	ns.Timestamp = time.Now() // Update timestamp on modification
	dm.namespaces[namespace] = ns

	logger.WithFields(logrus.Fields{
		"operation": operation,
		"namespace": namespace,
		"leader":    ns.Leader,
		"time":      ns.Timestamp,
	}).Trace("UpdateLeader data finished successfully")
	return nil
}

func (dm *DataManager) ReadApps(namespace string) (map[string]App, error) {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadLeader"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return map[string]App{}, err
	}

	ns := dm.namespaces[namespace]

	// Log success
	logger.WithFields(logrus.Fields{
		"operation": operation,
		"namespace": namespace,
		"apps":      ns.Apps,
	}).Trace("ReadApp successfully")

	return ns.Apps, nil //returning copy
}

func (dm *DataManager) UpdateApps(namespace string, apps map[string]App) error {
	dm.mu.Lock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.Unlock() // Ensure the lock is always released
	operation := "UpdateApps"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return err
	}

	ns := dm.namespaces[namespace]
	ns.Apps = apps
	ns.Timestamp = time.Now() // Update timestamp on modification
	dm.namespaces[namespace] = ns

	logger.WithFields(logrus.Fields{
		"operation": operation,
		"namespace": namespace,
		"apps":      dm.namespaces[namespace].Apps,
		"time":      dm.namespaces[namespace].Timestamp,
	}).Trace("UpdateApps finished successfully")
	return nil
}

func (dm *DataManager) ReadKnownVhosts(namespace string) (Vhosts, error) {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadKnownVhosts"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return Vhosts{}, err
	}

	ns := dm.namespaces[namespace]

	// Log success
	logger.WithFields(logrus.Fields{
		"operation":   operation,
		"namespace":   namespace,
		"knownVHosts": ns.KnownVHosts,
	}).Trace("ReadKnownVhosts successfully")

	return ns.KnownVHosts, nil //returning copy
}

func (dm *DataManager) ReadAllKnownVhosts() Vhosts {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadAllKnownVhosts"

	allKnownVhosts := Vhosts{}
	allKnownVhosts.Vhosts = make(map[string]bool)

	for _, data := range dm.namespaces {
		for key, value := range data.KnownVHosts.Vhosts {
			allKnownVhosts.Vhosts[key] = value
		}
	}

	// Log success
	logger.WithFields(logrus.Fields{
		"operation": operation,
		"allApps":   allKnownVhosts,
	}).Trace("ReadAllKnownVhosts successfully")

	return allKnownVhosts //returning copy
}

func (dm *DataManager) UpdateKnownVhosts(namespace string, KnownVHosts Vhosts) error {
	dm.mu.Lock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.Unlock() // Ensure the lock is always released
	operation := "UpdateKnownVhosts"

	// Ensure namespace exists
	if _, exists := dm.namespaces[namespace]; !exists {
		logger.WithFields(logrus.Fields{
			"operation": operation,
			"namespace": namespace,
		}).Error("NamespaceData read failed")
		err := fmt.Errorf("namespace '%s' not found", namespace)
		return err
	}

	ns := dm.namespaces[namespace]
	ns.KnownVHosts = KnownVHosts
	ns.Timestamp = time.Now() // Update timestamp on modification
	dm.namespaces[namespace] = ns

	logger.WithFields(logrus.Fields{
		"operation":  operation,
		"namespace":  namespace,
		"knownHosts": dm.namespaces[namespace].KnownVHosts,
		"time":       dm.namespaces[namespace].Timestamp,
	}).Debug("UpdateKnownVhosts finished successfully")
	return nil
}

func (dm *DataManager) ReadLastReloadTimestamps() time.Time {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "LastReloadTimestamp"

	// Log success
	logger.WithFields(logrus.Fields{
		"operation":           operation,
		"LastReloadTimestamp": dm.LastReloadTimestamp,
	}).Trace("ReadLastReloadTimestamps successfully")

	return dm.LastReloadTimestamp //returning copy
}
func (dm *DataManager) UpdateReloadTimestamps(startTime time.Time) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	operation := "UpdateReloadTimestamp"

	dm.LastReloadTimestamp = startTime

	logger.WithFields(logrus.Fields{
		"operation":           operation,
		"LastReloadTimestamp": dm.LastReloadTimestamp,
	}).Trace("UpdateLastReloadTimestamps finished successfully")
	return nil
}

func (dm *DataManager) ReadLastUpstreamAPIUpdateTimestamps() time.Time {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "LastUpstreamAPIUpdateTimestamp"
	// Log success
	logger.WithFields(logrus.Fields{
		"operation":                      operation,
		"LastUpstreamAPIUpdateTimestamp": dm.LastUpstreamAPIUpdateTimestamp,
	}).Trace("ReadLastUpstreamAPIUpdateTimestamps successfully")

	return dm.LastUpstreamAPIUpdateTimestamp //returning copy
}

func (dm *DataManager) UpdateUpstreamAPIUpdateTimestamps(updateTime time.Time) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	operation := "UpdateUpstreamAPIUpdateTimestamp"

	dm.LastUpstreamAPIUpdateTimestamp = updateTime

	logger.WithFields(logrus.Fields{
		"operation":                      operation,
		"LastUpstreamAPIUpdateTimestamp": dm.LastUpstreamAPIUpdateTimestamp,
	}).Trace("UpdateLastUpstreamAPIUpdateTimestamps finished successfully")
	return nil
}

func (dm *DataManager) ReadLastKnownVhosts() Vhosts {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadLastKnownVhosts"

	// Log success
	logger.WithFields(logrus.Fields{
		"operation":       operation,
		"LastKnownVhosts": dm.LastKnownVhosts,
	}).Trace("LastKnownVhosts successfully")

	return dm.LastKnownVhosts //returning copy
}

func (dm *DataManager) UpdateLastKnownVhosts(inLastKnownVhosts Vhosts) error {
	dm.mu.Lock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.Unlock() // Ensure the lock is always released
	operation := "UpdateLastKnownVhosts"

	dm.LastKnownVhosts = inLastKnownVhosts

	logger.WithFields(logrus.Fields{
		"operation":       operation,
		"LastKnownVhosts": dm.LastKnownVhosts,
	}).Debug("UpdateLastKnownVhosts finished successfully")
	return nil
}

func (dm *DataManager) ReadLastKnownBackends() map[string]bool {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	operation := "ReadLastKnownBackends"

	// Log success
	logger.WithFields(logrus.Fields{
		"operation":         operation,
		"LastKnownBackends": dm.LastKnownBackends,
	}).Trace("ReadLastKnownBackends successfully")

	return dm.LastKnownBackends //returning copy
}

func (dm *DataManager) UpdateLastKnownBackends(inLastKnownBackends map[string]bool) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	operation := "UpdateLastKnownBackends"

	dm.LastKnownBackends = inLastKnownBackends

	logger.WithFields(logrus.Fields{
		"operation":         operation,
		"LastKnownBackends": dm.LastKnownBackends,
	}).Trace("UpdateLastKnownBackends finished successfully")
	return nil
}

func (dm *DataManager) ReadAllNamespace() map[string]NamespaceData {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadAllNamespace"

	// Start the operation log
	logger.WithFields(logrus.Fields{
		"operation": operation,
	}).Trace("ReadAllNamespace data successfully")

	return dm.namespaces //returning copy
}

// Read retrieves data from a specific namespace
func (dm *DataManager) ReadStaticData() StaticConfig {
	dm.mu.RLock()         // Read lock to allow multiple concurrent reads
	defer dm.mu.RUnlock() // Ensure the lock is always released
	operation := "ReadStaticData"

	// Start the operation log
	logger.WithFields(logrus.Fields{
		"operation":  operation,
		"staticData": dm.StaticData,
	}).Trace("ReadStaticData successfully")

	return dm.StaticData //returning copy
}
