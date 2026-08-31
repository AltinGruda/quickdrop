package server

import (
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleControlPage serves the browser-based control page used for development
// and quick manual testing without the desktop GUI.
func (s *Server) handleControlPage(w http.ResponseWriter, r *http.Request) {
	png, err := s.QRPNG(512)
	qr := ""
	if err == nil {
		qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	page := controlHTML
	page = strings.ReplaceAll(page, "QR_PLACEHOLDER", qr)
	page = strings.ReplaceAll(page, "URL_PLACEHOLDER", html.EscapeString(s.url))
	page = strings.ReplaceAll(page, "EVENTS_PLACEHOLDER", "/control/events")
	page = strings.ReplaceAll(page, "DEST_PLACEHOLDER", html.EscapeString(s.destDir))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, page)
}

// handleControlEvents is the SSE endpoint for control pages: every server event
// is streamed as a "data: {...}" line.
func (s *Server) handleControlEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	_, _ = io.WriteString(w, ": connected\n\n")
	fl.Flush()

	t := time.NewTicker(20 * time.Second)
	defer t.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return // hub closed (session teardown)
			}
			data, _ := json.Marshal(ev)
			_, _ = io.WriteString(w, "data: ")
			_, _ = w.Write(data)
			_, _ = io.WriteString(w, "\n\n")
			fl.Flush()
		case <-t.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}