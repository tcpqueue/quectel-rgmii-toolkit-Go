//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func manageDeviceRoot() bool {
	if persistenceMock {
		return false
	}
	if _, explicit := os.LookupEnv("SIMPLEADMIN_MANAGE_ROOTFS"); explicit {
		return envBool("SIMPLEADMIN_MANAGE_ROOTFS")
	}
	info, err := os.Stat(defaultATDevice)
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func remountDeviceRoot(writable bool) error {
	mode := "ro"
	if writable {
		mode = "rw"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mount", "-o", "remount,"+mode, "/").CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore root mount %s: %w: %s", mode, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func lockPersistentSettings() (func(), error) {
	// The inter-process lock must never be created on NAND.
	for _, dir := range []string{"/run", "/tmp"} {
		var stat syscall.Statfs_t
		if syscall.Statfs(dir, &stat) != nil || uint64(stat.Type) != 0x01021994 {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, "simpleadmin-config.lock"), os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC, 0600)
		if err != nil {
			return nil, err
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
			}
			if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
				_ = f.Close()
				return nil, fmt.Errorf("configuration lock: %w", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("configuration lock requires tmpfs at /run or /tmp")
}

func syncConfigDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
