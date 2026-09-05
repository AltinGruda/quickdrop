package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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
	prefs   map[string]bool

	history        *historyStore
	notifMu        sync.Mutex
	notifInitDone  bool
	notifAuthAsked bool
	notifPending   []string
	notifTimer     *time.Timer
	forceQuit      bool
}

// appConfig is the persisted settings file (destination folder plus feature
// toggles). It lives in the user's config directory.
type appConfig struct {
	DestDir         string `json:"destDir"`
	LaunchAtStartup bool   `json:"launchAtStartup"`
	Notifications   bool   `json:"notifications"`
	Sound           bool   `json:"sound"`
	CloseToTray     bool   `json:"closeToTray"`
}

// appConfigDefaults returns the first-run feature-toggle values. Used when no
// config file exists yet (fresh install).
func appConfigDefaults() appConfig {
	return appConfig{
		Notifications: true,
		Sound:         true,
		CloseToTray:   true,
	}
}

func NewApp() *App {
	return &App{history: newHistoryStore()}
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
	cfg := appConfigDefaults()
	b, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	// Unmarshal into a pointing version so we can tell a boolean that was
	// explicitly written as false apart from one that was never saved.
	var raw struct {
		DestDir         *string `json:"destDir"`
		LaunchAtStartup *bool   `json:"launchAtStartup"`
		Notifications   *bool   `json:"notifications"`
		Sound           *bool   `json:"sound"`
		CloseToTray     *bool   `json:"closeToTray"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return cfg
	}
	if raw.DestDir != nil {
		cfg.DestDir = *raw.DestDir
	}
	if raw.LaunchAtStartup != nil {
		cfg.LaunchAtStartup = *raw.LaunchAtStartup
	}
	if raw.Notifications != nil {
		cfg.Notifications = *raw.Notifications
	}
	if raw.Sound != nil {
		cfg.Sound = *raw.Sound
	}
	if raw.CloseToTray != nil {
		cfg.CloseToTray = *raw.CloseToTray
	}
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
	a.prefs = map[string]bool{
		"launchAtStartup": stored.LaunchAtStartup,
		"notifications":   stored.Notifications,
		"sound":           stored.Sound,
		"closeToTray":     stored.CloseToTray,
	}
	// Reconcile the persisted preference with the OS-level autostart
	// registration, so the two never drift apart after an upgrade or an
	// external change.
	reconcileLaunchAtStartup(stored.LaunchAtStartup)
	if err := a.startServer(); err != nil {
		wailsruntime.EventsEmit(ctx, "startup-error", plainErr(err))
	}
	// Ask for notification permission once if the feature is enabled.
	if a.prefEnabled("notifications") {
		a.requestNotificationPermission()
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

// onEvent bridges server-side events into GUI events, and drives the OS
// notifications + received-files history. It runs on server goroutines, so it
// must not block or panic.
func (a *App) onEvent(ev server.Event) {
	switch ev.Type {
	case server.EvFileDone:
		a.recordReceived(ev)
		if a.prefEnabled("notifications") {
			a.scheduleNotify(ev)
		}
	case server.EvFileError:
		if ev.Message != "" && a.prefEnabled("notifications") {
			a.TrayNotify("QuickDrop transfer", ev.Message)
		}
	}

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

// recordReceived persists a successfully received file into the history store.
func (a *App) recordReceived(ev server.Event) {
	dir := a.srvDestDir()
	if dir == "" {
		return
	}
	if ev.FinalName == "" {
		return
	}
	a.history.add(HistoryEntry{
		Name:      ev.Filename,
		Size:      ev.Bytes,
		FinalName: ev.FinalName,
		Path:      filepath.Join(dir, ev.FinalName),
	})
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "history-update")
	}
}

// srvDestDir returns the current session's destination folder (thread-safe).
func (a *App) srvDestDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return a.destDir
	}
	return a.srv.DestDir()
}

func (a *App) prefEnabled(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prefs[key]
}

// notifBatchDelay is how long completed transfers are collected before a
// single combined notification goes out. It turns a burst of N files landing
// at once into one notice instead of an N-deep toast stack.
const notifBatchDelay = 900 * time.Millisecond

// scheduleNotify queues a completed transfer for the next batched
// notification, coalescing files that finish within notifBatchDelay of each
// other. Flushing happens on a timer; bursts become a single "N files landed".
func (a *App) scheduleNotify(ev server.Event) {
	name := ev.FinalName
	if name == "" {
		name = ev.Filename
	}
	if name == "" {
		return
	}
	a.notifMu.Lock()
	a.notifPending = append(a.notifPending, name)
	if a.notifTimer == nil {
		a.notifTimer = time.AfterFunc(notifBatchDelay, a.flushNotify)
	}
	a.notifMu.Unlock()
}

// flushNotify sends one combined notification for everything queued since the
// last flush (or since the batch timer fired).
func (a *App) flushNotify() {
	a.notifMu.Lock()
	if a.notifTimer != nil {
		a.notifTimer.Stop()
		a.notifTimer = nil
	}
	files := a.notifPending
	a.notifPending = nil
	a.notifMu.Unlock()
	if len(files) == 0 {
		return
	}
	if !a.prefEnabled("notifications") {
		return
	}
	folder := "Downloads"
	if d := a.srvDestDir(); d != "" {
		if base := filepath.Base(d); base != "" && base != "." && base != string(filepath.Separator) {
			folder = base
		}
	}
	a.TrayNotify("QuickDrop", notifyBody(files, folder))
}

// notifyBody turns one-or-more completed transfers into a notification body,
// naming the destination folder they landed in.
func notifyBody(files []string, folder string) string {
	if len(files) == 1 {
		return files[0] + " landed in " + folder + "."
	}
	return fmt.Sprintf("%d files just landed in %s.", len(files), folder)
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
	cfg := appConfig{
		DestDir:         a.destDir,
		LaunchAtStartup: a.prefs["launchAtStartup"],
		Notifications:   a.prefs["notifications"],
		Sound:           a.prefs["sound"],
		CloseToTray:     a.prefs["closeToTray"],
	}
	a.mu.Unlock()
	saveAppConfig(cfg)
}

// Settings returns the current user-controlled preferences for the GUI.
func (a *App) Settings() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]any, len(a.prefs))
	for k, v := range a.prefs {
		out[k] = v
	}
	return out
}

// SetSetting flips a single named preference (launchAtStartup, notifications,
// sound, closeToTray), persists it, and returns the updated settings snapshot.
func (a *App) SetSetting(key string, value bool) map[string]any {
	switch key {
	case "launchAtStartup", "notifications", "sound", "closeToTray":
	default:
		return map[string]any{"error": "Unknown setting: " + key}
	}
	a.mu.Lock()
	a.prefs[key] = value
	a.mu.Unlock()
	a.persistConfig()
	a.applyPreferenceSideEffects(key, value)
	return a.Settings()
}

// applyPreferenceSideEffects performs any immediate OS-level action a setting
// change requires (e.g. registering/unregistering launch at login).
func (a *App) applyPreferenceSideEffects(key string, value bool) {
	if key == "launchAtStartup" {
		applyLaunchAtStartup(value)
	}
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

// History returns the persisted list of received files (most recent first),
// mapped for the GUI.
func (a *App) History() []map[string]any {
	rows := a.history.list()
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"name":      r.Name,
			"size":      r.Size,
			"finalName": r.FinalName,
			"path":      r.Path,
			"at":        r.At.Unix(),
			"shown":     shortPath(r.Path),
		})
	}
	return out
}

// OpenFile opens a previously-received file with the OS default application.
func (a *App) OpenFile(path string) {
	if path == "" {
		return
	}
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("explorer.exe", path).Start()
	case "darwin":
		_ = exec.Command("open", path).Start()
	default:
		_ = exec.Command("xdg-open", path).Start()
	}
}

// RevealPath reveals a previously-received file in the OS file manager, with
// the file selected where the platform supports it.
func (a *App) RevealPath(path string) {
	if path == "" {
		return
	}
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("explorer.exe", "/select,", path).Start()
	case "darwin":
		_ = exec.Command("open", "-R", path).Start()
	default:
		_ = exec.Command("xdg-open", filepath.Dir(path)).Start()
	}
}

// ClearHistory removes the persisted received-files history.
func (a *App) ClearHistory() map[string]bool {
	a.history.clear()
	return map[string]bool{"cleared": true}
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
