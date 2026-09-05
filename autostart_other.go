//go:build !windows && !darwin

package main

func applyLaunchAtStartupWindows(enable bool) {}
func applyLaunchAtStartupMac(enable bool)     {}

func isLaunchAtStartupRegistered() bool {
	return false
}