package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"quickdrop/server"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails-bound application root. Its exported methods are callable
// from the GUI's JavaScript via window.go.main.App.
type App struct {
	ctx     context.Context
	mu      sync.Mutex
	srv     *server.Server
	destDir string
}

// appConfig is the small persisted settings file (destination folder). It
// lives in the user's config directory.
type appConfig struct {
	DestDir string `json:"destDir"`
}

func NewApp() *App {
	return &App{}
}

// configPath returns the user config file, e.g.
// %APPDATA%\QuickDrop\config.json on Windows.
func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "QuickDrop", "config.json")
}

func loadAppConfig() appConfig {
	var cfg appConfig
	b, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg
}

func saveAppConfig(cfg appConfig) {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o600)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	stored := loadAppConfig()
	a.destDir = stored.DestDir
	if a.destDir == "" {
		a.destDir = server.DefaultDestDir()
	}
	if err := a.startServer(); err != nil {
		wailsruntime.EventsEmit(ctx, "startup-error", plainErr(err))
	}
}

func (a *App) domReady(ctx context.Context) {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		wailsruntime.EventsEmit(ctx, "startup-error", "QuickDrop couldn\u2019t start. Close it and open it again.")
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	srv := a.srv
	a.srv = nil
	a.mu.Unlock()
	if srv != nil {
		_ = srv.Shutdown()
	}
}

// startServer tears down any existing session and starts a fresh one. Returning
// an error triggers the plain-English startup error in the GUI.
func (a *App) startServer() error {
	a.mu.Lock()
	old := a.srv
	a.srv = nil
	a.mu.Unlock()
	if old != nil {
		_ = old.Shutdown()
	}

	cfg := server.Config{DestDir: a.destDir, OnEvent: a.onEvent}
	srv, err := server.Start(cfg)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.srv = srv
	a.mu.Unlock()
	return nil
}

// onEvent bridges server-side events into GUI events.
func (a *App) onEvent(ev server.Event) {
	if a.ctx == nil {
		return
	}
	if ev.Type == server.EvClientIsolation {
		wailsruntime.EventsEmit(a.ctx, "isolation", ev.Message)
		return
	}
	payload := map[string]any{
		"type":     string(ev.Type),
		"uploadId": ev.UploadID,
		"filename": ev.Filename,
		"finalName": ev.FinalName,
		"bytes":    ev.Bytes,
		"total":    ev.Total,
		"stage":    ev.Stage,
		"message":  ev.Message,
	}
	wailsruntime.EventsEmit(a.ctx, "upload-progress", payload)
}

// info snapshots the current session for the GUI.
func (a *App) info() map[string]any {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return map[string]any{"state": "error", "error": "QuickDrop isn\u2019t running."}
	}
	return map[string]any{
		"state":     "ready",
		"url":       srv.URL(),
		"token":     srv.Token(),
		"port":      srv.Port(),
		"lanIP":     srv.PrimaryIP(),
		"altUrls":   srv.AltURLs(),
		"destDir":   srv.DestDir(),
		"destShown": shortPath(srv.DestDir()),
	}
}

// SessionInfo returns the current session details for the GUI.
func (a *App) SessionInfo() map[string]any { return a.info() }

// NewSession invalidates the current token/session (and tears down its uploads)
// then starts a fresh one with a new ready-to-scan QR code.
func (a *App) NewSession() map[string]any {
	if err := a.startServer(); err != nil {
		return map[string]any{"state": "error", "error": plainErr(err)}
	}
	info := a.info()
	wailsruntime.EventsEmit(a.ctx, "session-info", info)
	return info
}

// SetDestDir changes where files land and restarts the session immediately.
func (a *App) SetDestDir(dir string) map[string]any {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return map[string]any{"state": "error", "error": "Type a folder path first."}
	}
	a.mu.Lock()
	a.destDir = dir
	a.mu.Unlock()
	a.persistConfig()
	if err := a.startServer(); err != nil {
		return map[string]any{"state": "error", "error": plainErr(err)}
	}
	info := a.info()
	wailsruntime.EventsEmit(a.ctx, "session-info", info)
	return info
}

// persistConfig writes the current settings to disk.
func (a *App) persistConfig() {
	a.mu.Lock()
	cfg := appConfig{DestDir: a.destDir}
	a.mu.Unlock()
	saveAppConfig(cfg)
}

// PickDestDir opens the native OS folder picker. Choosing a folder switches to
// it immediately and returns the refreshed session info; cancelling returns a
// "cancelled" state and leaves the session untouched.
func (a *App) PickDestDir() map[string]any {
	a.mu.Lock()
	def := ""
	if srv := a.srv; srv != nil {
		def = srv.DestDir()
	}
	a.mu.Unlock()
	dir, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:            "Choose a folder for received files",
		DefaultDirectory: def,
	})
	if err != nil || dir == "" {
		return map[string]any{"state": "cancelled"}
	}
	return a.SetDestDir(dir)
}

// QRDataURL returns the current session's QR code as a PNG data URL.
func (a *App) QRDataURL() string {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return ""
	}
	png, err := srv.QRPNG(640)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// FilesReceived lists files that completed successfully this session.
func (a *App) FilesReceived() []string {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.FilesOnDisk()
}

// OpenFolder reveals the destination folder in Explorer/Finder.
func (a *App) OpenFolder() {
	a.mu.Lock()
	srv := a.srv
	a.mu.Unlock()
	if srv == nil {
		return
	}
	dir := srv.DestDir()
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("explorer.exe", dir).Start()
	case "darwin":
		_ = exec.Command("open", dir).Start()
	default:
		_ = exec.Command("xdg-open", dir).Start()
	}
}

// plainErr converts a startup/server error into one plain-English sentence.
func plainErr(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, server.ErrNoNetwork):
		return "No Wi-Fi connection found — connect to Wi-Fi and reopen the app."
	case errors.Is(err, server.ErrDestUnavailable):
		return "Couldn\u2019t use the save folder — pick a different one and try again."
	default:
		return "QuickDrop couldn\u2019t start. Close it and open it again."
	}
}

// shortPath renders a destination path without the user's home directory prefix.
func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	sep := string(filepath.Separator)
	prefix := home + sep
	if rest, ok := strings.CutPrefix(p, prefix); ok {
		return "~" + sep + rest
	}
	return p
}
