package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"unsafe"
)

func testPersistenceStore(events *[]string) *persistenceStore {
	return &persistenceStore{
		managed: func() bool { return true },
		lock: func() (func(), error) {
			*events = append(*events, "lock")
			return func() { *events = append(*events, "unlock") }, nil
		},
		remount: func(rw bool) error {
			if rw {
				*events = append(*events, "rw")
			} else {
				*events = append(*events, "ro")
			}
			return nil
		},
	}
}
func TestPersistenceOrderAndUnchangedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	var events []string
	s := testPersistenceStore(&events)
	if err := s.write(persistentFile{path, []byte("value"), 0600}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"lock", "rw", "ro", "unlock"}) {
		t.Fatal(events)
	}
	before, _ := os.Stat(path)
	events = nil
	if err := s.write(persistentFile{path, []byte("value"), 0600}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged configuration rewritten")
	}
	if !reflect.DeepEqual(events, []string{"lock", "unlock"}) {
		t.Fatal(events)
	}
	if before.Mode().Perm() != 0600 {
		t.Fatal(before.Mode())
	}
}
func TestPersistenceRestoresROOnFailure(t *testing.T) {
	for _, failure := range []string{"rw", "write", "ro"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			var events []string
			s := testPersistenceStore(&events)
			remount := s.remount
			s.remount = func(rw bool) error {
				_ = remount(rw)
				if failure == "rw" && rw || failure == "ro" && !rw {
					return errors.New("mount failure")
				}
				return nil
			}
			if failure == "write" {
				blocker := filepath.Join(dir, "blocker")
				s.remount = func(rw bool) error {
					_ = remount(rw)
					if rw {
						return os.Mkdir(blocker, 0700)
					}
					return nil
				}
				// The second file fails after the first rename, while rw is held.
				err := s.write(persistentFile{path, []byte("new"), 0600}, persistentFile{blocker, []byte("x"), 0600})
				if !persistenceCommitted(err) {
					t.Fatal("expected partial commit error", err)
				}
				if !reflect.DeepEqual(events, []string{"lock", "rw", "ro", "unlock"}) {
					t.Fatal(events)
				}
				leftovers, _ := filepath.Glob(filepath.Join(dir, ".simpleadmin-config-*"))
				if len(leftovers) != 0 {
					t.Fatal("temporary files retained", leftovers)
				}
				return
			}
			err := s.write(persistentFile{path, []byte("new"), 0600})
			if err == nil {
				t.Fatal("expected error")
			}
			if !reflect.DeepEqual(events, []string{"lock", "rw", "ro", "unlock"}) {
				t.Fatal(events)
			}
			if failure == "rw" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("wrote despite rw failure")
				}
			}
			if persistenceCommitted(err) != (failure == "ro") {
				t.Fatal("wrong commit state", err)
			}
		})
	}
}

func TestCommittedSettingsRemainConsistentOnROFailure(t *testing.T) {
	oldManaged, oldRemount, oldLock := persistentSettings.managed, persistentSettings.remount, persistentSettings.lock
	defer func() {
		persistentSettings.managed, persistentSettings.remount, persistentSettings.lock = oldManaged, oldRemount, oldLock
	}()
	persistentSettings.managed = func() bool { return true }
	persistentSettings.lock = func() (func(), error) { return func() {}, nil }
	persistentSettings.remount = func(rw bool) error {
		if !rw {
			return errors.New("ro failure")
		}
		return nil
	}
	s := securityServer(t)
	cookie := securitySession(t, s)
	w := securityRequest(s, cookie, "POST", "/api/set_password", "current_password=admin&new_password=changed123&confirm_password=changed123")
	if w.Code != 500 || s.validateSession(cookie.Value) {
		t.Fatal("committed password retained session", w.Code)
	}
	auth, err := loadAuthConfig(s.cfg.authFile)
	if err != nil || auth.Password != "changed123" {
		t.Fatal(auth, err)
	}
	m := newTelemetryMonitor(filepath.Join(t.TempDir(), "monitor.json"), true)
	if err := m.setTarget("example.com"); !persistenceCommitted(err) {
		t.Fatal(err)
	}
	if m.target != "example.com" || m.generation != 1 {
		t.Fatal("committed target not applied")
	}
}
func TestPersistenceConcurrentSavesAreSerialized(t *testing.T) {
	var events []string
	s := testPersistenceStore(&events)
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(value byte) {
			defer wg.Done()
			if err := s.write(persistentFile{filepath.Join(dir, "config"), []byte{value}, 0600}); err != nil {
				t.Error(err)
			}
		}(byte(i))
	}
	wg.Wait()
	for i := 0; i < len(events); i += 4 {
		if !reflect.DeepEqual(events[i:i+4], []string{"lock", "rw", "ro", "unlock"}) {
			t.Fatal(events)
		}
	}
}
func TestCompactTelemetryBudgetAndRoundTrip(t *testing.T) {
	bytes := 300*unsafe.Sizeof(compactPing{}) + 60*unsafe.Sizeof(compactSignal{})
	if bytes > 6240 {
		t.Fatalf("history buffers use %d bytes", bytes)
	}
	t.Logf("fixed history buffers: %d bytes", bytes)
	for _, value := range []*float64{nil, metric(-98.4), metric(22.1), metric(900)} {
		expanded := expandMetric(compactMetric(value))
		if value == nil {
			if expanded != nil {
				t.Fatal("missing value lost")
			}
		} else if expanded == nil || *expanded != *value {
			t.Fatal("precision lost")
		}
	}
	h := pingHistory{historyRing: historyRing[compactPing]{values: make([]compactPing, 300)}}
	sample := pingSample{Time: 1, RTT: metric(10), Status: "ok", Sent: true, IP: "1.1.1.1"}
	if allocations := testing.AllocsPerRun(1000, func() { h.add(sample) }); allocations != 0 {
		t.Fatalf("history insertion allocates %v objects", allocations)
	}
	points := h.snapshot()
	if points[len(points)-1].IP != "1.1.1.1" || points[0].IP != "" {
		t.Fatal("address must be kept only once")
	}
}
