package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const persistedStateSchemaVersion = 1
const persistedStateFileName = "datamanager-state.json"

type PersistedDataManagerState struct {
	Version    int                 `json:"version"`
	CapturedAt time.Time           `json:"captured_at"`
	Snapshot   DataManagerSnapshot `json:"snapshot"`
}

var dataManagerState struct {
	sync.RWMutex
	state *PersistedDataManagerState
}

func isStatePersistenceEnabled() bool {
	if config.StatePersistenceEnabled == nil {
		return true
	}
	return *config.StatePersistenceEnabled
}

func getStatePersistenceFilePath() string {
	return filepath.Join(config.StatePersistenceDir, persistedStateFileName)
}

func setDataManagerStateInMemory(state PersistedDataManagerState) {
	dataManagerState.Lock()
	defer dataManagerState.Unlock()
	cloned := state
	dataManagerState.state = &cloned
}

func rememberDataManagerState() {
	state := PersistedDataManagerState{
		Version:    persistedStateSchemaVersion,
		CapturedAt: time.Now().UTC(),
		Snapshot:   db.ExportSnapshot(),
	}
	setDataManagerStateInMemory(state)
	updateDataManagerStateHealth(false, "Using fresh DataManager data from controller")

	if !isStatePersistenceEnabled() {
		logger.Debug("Disk state persistence is disabled; stored datamanager state in memory only")
		return
	}

	if err := persistStateToDisk(state); err != nil {
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
			"path":  getStatePersistenceFilePath(),
		}).Warn("failed to persist datamanager state to disk")
		return
	}

	logger.WithFields(logrus.Fields{
		"path": getStatePersistenceFilePath(),
	}).Debug("Persisted datamanager state to disk")
}

func persistStateToDisk(state PersistedDataManagerState) error {
	if config.StatePersistenceDir == "" {
		return errors.New("state_persistence_dir is empty")
	}
	if err := os.MkdirAll(config.StatePersistenceDir, 0o755); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	stateFile := getStatePersistenceFilePath()
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, payload, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	return nil
}

func restoreDataManagerStateFromDisk() bool {
	return restoreDataManagerStateForNamespaces(nil)
}

func restoreDataManagerStateForUnavailableNamespaces() bool {
	namespaces := unavailableControllerNamespaces()
	if len(namespaces) == 0 {
		updateDataManagerStateHealth(false, "Using fresh DataManager data from controller")
		return false
	}
	return restoreDataManagerStateForNamespaces(namespaces)
}

func restoreDataManagerStateForNamespaces(namespaces map[string]bool) bool {
	if !isStatePersistenceEnabled() {
		logger.Info("Disk state persistence disabled via config")
		if namespaces == nil {
			updateDataManagerStateHealth(false, "Fresh DataManager data unavailable and disk persistence is disabled")
		} else {
			updateDataManagerStateHealth(false, "Fresh DataManager data unavailable for namespace(s) and disk persistence is disabled")
		}
		return false
	}

	state, err := readPersistedStateFromDisk()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.WithField("path", getStatePersistenceFilePath()).Info("No persisted state file found, continuing with live discovery")
			if namespaces == nil {
				updateDataManagerStateHealth(false, "Fresh DataManager data unavailable and no persisted state file found")
			} else {
				updateDataManagerStateHealth(false, "Fresh DataManager data unavailable for namespace(s) and no persisted state file found")
			}
			return false
		}
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
			"path":  getStatePersistenceFilePath(),
		}).Warn("Unable to load persisted state file")
		if namespaces == nil {
			updateDataManagerStateHealth(false, "Fresh DataManager data unavailable and persisted state could not be loaded")
		} else {
			updateDataManagerStateHealth(false, "Fresh DataManager data unavailable for namespace(s) and persisted state could not be loaded")
		}
		return false
	}

	restoredNamespaces, restoredNames := db.ImportSnapshotForNamespacesDetailed(state.Snapshot, namespaces)
	if restoredNamespaces == 0 {
		if namespaces == nil {
			updateDataManagerStateHealth(false, "Persisted DataManager state found, but no matching namespace data to restore")
		} else {
			updateDataManagerStateHealth(false, "No stale DataManager state available for unavailable namespace(s)")
		}
		return false
	}
	setDataManagerStateInMemory(*state)
	logger.WithFields(logrus.Fields{
		"path":                getStatePersistenceFilePath(),
		"captured_at":         state.CapturedAt,
		"restored_namespaces": restoredNamespaces,
		"namespaces":          restoredNames,
	}).Info("Loaded datamanager state from disk")
	if namespaces == nil {
		updateDataManagerStateHealth(true, "Using stale DataManager state from persisted snapshot")
	} else {
		updateDataManagerStateHealth(true, "Using stale DataManager state for unavailable namespace(s): "+strings.Join(restoredNames, ","))
	}
	return true
}

func applyDataManagerStateForOfflineReconcile() bool {
	namespaces := unavailableControllerNamespaces()
	if len(namespaces) == 0 {
		updateDataManagerStateHealth(false, "Using fresh DataManager data from controller")
		return false
	}

	state, source := getDataManagerState()
	if state == nil {
		logger.Warn("Controller endpoints are unreachable and no datamanager state snapshot is available")
		updateDataManagerStateHealth(false, "Controllers unreachable for namespace(s) and no stale DataManager state is available")
		return false
	}

	restoredNamespaces, restoredNames := db.ImportSnapshotForNamespacesDetailed(state.Snapshot, namespaces)
	if restoredNamespaces == 0 {
		updateDataManagerStateHealth(false, "No stale DataManager state available for unavailable namespace(s)")
		return false
	}
	logger.WithFields(logrus.Fields{
		"source":              source,
		"captured_at":         state.CapturedAt,
		"restored_namespaces": restoredNamespaces,
		"namespaces":          restoredNames,
	}).Warn("Controller endpoints are unreachable for some namespaces; applying stale datamanager state before reconcile")
	updateDataManagerStateHealth(true, "Using stale DataManager state for unavailable namespace(s): "+strings.Join(restoredNames, ","))
	return true
}

func unavailableControllerNamespaces() map[string]bool {
	health.RLock()
	defer health.RUnlock()
	namespaces := make(map[string]bool)
	for namespace, endpoints := range health.NamespaceEndpoints {
		healthy := false
		for _, endpoint := range endpoints {
			if endpoint.Healthy {
				healthy = true
				break
			}
		}
		if !healthy {
			namespaces[namespace] = true
		}
	}
	return namespaces
}

func updateDataManagerStateHealth(usingStaleDroveState bool, message string) {
	health.Lock()
	previousStale := health.DataManagerState.UsingStaleDroveState
	previousMessage := health.DataManagerState.Message
	health.DataManagerState.UsingStaleDroveState = usingStaleDroveState
	health.DataManagerState.Message = message
	health.DataManagerState.LastUpdated = time.Now().UTC()
	health.Unlock()

	if previousStale == usingStaleDroveState && previousMessage == message {
		return
	}

	fields := logrus.Fields{
		"using_stale_drove_state": usingStaleDroveState,
		"message":                 message,
	}
	if usingStaleDroveState {
		logger.WithFields(fields).Warn("DataManager state source switched to stale persisted data")
		return
	}
	logger.WithFields(fields).Info("DataManager state source switched to fresh controller data")
}

func getDataManagerState() (*PersistedDataManagerState, string) {
	dataManagerState.RLock()
	if dataManagerState.state != nil {
		state := *dataManagerState.state
		dataManagerState.RUnlock()
		return &state, "memory"
	}
	dataManagerState.RUnlock()

	if !isStatePersistenceEnabled() {
		return nil, "none"
	}

	state, err := readPersistedStateFromDisk()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.WithFields(logrus.Fields{
				"error": err.Error(),
				"path":  getStatePersistenceFilePath(),
			}).Warn("Unable to load persisted datamanager state for offline reconcile")
		}
		return nil, "none"
	}

	setDataManagerStateInMemory(*state)
	return state, "disk"
}

func readPersistedStateFromDisk() (*PersistedDataManagerState, error) {
	stateFile := getStatePersistenceFilePath()
	payload, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}

	state := PersistedDataManagerState{}
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, err
	}
	if state.Version != persistedStateSchemaVersion {
		return nil, errors.New("unsupported persisted state schema version")
	}
	return &state, nil
}
