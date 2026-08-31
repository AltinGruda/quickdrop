package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// logFile is a plain append-only debug log. The user is never shown its path or
// contents; it exists so a wrong LAN-IP guess or a failed write can be
// diagnosed after the fact.
type logFile struct {
	mu sync.Mutex
	f  *os.File
}

func logDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "QuickDrop"), nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Local", "QuickDrop"), nil
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Logs", "QuickDrop"), nil
		}
	default:
		if d := os.Getenv("XDG_STATE_HOME"); d != "" {
			return filepath.Join(d, "quickdrop"), nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "state", "quickdrop"), nil
		}
	}
	return "", fmt.Errorf("no log directory")
}

func openLog() (*logFile, error) {
	dir, err := logDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &logFile{f: f}, nil
}

func (l *logFile) Writef(format string, args ...any) {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf("%s "+format, append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	_, _ = fmt.Fprintln(l.f, line)
}

func (l *logFile) Close() {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	_ = l.f.Close()
	l.f = nil
	l.mu.Unlock()
}