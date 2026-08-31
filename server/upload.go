package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---- wire structs for the upload protocol ----

type initRequest struct {
	UploadID  string `json:"uploadId"`
	Filename  string `json:"filename"`
	TotalSize int64  `json:"totalSize"`
	ChunkSize int64  `json:"chunkSize"`
}

type initResponse struct {
	UploadID  string `json:"uploadId"`
	Filename  string `json:"filename"`
	TotalSize int64  `json:"totalSize"`
	ChunkSize int64  `json:"chunkSize"`
	State     string `json:"state"`
}

type statusResponse struct {
	UploadID           string  `json:"uploadId"`
	TotalSize          int64   `json:"totalSize"`
	ChunkSize          int64   `json:"chunkSize"`
	BytesReceived      int64   `json:"bytesReceived"`
	ReceivedChunkCount int64   `json:"receivedChunkCount"`
	NextChunk          int64   `json:"nextChunk"`
	ReceivedChunks     []int64 `json:"receivedChunks"`
	Complete           bool    `json:"complete"`
	State              string  `json:"state"`
}

type completeRequest struct {
	Hash string `json:"hash"` // SHA-256 hex of the file as sent by the phone
}

type completeResponse struct {
	OK        bool   `json:"ok"`
	FinalName string `json:"finalName"`
}

type chunkResponse struct {
	Received      bool   `json:"received"`
	Duplicate     bool   `json:"duplicate,omitempty"`
	BytesReceived int64  `json:"bytesReceived,omitempty"`
	Message       string `json:"message,omitempty"`
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func errBody(msg string) errorBody { return errorBody{Error: msg, Message: msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func decodeJSON(r *http.Request, dst any, limit int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func (s *Server) lookupUpload(id string) *Upload {
	s.upMu.Lock()
	u := s.uploads[id]
	s.upMu.Unlock()
	return u
}

// normalizeChunkSize clamps a phone-declared chunk size into protocol bounds.
func normalizeChunkSize(n int64) int64 {
	if n <= 0 {
		return defaultChunkSize
	}
	if n < minChunkSize {
		return minChunkSize
	}
	if n > maxChunkSize {
		return maxChunkSize
	}
	return n
}

// validUploadID accepts the client-generated upload identity (a UUIDv4 or any
// similar short DNS-safe token) and nothing else.
func validUploadID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

// ---- handlers ----

// handleUploadInit registers a new upload: the server creates authoritative
// state (temp file, bitmap, running hash) and returns confirmation.
func handleUploadInit(s *Server, w http.ResponseWriter, r *http.Request) {
	req := initRequest{}
	if err := decodeJSON(r, &req, 16*1024); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Couldn't start the upload — please try again."))
		return
	}
	req.ChunkSize = normalizeChunkSize(req.ChunkSize)

	if !validUploadID(req.UploadID) {
		writeJSON(w, http.StatusBadRequest, errBody("Couldn't start the upload — please try again."))
		return
	}
	if req.TotalSize <= 0 || req.TotalSize > maxFileSize {
		writeJSON(w, http.StatusBadRequest, errBody("That file is too big to send."))
		return
	}
	filename := sanitizeName(req.Filename)
	if filename == "" {
		writeJSON(w, http.StatusBadRequest, errBody("The file has no valid name."))
		return
	}

	// Disk full: check the declared size once up front rather than per chunk.
	if free, err := freeDiskBytes(s.destDir); err == nil && req.TotalSize > free {
		s.emit(Event{Type: EvFileError, UploadID: req.UploadID, Filename: filename, Total: req.TotalSize, Message: "Not enough space on this computer."})
		writeJSON(w, http.StatusInsufficientStorage, errBody("Not enough space on this computer to receive this file."))
		return
	} else if err != nil {
		s.log.Writef("disk free probe failed for %q: %v", s.destDir, err)
	}

	if existing := s.lookupUpload(req.UploadID); existing != nil {
		if existing.TotalSize == req.TotalSize && existing.ChunkSize == req.ChunkSize && existing.Filename == filename {
			// Idempotent re-init after a lost first response.
			writeJSON(w, http.StatusOK, initResponse{
				UploadID:  existing.ID,
				Filename:  existing.Filename,
				TotalSize: existing.TotalSize,
				ChunkSize: existing.ChunkSize,
				State:     existing.stateString(),
			})
			return
		}
		writeJSON(w, http.StatusConflict, errBody("That upload is already in progress with different settings."))
		return
	}

	partPath := filepath.Join(s.destDir, partFileName(req.UploadID))
	u, err := newUpload(req.UploadID, filename, req.TotalSize, req.ChunkSize, partPath)
	if err != nil {
		s.log.Writef("init: create part file for %s: %v", req.UploadID, err)
		writeJSON(w, http.StatusInternalServerError, errBody("Couldn't save the file — try again or pick a different save location."))
		return
	}

	s.upMu.Lock()
	if _, dup := s.uploads[req.UploadID]; dup {
		s.upMu.Unlock()
		u.removePart()
		writeJSON(w, http.StatusConflict, errBody("That upload is already in progress."))
		return
	}
	s.uploads[req.UploadID] = u
	s.upMu.Unlock()

	s.emit(Event{Type: EvFileStart, UploadID: u.ID, Filename: u.Filename, Total: u.TotalSize, Stage: "starting"})
	writeJSON(w, http.StatusOK, initResponse{
		UploadID:  u.ID,
		Filename:  filename,
		TotalSize: u.TotalSize,
		ChunkSize: u.ChunkSize,
		State:     string(StateInProgress),
	})
}

func handleUploadStatus(s *Server, w http.ResponseWriter, r *http.Request, id string) {
	u := s.lookupUpload(id)
	if u == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	u.mu.Lock()
	resp := statusResponse{
		UploadID:           u.ID,
		TotalSize:          u.TotalSize,
		ChunkSize:          u.ChunkSize,
		BytesReceived:      u.bytesRecv,
		ReceivedChunkCount: u.recvCount,
		NextChunk:          u.recvCount,
		ReceivedChunks:     u.receivedIndices(2000),
		Complete:           u.isComplete(),
		State:              string(u.state),
	}
	u.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func handleUploadChunk(s *Server, w http.ResponseWriter, r *http.Request, id, indexStr string) {
	index, err := strconv.ParseInt(indexStr, 10, 64)
	if err != nil || index < 0 {
		writeJSON(w, http.StatusBadRequest, errBody("Bad chunk index."))
		return
	}
	u := s.lookupUpload(id)
	if u == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}

	resp, status := s.applyChunk(u, index, r.Body)
	if status == http.StatusOK && resp.Received && !resp.Duplicate {
		s.emit(Event{Type: EvProgress, UploadID: u.ID, Filename: u.Filename, Bytes: resp.BytesReceived, Total: u.TotalSize})
	}
	writeJSON(w, status, resp)
}

// applyChunk commits one chunk under the upload lock. Committed chunks always
// form a contiguous in-order prefix, which keeps the running SHA-256 honest.
func (s *Server) applyChunk(u *Upload, index int64, body io.Reader) (chunkResponse, int) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.state != StateInProgress {
		return chunkResponse{Received: false, BytesReceived: u.bytesRecv, Message: "This upload is no longer in progress."}, http.StatusConflict
	}
	if index >= u.NChunks {
		return chunkResponse{Received: false, BytesReceived: u.bytesRecv, Message: "Bad chunk index."}, http.StatusBadRequest
	}
	if index < u.recvCount {
		// Duplicate from a retry after a lost response: drain and succeed.
		_, _ = io.Copy(io.Discard, io.LimitReader(body, u.ChunkSize+1))
		return chunkResponse{Received: true, Duplicate: true, BytesReceived: u.bytesRecv}, http.StatusOK
	}
	if index > u.recvCount {
		// Out-of-order: ask the caller to refresh status and fill the gap.
		return chunkResponse{Received: false, Duplicate: false, BytesReceived: u.bytesRecv, Message: "Missing earlier chunks — request status and continue."}, http.StatusConflict
	}

	data, err := io.ReadAll(io.LimitReader(body, u.ChunkSize+1))
	if err != nil {
		return chunkResponse{Received: false, BytesReceived: u.bytesRecv, Message: "Couldn't read the chunk — please try again."}, http.StatusBadRequest
	}
	offset := index * u.ChunkSize
	length := int64(len(data))
	if length > u.ChunkSize || offset+length > u.TotalSize {
		return chunkResponse{Received: false, BytesReceived: u.bytesRecv, Message: "Chunk is too big — please try again."}, http.StatusBadRequest
	}

	if _, werr := u.file.WriteAt(data, offset); werr != nil {
		s.log.Writef("chunk write failed %s idx %d: %v", u.ID, index, werr)
		return chunkResponse{Received: false, BytesReceived: u.bytesRecv, Message: "Couldn't save the file — the computer may be out of space."}, http.StatusInsufficientStorage
	}
	_, _ = u.hasher.Write(data)
	u.setChunk(index)
	u.recvCount++
	u.bytesRecv += length
	u.lastChunkAt = time.Now()

	return chunkResponse{Received: true, Duplicate: false, BytesReceived: u.bytesRecv}, http.StatusOK
}

func handleUploadComplete(s *Server, w http.ResponseWriter, r *http.Request, id string) {
	var req completeRequest
	if err := decodeJSON(r, &req, 8*1024); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Couldn't finish the upload — please try again."))
		return
	}
	req.Hash = strings.ToLower(strings.TrimSpace(req.Hash))
	if len(req.Hash) != 64 {
		writeJSON(w, http.StatusBadRequest, errBody("Couldn't finish the upload — please try again."))
		return
	}

	u := s.lookupUpload(id)
	if u == nil {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}

	u.mu.Lock()
	if u.state != StateInProgress {
		if u.state == StateComplete {
			// Idempotent completion after a lost first response.
			u.mu.Unlock()
			writeJSON(w, http.StatusOK, completeResponse{OK: true, FinalName: u.FinalName})
			return
		}
		u.mu.Unlock()
		writeJSON(w, http.StatusConflict, errBody("This upload is no longer in progress."))
		return
	}
	if !u.isComplete() {
		bytes := u.bytesRecv
		u.mu.Unlock()
		writeJSON(w, http.StatusConflict, chunkResponse{Received: false, BytesReceived: bytes, Message: "Upload isn't complete — continue sending."})
		return
	}

	serverHash := u.hashHex()
	if serverHash == req.Hash {
		origName := u.Filename
		finalName := s.uniqueFinalName(u.Filename)
		_ = u.closeFile()
		finalPath := filepath.Join(s.destDir, finalName)
		if err := os.Rename(u.partPath, finalPath); err != nil {
			s.log.Writef("complete: final rename failed %s -> %s: %v", u.partPath, finalPath, err)
			u.state = StateFailed
			u.errMsg = "rename failed: " + err.Error()
			u.removePart()
			u.mu.Unlock()
			s.emit(Event{Type: EvFileError, UploadID: u.ID, Filename: origName, Message: "Couldn't save the file — try a different save location."})
			writeJSON(w, http.StatusInsufficientStorage, errBody("Couldn't save the file — try a different save location."))
			return
		}
		u.FinalName = finalName
		u.state = StateComplete
		s.addCompletedFile(finalPath)
		u.mu.Unlock()
		s.emit(Event{Type: EvFileDone, UploadID: u.ID, Filename: origName, FinalName: finalName, Bytes: u.bytesRecv, Total: u.TotalSize})
		writeJSON(w, http.StatusOK, completeResponse{OK: true, FinalName: finalName})
		return
	}

	// Checksum mismatch: delete the temp file, fail loudly, never publish a
	// corrupt file under its final name.
	u.state = StateFailed
	u.errMsg = "checksum mismatch"
	s.log.Writef("checksum mismatch %s (%s): server=%s client=%s", id, u.Filename, serverHash, req.Hash)
	u.removePart()
	u.mu.Unlock()
	s.emit(Event{Type: EvFileError, UploadID: u.ID, Filename: u.Filename, Message: "Transfer failed — please try again."})
	writeJSON(w, http.StatusUnprocessableEntity, errBody("Transfer failed — please try again."))
}