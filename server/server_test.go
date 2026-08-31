package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventSink collects events from the OnEvent callback.
type eventSink struct {
	ch chan Event
}

func newSink() *eventSink { return &eventSink{ch: make(chan Event, 512)} }

func (e *eventSink) add(ev Event) {
	select {
	case e.ch <- ev:
	default:
	}
}

func (e *eventSink) next(timeout time.Duration) Event {
	select {
	case ev := <-e.ch:
		return ev
	case <-time.After(timeout):
		return Event{}
	}
}

func startTestServer(t *testing.T) (*Server, *eventSink, string) {
	t.Helper()
	dest := t.TempDir()
	sink := newSink()
	srv, err := Start(Config{DestDir: dest, OnEvent: sink.add})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	return srv, sink, dest
}

func tokenOf(s *Server) string {
	return s.Token()
}

func mustInit(t *testing.T, base, id, filename string, size, chunk int64) {
	t.Helper()
	body := fmt.Sprintf(`{"uploadId":%q,"filename":%q,"totalSize":%d,"chunkSize":%d}`,
		id, filename, size, chunk)
	resp, err := http.Post(base+"/upload/init", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("init status %d: %s", resp.StatusCode, b)
	}
}

func mustChunk(t *testing.T, base, id string, index int, data []byte) chunkResponse {
	t.Helper()
	resp, err := http.Post(base+"/upload/"+id+"/chunk/"+fmt.Sprint(index),
		"application/octet-stream", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("chunk %d: %v", index, err)
	}
	defer resp.Body.Close()
	var cr chunkResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	if resp.StatusCode != http.StatusOK || !cr.Received {
		t.Fatalf("chunk %d status %d: %+v", index, resp.StatusCode, cr)
	}
	return cr
}

func mustComplete(t *testing.T, base, id, hash string) completeResponse {
	t.Helper()
	body := fmt.Sprintf(`{"hash":%q}`, hash)
	resp, err := http.Post(base+"/upload/"+id+"/complete", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer resp.Body.Close()
	var cr completeResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("complete status %d: %s", resp.StatusCode, b)
	}
	return cr
}

// uploadWhole sends an entire file in chunks and completes it, returning the
// expected hash for cross-checks.
func uploadWhole(t *testing.T, base, id, filename string, data []byte) {
	t.Helper()
	chunk := int64(2 * 1024 * 1024)
	mustInit(t, base, id, filename, int64(len(data)), chunk)
	n := (int64(len(data)) + chunk - 1) / chunk
	for i := int64(0); i < n; i++ {
		start := i * chunk
		end := start + chunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		mustChunk(t, base, id, int(i), data[start:end])
	}
	h := sha256.Sum256(data)
	mustComplete(t, base, id, hex.EncodeToString(h[:]))
}

func TestEndToEndChunkedUpload(t *testing.T) {
	srv, sink, dest := startTestServer(t)
	base := srv.URL()
	id := "11111111-1111-4111-8111-111111111111"

	data := bytes.Repeat([]byte("QuickDrop payload! 123 "), 70000) // ~1.5 MB
	uploadWhole(t, base, id, "trip photo.jpg", data)

	finalPath := filepath.Join(dest, "trip photo.jpg")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("final file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("file contents do not match what was uploaded")
	}

	// no .part files left behind
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partSuffix) {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}

	if len(srv.FilesOnDisk()) != 1 {
		t.Fatalf("FilesOnDisk = %d, want 1", len(srv.FilesOnDisk()))
	}

	// event sequence: file-start then progress then file-done
	if ev := sink.next(2 * time.Second); ev.Type != EvFileStart {
		t.Fatalf("first event = %q, want file-start", ev.Type)
	}
	foundDone := false
	deadline := time.After(5 * time.Second)
	for !foundDone {
		select {
		case <-deadline:
			t.Fatal("never saw file-done event")
		default:
		}
		ev := sink.next(1 * time.Second)
		if ev.Type == EvFileDone {
			foundDone = true
			if ev.FinalName != "trip photo.jpg" {
				t.Fatalf("FinalName = %q", ev.FinalName)
			}
		}
	}
}

func TestResumeAfterInterruption(t *testing.T) {
	srv, _, dest := startTestServer(t)
	id := "22222222-2222-4222-8222-222222222222"
	chunk := int64(512 * 1024)
	total := int64(5*chunk + 123)
	data := bytes.Repeat([]byte("resume-me-"), int((total+9)/10))
	data = data[:total]

	mustInit(t, srv.URL(), id, "resume.bin", int64(len(data)), chunk)

	for i := int64(0); i < 3; i++ { // send 3 of 6
		start := i * chunk
		end := start + chunk
		post := srv.URL() + "/upload/" + id + "/chunk/" + fmt.Sprint(i)
		resp, err := http.Post(post, "application/octet-stream", bytes.NewReader(data[start:end]))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	// "Phone dropped the connection and came back": ask status, resume from gaps.
	var st statusResponse
	resp, err := http.Get(srv.URL() + "/upload/" + id + "/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.NextChunk != 3 || st.ReceivedChunkCount != 3 || st.BytesReceived != 3*chunk {
		t.Fatalf("unexpected status: %+v", st)
	}
	if len(st.ReceivedChunks) != 3 {
		t.Fatalf("received chunks = %v", st.ReceivedChunks)
	}

	for i := int64(3); i < 6; i++ {
		start := i * chunk
		end := start + chunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		mustChunk(t, srv.URL(), id, int(i), data[start:end])
	}

	h := sha256.Sum256(data)
	mustComplete(t, srv.URL(), id, hex.EncodeToString(h[:]))

	got, err := os.ReadFile(filepath.Join(dest, "resume.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("resumed file mismatch")
	}
}

func TestDuplicateChunkIsIdempotent(t *testing.T) {
	srv, _, _ := startTestServer(t)
	id := "33333333-3333-4333-8333-333333333333"
	chunk := int64(1024)
	data := bytes.Repeat([]byte("dup"), 1024/3+2)

	mustInit(t, srv.URL(), id, "dup.bin", int64(len(data)), chunk)

	// send chunk 0 twice
	cr := mustChunk(t, srv.URL(), id, 0, data)
	if cr.Duplicate {
		t.Fatal("first send should not be duplicate")
	}
	cr2 := mustChunk(t, srv.URL(), id, 0, data)
	if !cr2.Duplicate {
		t.Fatal("second send should be duplicate")
	}

	// after the duplicate, server state is still exactly one chunk
	var st statusResponse
	resp, _ := http.Get(srv.URL() + "/upload/" + id + "/status")
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.ReceivedChunkCount != 1 {
		t.Fatalf("expected 1 chunk, got %d", st.ReceivedChunkCount)
	}

	h := sha256.Sum256(data)
	mustComplete(t, srv.URL(), id, hex.EncodeToString(h[:]))
}

func TestOutOfOrderChunkRejected(t *testing.T) {
	srv, _, _ := startTestServer(t)
	id := "44444444-4444-4444-8444-444444444444"
	data := []byte("hello out of order world")

	mustInit(t, srv.URL(), id, "ooo.bin", 3*minChunkSize, minChunkSize)

	resp, err := http.Post(srv.URL()+"/upload/"+id+"/chunk/1", "application/octet-stream",
		bytes.NewReader(data[8:]))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cr chunkResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	if resp.StatusCode != http.StatusConflict || cr.Received {
		t.Fatalf("out-of-order chunk: status=%d %+v", resp.StatusCode, cr)
	}
}

func TestChecksumMismatchFails(t *testing.T) {
	srv, sink, dest := startTestServer(t)
	id := "55555555-5555-4555-8555-555555555555"
	data := bytes.Repeat([]byte("X"), int(3*minChunkSize+50))
	chunk := int64(minChunkSize)

	mustInit(t, srv.URL(), id, "corrupt.bin", int64(len(data)), chunk)
	n := (int64(len(data)) + chunk - 1) / chunk
	for i := int64(0); i < n; i++ {
		start := i * chunk
		end := start + chunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		mustChunk(t, srv.URL(), id, int(i), data[start:end])
	}

	// Deliberately wrong hash.
	bad := strings.Repeat("0", 64)
	resp, err := http.Post(srv.URL()+"/upload/"+id+"/complete", "application/json",
		strings.NewReader(fmt.Sprintf(`{"hash":%q}`, bad)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("complete with bad hash: status %d (%s)", resp.StatusCode, b)
	}

	// The failed file must never appear under its real name, and no .part remains.
	if _, err := os.Stat(filepath.Join(dest, "corrupt.bin")); err == nil {
		t.Fatal("corrupt.bin should not exist on disk")
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partSuffix) {
			t.Fatalf("temp file left after checksum failure: %s", e.Name())
		}
	}

	var st statusResponse
	gresp, _ := http.Get(srv.URL() + "/upload/" + id + "/status")
	_ = json.NewDecoder(gresp.Body).Decode(&st)
	gresp.Body.Close()
	if st.State != string(StateFailed) {
		t.Fatalf("state = %q, want failed", st.State)
	}

	// A file-error event is pushed to the GUI.
	deadline := time.After(3 * time.Second)
	for {
		ev := sink.next(500 * time.Millisecond)
		if ev.Type == EvFileError {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no file-error event emitted")
		default:
		}
	}
}

func TestDuplicateFilenamesRename(t *testing.T) {
	srv, _, dest := startTestServer(t)
	data := []byte("same content")

	uploadWhole(t, srv.URL(), "aaaa0000-aaaa-4aaa-8aaa-aaaaaaaaaaa1", "vacation.jpg", data)
	uploadWhole(t, srv.URL(), "aaaa0000-aaaa-4aaa-8aaa-aaaaaaaaaaa2", "vacation.jpg", data)
	uploadWhole(t, srv.URL(), "aaaa0000-aaaa-4aaa-8aaa-aaaaaaaaaaa3", "vacation.jpg", data)

	want := []string{"vacation.jpg", "vacation (1).jpg", "vacation (2).jpg"}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("expected %s on disk: %v", name, err)
		}
	}
}

func TestTokenIsolation(t *testing.T) {
	srv, _, _ := startTestServer(t)
	// wrong token anywhere -> 404
	for _, p := range []string{
		"http://invalid:1/" + srv.PrimaryIPErrTokenStub(),
	} {
		_ = p
	}
	resp, err := http.Get(srv.URL() + "/upload/zzzz/status") // nil upload id under right token
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown upload status = %d", resp.StatusCode)
	}

	// A completely different token must 404 on the page and the ping.
	wrong := srv.URL()[:strings.LastIndex(srv.URL(), "/")] + "/WRONGTOKEN"
	r, err := http.Get(wrong)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong token page = %d", r.StatusCode)
	}
	r2, _ := http.Get(wrong + "/ping")
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong token ping = %d", r2.StatusCode)
	}
}

func TestPhonePageServesTokenAndPing(t *testing.T) {
	srv, _, _ := startTestServer(t)
	page, err := http.Get(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatal("page status", page.StatusCode)
	}
	if !bytes.Contains(b, []byte("Choose files to send")) {
		t.Fatal("page missing the choose-files button")
	}
	if !bytes.Contains(b, []byte(srv.Token())) {
		t.Fatal("page does not carry the session token")
	}
	if !bytes.Contains(b, []byte("upload.js")) {
		t.Fatal("page does not reference the client script")
	}
	if !bytes.Contains(b, []byte("id=\"fileInput\"")) {
		t.Fatal("page missing the hidden file input the picker button triggers")
	}

	js, err := http.Get(srv.URL() + "/static/upload.js")
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := io.ReadAll(js.Body)
	js.Body.Close()
	if !bytes.Contains(jb, []byte("sha256")) {
		t.Fatal("upload.js missing its hashing code")
	}

	p, err := http.Get(srv.URL() + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := io.ReadAll(p.Body)
	p.Body.Close()
	if string(pb) != "ok" {
		t.Fatalf("ping = %q", pb)
	}
}

func TestChunkTooLargeRejected(t *testing.T) {
	srv, _, _ := startTestServer(t)
	id := "66666666-6666-4666-8666-666666666666"
	data := make([]byte, minChunkSize+1)
	chunk := int64(minChunkSize)
	mustInit(t, srv.URL(), id, "big.bin", int64(len(data)), chunk)
	resp, err := http.Post(srv.URL()+"/upload/"+id+"/chunk/0", "application/octet-stream",
		bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized chunk accepted: %d", resp.StatusCode)
	}
}

func TestConcurrentUploadsFromSeveralPhones(t *testing.T) {
	srv, _, dest := startTestServer(t)
	base := srv.URL()

	const n = 6
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// two phones send the same filename, the rest unique ones
			var name string
			if i%2 == 0 {
				name = fmt.Sprintf("phone-%d.txt", i)
			} else {
				name = "shared.jpg"
			}
			data := bytes.Repeat([]byte{byte('a' + i)}, int((int64(i+1)*int64(minChunkSize))/int64(2))+7)
			id := fmt.Sprintf("cc11%04d-4000-4800-8000-0000%08d", i, i)
			err := runWhole(base, id, name, data)
			if err != nil {
				errCh <- fmt.Errorf("upload %d (%s): %w", i, name, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	if got := len(srv.FilesOnDisk()); got != n {
		t.Fatalf("FilesOnDisk = %d, want %d", got, n)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != n {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dest has %d files (%v), want %d", len(entries), names, n)
	}
}

// runWhole does a full upload outside the helpers to run in goroutines.
func runWhole(base, id, filename string, data []byte) error {
	chunk := int64(minChunkSize)
	body := fmt.Sprintf(`{"uploadId":%q,"filename":%q,"totalSize":%d,"chunkSize":%d}`,
		id, filename, len(data), chunk)
	resp, err := http.Post(base+"/upload/init", "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("init status %d", resp.StatusCode)
	}

	n := (int64(len(data)) + chunk - 1) / chunk
	for i := int64(0); i < n; i++ {
		start := i * chunk
		end := start + chunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		r, err := http.Post(base+"/upload/"+id+"/chunk/"+fmt.Sprint(i),
			"application/octet-stream", bytes.NewReader(data[start:end]))
		if err != nil {
			return err
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			return fmt.Errorf("chunk %d status %d", i, r.StatusCode)
		}
	}
	h := sha256.Sum256(data)
	rc, err := http.Post(base+"/upload/"+id+"/complete", "application/json",
		strings.NewReader(fmt.Sprintf(`{"hash":%q}`, hex.EncodeToString(h[:]))))
	if err != nil {
		return err
	}
	io.Copy(io.Discard, rc.Body)
	rc.Body.Close()
	if rc.StatusCode != http.StatusOK {
		return fmt.Errorf("complete status %d", rc.StatusCode)
	}
	return nil
}

func TestStaleUploadExpires(t *testing.T) {
	srv, sink, dest := startTestServer(t)
	id := "77777777-7777-4777-8777-777777777777"
	mustInit(t, srv.URL(), id, "stale.bin", 1000, 100)
	mustChunk(t, srv.URL(), id, 0, bytes.Repeat([]byte("a"), 100))

	// Backdate lastChunkAt so the sweep considers it abandoned.
	u := srv.lookupUpload(id)
	if u == nil {
		t.Fatal("upload not found")
	}
	u.mu.Lock()
	u.lastChunkAt = time.Now().Add(-31 * time.Minute)
	u.mu.Unlock()

	srv.expireStaleUploads()

	if srv.lookupUpload(id) != nil {
		t.Fatal("stale upload should have been evicted")
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partSuffix) {
			t.Fatalf("temp file left after expiry: %s", e.Name())
		}
	}
	ev := sink.next(2 * time.Second)
	if ev.Type == EvFileError && ev.UploadID != id {
		t.Fatalf("expiry event for wrong upload: %+v", ev)
	}
}

func TestOrphanSweep(t *testing.T) {
	srv, _, dest := startTestServer(t)
	// old orphan part -> removed
	old := filepath.Join(dest, ".orphan-old"+partSuffix)
	_ = os.WriteFile(old, []byte("junk"), 0o644)
	oldT := time.Now().Add(-25 * time.Hour)
	_ = os.Chtimes(old, oldT, oldT)
	// fresh orphan part -> kept
	fresh := filepath.Join(dest, ".orphan-fresh"+partSuffix)
	_ = os.WriteFile(fresh, []byte("junk"), 0o644)

	srv.sweepOrphanParts()

	if _, err := os.Stat(old); err == nil {
		t.Fatal("old orphan part not removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh orphan part should be kept")
	}
	_ = os.Remove(fresh)
}

func TestShutdownCleansTempState(t *testing.T) {
	srv, _, dest := startTestServer(t)
	id := "88888888-8888-4888-8888-888888888888"
	mustInit(t, srv.URL(), id, "mid.bin", 100000, 64000)
	mustChunk(t, srv.URL(), id, 0, bytes.Repeat([]byte("b"), 64000))

	if err := srv.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), partSuffix) {
			t.Fatalf("temp file survived shutdown: %s", e.Name())
		}
	}
}

func TestEnumerateCandidates(t *testing.T) {
	cands, err := EnumerateCandidates()
	if err == ErrNoNetwork {
		t.Skip("no LAN interfaces on this machine")
	}
	if err != nil {
		t.Fatalf("EnumerateCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	// sorted descending by score
	for i := 1; i < len(cands); i++ {
		if cands[i].Score > cands[i-1].Score {
			t.Fatalf("candidates not sorted by score: %+v", cands)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"../../evil.bin":       "evil.bin",
		`C:\fakepath\evil.txt`: "evil.txt",
		"a/b\\c:d*.txt":        "c_d_.txt", // filepath.Base drops the dir parts first
		"..":                   "",
		"   ":                  "",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// PrimaryIPErrTokenStub exists solely to keep TokenIsolation readable.
func (s *Server) PrimaryIPErrTokenStub() string {
	return s.primaryIP
}
