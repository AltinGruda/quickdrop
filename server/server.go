package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

var (
	// ErrNoNetwork is returned by Start/EnumerateCandidates when no usable LAN
	// address exists.
	ErrNoNetwork = errors.New("no network")
	// ErrDestUnavailable wraps destination-folder failures (create/writable).
	ErrDestUnavailable = errors.New("destination unavailable")
)

// tokenAlphabet omits visually ambiguous characters (0/O/1/l/I).
const tokenAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"

const (
	uploadTTL    = 30 * time.Minute // no chunk received -> expire
	orphanTTL    = 24 * time.Hour   // orphaned temp files older than this get swept
	pagePingWait = 5 * time.Second  // phone loaded the page but ping never arrived
	maxPages     = 64               // recent page visits retained for isolation checks
)

// Config controls one server session.
type Config struct {
	DestDir string
	OnEvent func(Event) // may be nil (e.g. the headless CLI)
}

// Server is one QuickDrop session: an HTTP server on a dynamic port plus the
// phone-facing upload page and the event plumbing into the GUI.
type Server struct {
	lifeMu sync.Mutex
	closed bool
	stopCh chan struct{}
	wg     sync.WaitGroup

	cfg       Config
	token     string
	port      int
	destDir   string
	url       string

	listener net.Listener
	mux      *http.ServeMux
	httpSrv  *http.Server

	primaryIP string
	altURLs   []string

	pagesMu sync.Mutex
	pages   []pageVisit

	upMu    sync.Mutex
	uploads map[string]*Upload

	foMu       sync.Mutex
	filesOnDisk []string
	reserved   map[string]bool

	hub *eventHub
	log *logFile
}

type pageVisit struct {
	at     time.Time
	pinged bool
}

// Start creates a new session: resolves the LAN address, binds a dynamic port,
// generates a fresh token, serves the phone page, and starts the sweeps.
func Start(cfg Config) (*Server, error) {
	cands, err := EnumerateCandidates()
	if err != nil {
		return nil, ErrNoNetwork
	}

	destDir := cfg.DestDir
	if destDir == "" {
		destDir = DefaultDestDir()
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: could not create destination folder: %v", ErrDestUnavailable, err)
	}
	if err := checkWritable(destDir); err != nil {
		return nil, fmt.Errorf("%w: destination folder is not writable: %v", ErrDestUnavailable, err)
	}

	log, _ := openLog()

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("could not start network listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	token, err := newToken(8)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	primary := cands[0].IP
	altURLs := make([]string, 0, len(cands))
	for _, c := range cands[1:] {
		altURLs = append(altURLs, fmt.Sprintf("http://%s:%d/%s", c.IP, port, token))
	}

	url := fmt.Sprintf("http://%s:%d/%s", primary, port, token)

	s := &Server{
		stopCh:    make(chan struct{}),
		cfg:       cfg,
		token:     token,
		port:      port,
		destDir:   destDir,
		url:       url,
		listener:  listener,
		primaryIP: primary,
		altURLs:   altURLs,
		uploads:   make(map[string]*Upload),
		reserved:  make(map[string]bool),
		hub:       newEventHub(),
		log:       log,
	}

	// Sweep temp files left by a previous crash (older than ~24h).
	s.sweepOrphanParts()

	s.mux = http.NewServeMux()
	s.routes()

	s.httpSrv = &http.Server{
		Handler:     s.mux,
		ReadTimeout: 2 * time.Minute,
		IdleTimeout: 90 * time.Second,
	}

	if log != nil {
		log.Writef("session started token=%s port=%d dest=%s primary=%s",
			token, port, destDir, primary)
		for i, c := range cands {
			log.Writef("candidate[%d] ip=%s iface=%q name=%q private=%v score=%d reason=%s",
				i, c.IP, c.Iface, c.Name, c.Private, c.Score, c.Reason)
		}
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = s.httpSrv.Serve(listener)
	}()

	s.startSweeps()

	return s, nil
}

func (s *Server) routes() {
	mux := s.mux
	mux.HandleFunc("GET /{token}", s.handlePhonePage)
	mux.HandleFunc("GET /{token}/ping", s.handlePing)
	mux.HandleFunc("GET /{token}/static/upload.css", s.static(uploadCSS))
	mux.HandleFunc("GET /{token}/static/upload.js", s.static(uploadJS))
	mux.HandleFunc("POST /{token}/upload/init", s.handleInit)
	mux.HandleFunc("GET /{token}/upload/{id}/status", s.handleStatus)
	mux.HandleFunc("POST /{token}/upload/{id}/chunk/{index}", s.handleChunk)
	mux.HandleFunc("POST /{token}/upload/{id}/complete", s.handleComplete)
	mux.HandleFunc("GET /control", s.handleControlPage)
	mux.HandleFunc("GET /control/events", s.handleControlEvents)
	mux.HandleFunc("GET /", s.handleRoot)
}

// validToken reports whether the request path carries the current session token.
// Old tokens (from a previous session) must 404, never serve.
func (s *Server) validToken(r *http.Request) bool {
	return r.PathValue("token") == s.token
}

func (s *Server) static(content []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validToken(r) {
			writeJSON(w, http.StatusNotFound, errBody("not found"))
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(content)
	}
}

func (s *Server) handlePhonePage(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r) {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	s.recordPage()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, phonePageHTML(s.token))
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r) {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	s.notePing()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	http.Redirect(w, r, "/control", http.StatusFound)
}

// ---- init / status / chunk / complete (thin token-aware wrappers) ----

func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r) {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	handleUploadInit(s, w, r)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r) {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	handleUploadStatus(s, w, r, r.PathValue("id"))
}

func (s *Server) handleChunk(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r) {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	handleUploadChunk(s, w, r, r.PathValue("id"), r.PathValue("index"))
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r) {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	handleUploadComplete(s, w, r, r.PathValue("id"))
}

// ---- events ----

func (s *Server) emit(ev Event) {
	s.hub.publish(ev)
	if s.cfg.OnEvent != nil {
		s.cfg.OnEvent(ev)
	}
}

// ---- accessors ----

func (s *Server) URL() string          { return s.url }
func (s *Server) Token() string        { return s.token }
func (s *Server) Port() int            { return s.port }
func (s *Server) PrimaryIP() string    { return s.primaryIP }
func (s *Server) AltURLs() []string    { return s.altURLs }
func (s *Server) DestDir() string      { return s.destDir }

func (s *Server) FilesOnDisk() []string {
	s.foMu.Lock()
	defer s.foMu.Unlock()
	out := make([]string, len(s.filesOnDisk))
	copy(out, s.filesOnDisk)
	return out
}

func (s *Server) QRPNG(size int) ([]byte, error) {
	return qrcode.Encode(s.url, qrcode.High, size)
}

func (s *Server) ControlURL() string {
	return fmt.Sprintf("http://%s:%d/control", s.primaryIP, s.port)
}

// Shutdown stops the HTTP server (listener + in-flight requests) with a short
// timeout, stops the sweeps, and tears down all upload temp state.
func (s *Server) Shutdown() error {
	if s == nil {
		return nil
	}
	s.lifeMu.Lock()
	if s.closed {
		s.lifeMu.Unlock()
		return nil
	}
	s.closed = true
	s.lifeMu.Unlock()

	close(s.stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.httpSrv.Shutdown(ctx)

	s.wg.Wait()
	s.tidySession()
	s.emit(Event{Type: EvSessionStopped, Message: "Session ended."})

	if s.log != nil {
		s.log.Writef("session stopped token=%s port=%d", s.token, s.port)
		s.log.Close()
	}
	return err
}

// ---- filenames & temp files ----

func partFileName(id string) string {
	return "." + id + partSuffix
}

// sanitizeName strips directory components and path-hostile characters so a
// hostile or buggy phone can never write outside the destination folder.
func sanitizeName(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, c := range name {
		switch c {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			b.WriteByte('_')
		default:
			b.WriteRune(c)
		}
	}
	out := b.String()
	out = strings.TrimSpace(out)
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

// uniqueFinalName reserves a collision-free final name for an upload, appending
// " (1)", " (2)", ... rather than ever overwriting an existing file.
func (s *Server) uniqueFinalName(name string) string {
	s.foMu.Lock()
	defer s.foMu.Unlock()
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	candidate := name
	for i := 1; ; i++ {
		if !s.reserved[candidate] && !existsOnDisk(filepath.Join(s.destDir, candidate)) {
			s.reserved[candidate] = true
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

func existsOnDisk(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (s *Server) addCompletedFile(path string) {
	s.foMu.Lock()
	s.filesOnDisk = append(s.filesOnDisk, path)
	s.foMu.Unlock()
}

// ---- tokens ----

func newToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = tokenAlphabet[int(v)%len(tokenAlphabet)]
	}
	return string(out), nil
}

// ---- destination folder default ----

// DefaultDestDir returns the platform's Downloads folder plus QuickDrop, falling
// back to the home directory.
func DefaultDestDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		if d, err2 := os.UserConfigDir(); err2 == nil {
			_ = d
		}
		return filepath.Join(home, "Downloads", "QuickDrop")
	}
	if d := os.Getenv("HOME"); d != "" {
		return filepath.Join(d, "Downloads", "QuickDrop")
	}
	return "QuickDrop"
}