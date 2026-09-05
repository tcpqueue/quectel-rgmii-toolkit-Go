//go:build !linux

package main

func manageDeviceRoot() bool                  { return false }
func remountDeviceRoot(bool) error            { return nil }
func lockPersistentSettings() (func(), error) { return func() {}, nil }
func syncConfigDirectory(string) error        { return nil }
