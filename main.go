package main

import (
	"context"
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:gui
var guiFS embed.FS

func main() {
	app := NewApp()

	// The tray is always available (open folder, fresh code, show window,
	// quit). Whether clicking the window's close button hides to the tray or
	// quits is decided at runtime in app.onBeforeClose.
	runTray(app)

	err := wails.Run(&options.App{
		Title:     "QuickDrop",
		Width:     960,
		Height:    640,
		MinWidth:  680,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: guiFS,
		},
		OnStartup:    app.startup,
		OnDomReady:   app.domReady,
		OnShutdown:   app.shutdown,
		OnBeforeClose: app.onBeforeClose,
		Bind:         []interface{}{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}

// onBeforeClose decides what a window-close / quit request does. Returning true
// cancels the close. This is invoked both when the user clicks the window's ✕
// and (indirectly, via runtime.Quit) when the tray menu asks to quit — hence
// the forceQuit flag to tell them apart.
func (a *App) onBeforeClose(ctx context.Context) bool {
	a.mu.Lock()
	force := a.forceQuit
	a.mu.Unlock()
	if force {
		// A deliberate tray "Quit" (or a real app shutdown) always closes.
		return false
	}
	if a.prefEnabled("closeToTray") {
		wailsruntime.WindowHide(ctx)
		return true // keep running in the tray
	}
	// Close to quit: let the window close and the app exit.
	return false
}

// forceQuit records that a deliberate app-quit request is in flight, so
// onBeforeClose lets it proceed instead of hiding to the tray.
func (a *App) forceQuitApp() {
	a.mu.Lock()
	a.forceQuit = true
	a.mu.Unlock()
}