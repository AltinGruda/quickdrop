package main

import (
	"runtime"

	"fyne.io/systray"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// trayApp holds the shared state between the systray goroutine and the Wails
// app. It exists so the tray can stay alive even when the window is hidden.
type trayController struct {
	app *App
}

// runTray starts the system tray in a dedicated OS-thread-locked goroutine.
// It stays running for the whole app life; quitting is driven from the menu.
func runTray(app *App) {
	if app == nil {
		return
	}
	tc := &trayController{app: app}
	go func() {
		// On Windows the systray message loop must own a stable OS thread.
		runtime.LockOSThread()
		systray.Run(func() { tc.ready() }, func() { tc.exit() })
	}()
}

// ready builds the tray icon and menu. It runs on the systray goroutine.
func (tc *trayController) ready() {
	icon := trayIcon()
	if icon != nil {
		systray.SetIcon(icon)
	}
	systray.SetTitle("QuickDrop")
	systray.SetTooltip("QuickDrop — scan to send files")

	// On Windows a tray icon opens its context menu on any click; rebind the
	// primary (left) click to raise the app window and keep right-click as
	// the menu. macOS keeps the convention of clicking the icon opening the
	// menu bar menu.
	if runtime.GOOS == "windows" {
		systray.SetOnTapped(tc.show)
	}

	mShow := systray.AddMenuItem("Open QuickDrop", "Show the QuickDrop window")
	mOpen := systray.AddMenuItem("Open received folder", "Open the folder files arrive in")
	mNew := systray.AddMenuItem("Start a fresh code", "Invalidate the current code and show a new one")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit QuickDrop", "Close QuickDrop entirely")

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				tc.show()
			case <-mOpen.ClickedCh:
				tc.app.OpenFolder()
			case <-mNew.ClickedCh:
				tc.app.NewSession()
			case <-mQuit.ClickedCh:
				tc.quit()
				return
			}
		}
	}()
}

func (tc *trayController) show() {
	if tc.app.ctx == nil {
		return
	}
	if wailsruntime.WindowIsMinimised(tc.app.ctx) {
		wailsruntime.WindowUnminimise(tc.app.ctx)
	}
	wailsruntime.WindowShow(tc.app.ctx)
}

func (tc *trayController) quit() {
	tc.app.forceQuitApp()
	if tc.app.ctx != nil {
		wailsruntime.Quit(tc.app.ctx)
	}
	systray.Quit()
}

func (tc *trayController) exit() {}