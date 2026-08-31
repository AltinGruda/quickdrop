package server

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// startSweeps runs the background maintenance loop: upload expiry and the
// client-isolation page scan. It is not a per-upload timer.
func (s *Server) startSweeps() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				s.expireStaleUploads()
				s.scanPageVisits()
			}
		}
	}()
}

// expireStaleUploads deletes in-progress uploads that have not received a chunk
// in uploadTTL (30 minutes), freeing their temp files.
func (s *Server) expireStaleUploads() {
	now := time.Now()
	s.upMu.Lock()
	ids := make([]string, 0, len(s.uploads))
	for id := range s.uploads {
		ids = append(ids, id)
	}
	s.upMu.Unlock()

	var expired []*Upload
	for _, id := range ids {
		u := s.lookupUpload(id)
		if u == nil {
			continue
		}
		u.mu.Lock()
		if u.state == StateInProgress && now.Sub(u.lastChunkAt) > uploadTTL {
			u.state = StateExpired
			u.errMsg = "no chunks received for 30 minutes"
			u.removePart()
			expired = append(expired, u)
		}
		u.mu.Unlock()
	}

	if len(expired) == 0 {
		return
	}
	s.upMu.Lock()
	for _, u := range expired {
		delete(s.uploads, u.ID)
	}
	s.upMu.Unlock()

	for _, u := range expired {
		s.emit(Event{Type: EvFileError, UploadID: u.ID, Filename: u.Filename, Message: "The transfer was interrupted and did not finish."})
	}
}

// sweepOrphanParts removes temp files left behind by a previous crash. Only
// files older than orphanTTL are touched, so a freshly interrupted real session
// is never disturbed.
func (s *Server) sweepOrphanParts() {
	entries, err := os.ReadDir(s.destDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-orphanTTL)
	removed := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), partSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(s.destDir, e.Name())) == nil {
				removed++
			}
		}
	}
	if removed > 0 && s.log != nil {
		s.log.Writef("orphan temp sweep removed %d file(s)", removed)
	}
}

// tidySession removes all upload temp state for a session being torn down.
// Completed files remain on disk; only metadata and .part files are cleaned.
func (s *Server) tidySession() {
	s.upMu.Lock()
	uploads := make([]*Upload, 0, len(s.uploads))
	for _, u := range s.uploads {
		uploads = append(uploads, u)
	}
	s.uploads = make(map[string]*Upload)
	s.upMu.Unlock()

	for _, u := range uploads {
		u.mu.Lock()
		u.removePart()
		u.mu.Unlock()
	}

	s.pagesMu.Lock()
	s.pages = nil
	s.pagesMu.Unlock()

	if s.hub != nil {
		s.hub.close()
	}
}

// ---- phone page / ping tracking for client-isolation detection ----

func (s *Server) recordPage() {
	now := time.Now()
	s.pagesMu.Lock()
	if len(s.pages) < maxPages {
		s.pages = append(s.pages, pageVisit{at: now})
	} else {
		s.pages[len(s.pages)-1] = pageVisit{at: now}
	}
	s.pagesMu.Unlock()
}

func (s *Server) notePing() {
	s.pagesMu.Lock()
	for i := range s.pages {
		s.pages[i].pinged = true
	}
	s.pagesMu.Unlock()
}

// scanPageVisits reports pages that were served but whose ping never came back
// within pagePingWait — a strong hint that the Wi-Fi isolates devices from each
// other despite both being "connected".
func (s *Server) scanPageVisits() {
	cutoff := time.Now().Add(-pagePingWait)
	var unresolved int
	s.pagesMu.Lock()
	kept := s.pages[:0]
	for _, p := range s.pages {
		if p.pinged {
			continue
		}
		if p.at.Before(cutoff) {
			unresolved++
			continue
		}
		kept = append(kept, p)
	}
	s.pages = kept
	s.pagesMu.Unlock()

	if unresolved > 0 {
		s.emit(Event{Type: EvClientIsolation, Message: "Your phone opened the page but can't reach this computer — this usually means the Wi-Fi network won't let devices talk to each other. Try a personal hotspot instead of this Wi-Fi."})
	}
}