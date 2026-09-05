//go:build !linux

package main

import "errors"

func verifySystemRootPassword(string) bool { return false }
func changeSystemRootPassword(string, string, bool) error {
	return errors.New("system root password requires Linux")
}
