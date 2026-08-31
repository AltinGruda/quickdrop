package server

import "sync"

// EventType describes the kind of event pushed out of the HTTP handlers into
// the Wails GUI (via the Config.OnEvent callback) and also into the browser
// control page (via Server-Sent Events).
type EventType string

const (
	// EvFileStart fires when a phone registers a new upload (init).
	EvFileStart EventType = "file-start"
	// EvProgress fires after every committed chunk; carries live byte counts.
	EvProgress EventType = "progress"
	// EvFileDone fires when an upload has been verified and renamed into place.
	EvFileDone EventType = "file-done"
	// EvFileError fires when an upload fails (checksum, disk, expiry, ...).
	EvFileError EventType = "file-error"
	// EvClientIsolation fires when a phone loaded the page but its ping never
	// reached the server (device isolation on the Wi-Fi network).
	EvClientIsolation EventType = "isolation"
	// EvInfo fires for non-file informational messages.
	EvInfo EventType = "info"
	// EvSessionStopped fires after a session has been torn down (shutdown / new session).
	EvSessionStopped EventType = "session-stopped"
)

// Event is a single update produced by a server goroutine. All fields that are
// not relevant to a given event type are left empty.
type Event struct {
	Type      EventType `json:"type"`
	UploadID  string    `json:"uploadId,omitempty"`
	Filename  string    `json:"filename,omitempty"`
	FinalName string    `json:"finalName,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	Total     int64     `json:"total,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	Message   string    `json:"message,omitempty"`
}

// eventHub fans events out to the browser control page over SSE. Sends are
// non-blocking so a slow control-page client can never stall an upload.
type eventHub struct {
	subMux  sync.Mutex
	clients map[chan Event]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{clients: make(map[chan Event]struct{})}
}

func (h *eventHub) subscribe() chan Event {
	ch := make(chan Event, 64)
	h.subMux.Lock()
	h.clients[ch] = struct{}{}
	h.subMux.Unlock()
	return ch
}

func (h *eventHub) unsubscribe(ch chan Event) {
	h.subMux.Lock()
	delete(h.clients, ch)
	h.subMux.Unlock()
}

func (h *eventHub) publish(ev Event) {
	h.subMux.Lock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// slow client: drop this event rather than blocking
		}
	}
	h.subMux.Unlock()
}

// close disconnects every subscribed control page.
func (h *eventHub) close() {
	h.subMux.Lock()
	for ch := range h.clients {
		close(ch)
	}
	h.clients = make(map[chan Event]struct{})
	h.subMux.Unlock()
}