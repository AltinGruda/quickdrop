package main

import (
	"embed"
	"runtime"
)

//go:embed build/appicon.png build/windows/icon.ico
var iconFS embed.FS

// trayIcon returns the app icon bytes for the system tray. Windows needs a
// real .ico (LoadImageW doesn't decode PNG); other platforms get the PNG.
func trayIcon() []byte {
	name := "build/appicon.png"
	if runtime.GOOS == "windows" {
		name = "build/windows/icon.ico"
	}
	b, err := iconFS.ReadFile(name)
	if err != nil {
		return nil
	}
	return b
}