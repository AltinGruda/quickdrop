package main

import (
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// TrayNotify sends a native OS notification. On platforms without a usable
// notification service (or when initialization failed) it degrades to no-op.
func (a *App) TrayNotify(title, body string) {
	if a.ctx == nil {
		return
	}
	a.notifMu.Lock()
	if !a.notifInitDone {
		if err := wailsruntime.InitializeNotifications(a.ctx); err != nil {
			a.notifMu.Unlock()
			return
		}
		a.notifInitDone = true
	}
	a.notifMu.Unlock()
	_ = wailsruntime.SendNotification(a.ctx, wailsruntime.NotificationOptions{
		Title: title,
		Body:  body,
	})
}

// requestNotificationPermission asks for notification permission once (macOS
// prompts the user; other platforms no-op / return true).
func (a *App) requestNotificationPermission() {
	if a.ctx == nil {
		return
	}
	a.notifMu.Lock()
	if a.notifAuthAsked {
		a.notifMu.Unlock()
		return
	}
	a.notifAuthAsked = true
	a.notifMu.Unlock()
	_, _ = wailsruntime.RequestNotificationAuthorization(a.ctx)
}