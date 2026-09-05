//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

// macPlistPath returns ~/Library/LaunchAgents/com.quickdrop.plist, the real
// LaunchAgents directory launchd watches on macOS.
func macPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.quickdrop.plist"), nil
}

func applyLaunchAtStartupMac(enable bool) {
	p, err := macPlistPath()
	if err != nil {
		return
	}
	if enable {
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.quickdrop</string>
    <key>ProgramArguments</key>
    <array>
        <string>` + resolveExecutable() + `</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
`
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(plist), 0o644)
		return
	}
	_ = os.Remove(p)
}

// applyLaunchAtStartupWindows is a no-op on macOS; it exists so the shared
// autostart.go always has both symbols defined.
func applyLaunchAtStartupWindows(enable bool) {}

// isLaunchAtStartupRegistered reports whether the macOS LaunchAgent plist
// currently exists on disk.
func isLaunchAtStartupRegistered() bool {
	p, err := macPlistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}