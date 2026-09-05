//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

func rootShadowHash() (string, error) {
	data, err := os.ReadFile("/etc/shadow")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 2 && fields[0] == "root" {
			return fields[1], nil
		}
	}
	return "", errors.New("root account unavailable")
}

func verifyRootHash(password, hash string) bool {
	if password == "" || len(password) > 128 || strings.ContainsAny(password, "\r\n\x00") {
		return false
	}
	fields := strings.Split(hash, "$")
	if len(fields) < 4 || fields[0] != "" {
		return false
	}
	algorithm := fields[1]
	if algorithm != "1" && algorithm != "5" && algorithm != "6" {
		return false
	}
	salt := strings.Join(fields[2:len(fields)-1], "$")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "openssl", "passwd", "-"+algorithm, "-salt", salt, "-stdin")
	cmd.Stdin = strings.NewReader(password + "\n")
	output, err := cmd.Output()
	return err == nil && constantTimeEqual(strings.TrimSpace(string(output)), hash)
}

func verifySystemRootPassword(password string) bool {
	hash, err := rootShadowHash()
	return err == nil && verifyRootHash(password, hash)
}

func changeSystemRootPassword(current, next string, initialize bool) error {
	if err := validateNewPassword(next); err != nil {
		return err
	}
	if strings.ContainsRune(next, 0) {
		return errors.New("invalid password")
	}
	s := &persistentSettings
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
	if !initialize && !verifySystemRootPassword(current) {
		return errors.New("current root password changed")
	}
	if verifySystemRootPassword(next) {
		return nil
	}
	return s.mutate(managed, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "passwd", "-a", "sha512", "root")
		cmd.Stdin = strings.NewReader(next + "\n" + next + "\n")
		if err := cmd.Run(); err != nil {
			return verifySystemRootPassword(next), errors.New("system passwd failed")
		}
		if !verifySystemRootPassword(next) {
			return true, errors.New("system password verification failed")
		}
		f, err := os.Open("/etc/shadow")
		if err != nil {
			return true, err
		}
		err = f.Sync()
		_ = f.Close()
		return true, errors.Join(err, syncConfigDirectory("/etc"))
	})
}
