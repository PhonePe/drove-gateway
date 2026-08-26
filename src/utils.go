package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
)

// writeFileAtomic writes content to a temp file in the same directory and renames it into place.
// The temp file is removed on error paths.
func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}

	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := tmpFile.Chmod(mode); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(content); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file %q to %q: %w", tmpPath, path, err)
	}

	return nil
}

func fileModeFromExistingOrDefault(path string, defaultMode os.FileMode) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().Perm(), nil
	}
	if os.IsNotExist(err) {
		return defaultMode, nil
	}
	return 0, err
}

const persistedStateSchemaVersion = 1
const persistedStateFileName = "datamanager-state.json"

var errDiskIOTimeout = errors.New("disk io timeout")

type PersistedDataManagerState struct {
	Version    int                 `json:"version"`
	CapturedAt time.Time           `json:"captured_at"`
	Snapshot   DataManagerSnapshot `json:"snapshot"`
}

var dataManagerState struct {
	sync.RWMutex
	state *PersistedDataManagerState
}

var persistStateToDiskInFlight int32

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

func diskIOTimeout() time.Duration {
	return time.Duration(config.DiskIOTimeoutSec) * time.Second
}

func updateDiskIOHealth(healthy bool, message string) {
	updateHealthSection("DiskIO", healthy, message)
}

func rememberDataManagerState() {
	state := PersistedDataManagerState{
		Version:    persistedStateSchemaVersion,
		CapturedAt: time.Now().UTC(),
		Snapshot:   db.ExportSnapshot(),
	}
	setDataManagerStateInMemory(state)
	updateDataManagerStateHealth(true, "Using fresh DataManager data from controller")

	if !isStatePersistenceEnabled() {
		logger.Debug("Disk state persistence is disabled; stored datamanager state in memory only")
		return
	}

	persistStateToDisk(state)
	logger.WithFields(logrus.Fields{
		"path": getStatePersistenceFilePath(),
	}).Debug("Scheduled async datamanager state persistence to disk")
}

func persistStateToDisk(state PersistedDataManagerState) {
	if !atomic.CompareAndSwapInt32(&persistStateToDiskInFlight, 0, 1) {
		return
	}

	go func(snapshot PersistedDataManagerState) {
		timeout := diskIOTimeout()
		errCh := make(chan error, 1)
		go func() {
			errCh <- persistStateToDiskSync(snapshot)
		}()

		select {
		case err := <-errCh:
			atomic.StoreInt32(&persistStateToDiskInFlight, 0)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error": err.Error(),
					"path":  getStatePersistenceFilePath(),
				}).Warn("Failed to persist datamanager state to disk")
				updateDiskIOHealth(false, "Disk write failed for datamanager state")
				return
			}
			updateDiskIOHealth(true, "Disk read/write healthy")
			return
		case <-time.After(timeout):
			logger.WithFields(logrus.Fields{
				"timeout": timeout,
				"path":    getStatePersistenceFilePath(),
			}).Warn("Timed out waiting for datamanager state persistence")
			updateDiskIOHealth(false, "Disk write timed out for datamanager state")
			go func() {
				_ = <-errCh
				atomic.StoreInt32(&persistStateToDiskInFlight, 0)
			}()
			return
		}
	}(state)
}

func persistStateToDiskSync(state PersistedDataManagerState) error {
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
	fileMode, modeErr := fileModeFromExistingOrDefault(stateFile, 0o644)
	if modeErr != nil {
		return modeErr
	}
	if err := writeFileAtomic(stateFile, payload, fileMode); err != nil {
		return err
	}
	return nil
}

func restoreDataManagerStateForNamespaces(namespaces map[string]bool) bool {
	if !isStatePersistenceEnabled() {
		logger.Info("Disk state persistence disabled via config")
		updateDataManagerStateHealth(true, "Fresh DataManager data unavailable for namespace(s) and disk persistence is disabled")
		return false
	}

	state, err := readPersistedStateFromDisk()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.WithField("path", getStatePersistenceFilePath()).Info("No persisted state file found, continuing with live discovery")
			updateDataManagerStateHealth(true, "Fresh DataManager data unavailable for namespace(s) and no persisted state file found")
			return false
		}
		logger.WithFields(logrus.Fields{
			"error": err.Error(),
			"path":  getStatePersistenceFilePath(),
		}).Warn("Unable to load persisted state file")
		updateDataManagerStateHealth(true, "Fresh DataManager data unavailable for namespace(s) and persisted state could not be loaded")
		return false
	}

	restoredNames := db.ImportSnapshotForNamespaces(state.Snapshot, namespaces)
	if len(restoredNames) == 0 {
		updateDataManagerStateHealth(true, "No stale DataManager state available for unavailable namespace(s)")
		return false
	}
	setDataManagerStateInMemory(*state)
	logger.WithFields(logrus.Fields{
		"path":                getStatePersistenceFilePath(),
		"captured_at":         state.CapturedAt,
		"restored_namespaces": len(restoredNames),
		"namespaces":          restoredNames,
	}).Info("Loaded datamanager state from disk")
	updateDataManagerStateHealth(false, "Using stale DataManager state for unavailable namespace(s): "+strings.Join(restoredNames, ","))
	return true
}

func applyDataManagerStateForOfflineReconcile() bool {
	namespaces := unavailableControllerNamespaces()
	if len(namespaces) == 0 {
		updateDataManagerStateHealth(true, "Using fresh DataManager data from controller")
		return false
	}

	state, source := getDataManagerState()
	if state == nil {
		logger.Warn("Controller endpoints are unreachable and no datamanager state snapshot is available")
		updateDataManagerStateHealth(true, "Controllers unreachable for namespace(s) and no stale DataManager state is available")
		return false
	}

	restoredNamespaces := db.ImportSnapshotForNamespaces(state.Snapshot, namespaces)
	restoredNamespacesCount := len(restoredNamespaces)
	if restoredNamespacesCount == 0 {
		updateDataManagerStateHealth(true, "No stale DataManager state available for unavailable namespace(s)")
		return false
	}
	logger.WithFields(logrus.Fields{
		"source":              source,
		"captured_at":         state.CapturedAt,
		"restored_namespaces": restoredNamespacesCount,
		"namespaces":          restoredNamespaces,
	}).Warn("Controller endpoints are unreachable for some namespaces; applying stale datamanager state before reconcile")
	updateDataManagerStateHealth(false, "Using stale DataManager state for unavailable namespace(s): "+strings.Join(restoredNamespaces, ","))
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

func updateDataManagerStateHealth(usingFreshDroveState bool, message string) {
	health.Lock()
	previousFresh := health.DataManagerState.UsingFreshDroveState
	previousMessage := health.DataManagerState.Message
	health.DataManagerState.UsingFreshDroveState = usingFreshDroveState
	health.DataManagerState.Message = message
	health.DataManagerState.LastUpdated = time.Now().UTC()
	health.Unlock()

	if previousFresh == usingFreshDroveState && previousMessage == message {
		return
	}

	fields := logrus.Fields{
		"using_fresh_drove_state": usingFreshDroveState,
		"message":                 message,
	}
	if !usingFreshDroveState {
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
	timeout := diskIOTimeout()
	type readResult struct {
		payload []byte
		err     error
	}
	readCh := make(chan readResult, 1)
	go func() {
		payload, err := os.ReadFile(stateFile)
		readCh <- readResult{payload: payload, err: err}
	}()

	var payload []byte
	var err error
	select {
	case result := <-readCh:
		payload = result.payload
		err = result.err
	case <-time.After(timeout):
		updateDiskIOHealth(false, "Disk read timed out for datamanager state")
		return nil, fmt.Errorf("%w while reading %s after %s", errDiskIOTimeout, stateFile, timeout)
	}

	if err != nil {
		if !errors.Is(err, errDiskIOTimeout) {
			updateDiskIOHealth(true, "Disk read/write healthy")
		}
		return nil, err
	}

	state := PersistedDataManagerState{}
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, err
	}
	if state.Version != persistedStateSchemaVersion {
		return nil, errors.New("unsupported persisted state schema version")
	}
	updateDiskIOHealth(true, "Disk read/write healthy")
	return &state, nil
}
