package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// historyMax is the cap on persisted received-file entries. Older entries are
// dropped from the front (oldest first).
const historyMax = 200

// HistoryEntry is one completed file transfer, kept across sessions so users
// can reopen files they received earlier.
type HistoryEntry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	FinalName string    `json:"finalName"`
	Path      string    `json:"path"`
	At        time.Time `json:"at"`
}

// historyStore persists the received-files history to a JSON file next to the
// app config. It is safe for concurrent use.
type historyStore struct {
	mu   sync.Mutex
	path string
	rows []HistoryEntry
}

// historyPath returns %APPDATA%/QuickDrop/history.json (Windows) or the
// equivalent user config dir on macOS/Linux.
func historyPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "QuickDrop", "history.json")
}

// newHistoryStore loads (or initializes) the history file.
func newHistoryStore() *historyStore {
	h := &historyStore{path: historyPath()}
	b, err := os.ReadFile(h.path)
	if err != nil {
		return h
	}
	_ = json.Unmarshal(b, &h.rows)
	return h
}

// add prepends a new entry (most recent first) and persists, honoring the cap.
func (h *historyStore) add(entry HistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	h.rows = append([]HistoryEntry{entry}, h.rows...)
	if len(h.rows) > historyMax {
		h.rows = h.rows[:historyMax]
	}
	h.save()
}

// list returns a copy of the current history, most recent first.
func (h *historyStore) list() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HistoryEntry, len(h.rows))
	copy(out, h.rows)
	return out
}

// clear removes the persisted history (used by a future "Clear history" action).
func (h *historyStore) clear() {
	h.mu.Lock()
	h.rows = nil
	h.save()
	h.mu.Unlock()
}

// save writes the current rows to disk atomically (temp file + rename).
func (h *historyStore) save() {
	b, err := json.MarshalIndent(h.rows, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	_ = os.WriteFile(h.path, b, 0o600)
}