//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// autostartKey is the HKCU Run key where Windows launches programs at logon.
const autostartKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func applyLaunchAtStartupWindows(enable bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()

	if enable {
		_ = k.SetStringValue("QuickDrop", `"`+resolveExecutable()+`"`)
	} else {
		_ = k.DeleteValue("QuickDrop")
	}
}

// applyLaunchAtStartupMac is a no-op on Windows (Apple LaunchAgents don't exist
// here); it exists so the shared autostart.go always has both symbols defined.
func applyLaunchAtStartupMac(enable bool) {}

// isLaunchAtStartupRegistered reports whether QuickDrop is currently present in
// the Windows autostart Run key.
func isLaunchAtStartupRegistered() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue("QuickDrop")
	return err == nil
}