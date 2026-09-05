package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// launchAtStartupValue returns the current value of the launch-at-login
// preference, defaulting to off for a fresh install.
func launchAtStartupValue(prefs map[string]bool) bool {
	return prefs["launchAtStartup"]
}

// applyLaunchAtStartup registers (or removes) the app in the OS autostart
// mechanism. Value true adds it; false removes it.
func applyLaunchAtStartup(enable bool) {
	switch runtime.GOOS {
	case "windows":
		applyLaunchAtStartupWindows(enable)
	case "darwin":
		applyLaunchAtStartupMac(enable)
	}
}

// reconcileLaunchAtStartup makes the OS-level autostart registration match the
// persisted preference on startup (covers upgrades and external edits).
func reconcileLaunchAtStartup(want bool) {
	if want == isLaunchAtStartupRegistered() {
		return
	}
	applyLaunchAtStartup(want)
}

// resolveExecutable returns the path of the running binary, falling back to the
// OS home directory / name if it cannot be determined.
func resolveExecutable() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "QuickDrop")
	}
	return "QuickDrop"
}
