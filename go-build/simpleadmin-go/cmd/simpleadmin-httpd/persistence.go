package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type persistentFile struct {
	path string
	data []byte
	mode os.FileMode
}
type persistenceStore struct {
	mu      sync.Mutex
	managed func() bool
	remount func(bool) error
	lock    func() (func(), error)
}

var persistentSettings = persistenceStore{managed: manageDeviceRoot, remount: remountDeviceRoot, lock: lockPersistentSettings}
var persistenceMock bool

// A post-rename failure must not leave callers using superseded credentials/config.
type persistenceCommittedError struct{ err error }

func (e *persistenceCommittedError) Error() string {
	return "settings written but durability/read-only restoration failed: " + e.err.Error()
}
func (e *persistenceCommittedError) Unwrap() error { return e.err }
func persistenceCommitted(err error) bool {
	var e *persistenceCommittedError
	return errors.As(err, &e)
}

func writePersistentFile(path string, data []byte, mode os.FileMode) error {
	return persistentSettings.write(persistentFile{path, data, mode})
}

func (s *persistenceStore) write(files ...persistentFile) (result error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	managed := s.managed()
	if managed {
		unlock, err := s.lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	changed := make([]persistentFile, 0, len(files))
	for _, file := range files {
		old, err := os.ReadFile(file.path)
		if err == nil && bytes.Equal(old, file.data) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		changed = append(changed, file)
	}
	if len(changed) == 0 {
		return nil
	}
	return s.mutate(managed, func() (bool, error) {
		committed := false
		for _, file := range changed {
			renamed, err := replacePersistentFile(file)
			committed = committed || renamed
			if err != nil {
				return committed, fmt.Errorf("save %s: %w", filepath.Base(file.path), err)
			}
		}
		return committed, nil
	})
}

// Caller holds both configuration locks while mutating files or invoking passwd.
func (s *persistenceStore) mutate(managed bool, action func() (bool, error)) (result error) {
	committed := false
	defer func() {
		if result != nil && committed {
			result = &persistenceCommittedError{result}
		}
	}()
	if managed {
		// Always try to restore ro, even if remount,rw reports an error.
		defer func() { result = errors.Join(result, s.remount(false)) }()
		if err := s.remount(true); err != nil {
			return err
		}
	}
	committed, result = action()
	return result
}

func replacePersistentFile(file persistentFile) (bool, error) {
	dir := filepath.Dir(file.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".simpleadmin-config-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(file.mode); err == nil {
		_, err = tmp.Write(file.data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := os.Rename(tmp.Name(), file.path); err != nil {
		return false, err
	}
	return true, syncConfigDirectory(dir)
}
