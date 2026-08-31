package server

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"os"
	"sync"
	"time"
)

// Protocol constants. The chunk size is fixed for v1; a phone declares its own
// chunk size at init time (defaulting to defaultChunkSize) within these bounds.
const (
	defaultChunkSize = 2 * 1024 * 1024 // 2 MiB
	minChunkSize     = 256 * 1024      // 256 KiB
	maxChunkSize     = 16 * 1024 * 1024 // 16 MiB
	maxFileSize      = int64(4) << 40  // 4 TiB sanity cap
	partSuffix       = ".quickdrop.part"
)

// UploadState is the life-cycle state of a single upload.
type UploadState string

const (
	StateInProgress UploadState = "in-progress"
	StateComplete   UploadState = "complete"
	StateFailed     UploadState = "failed"
	StateExpired    UploadState = "expired"
)

// Upload is the server-owned, authoritative record for one file transfer.
// The phone never tells the server how much it has already sent; every status
// answer is derived from this record's own bitmap and byte counter.
type Upload struct {
	mu sync.Mutex

	ID        string // client-generated uploadId (the protocol identity)
	Filename  string // original, pre-rename-for-duplicates name
	FinalName string // resolved name, set when the upload completes
	TotalSize int64
	ChunkSize int64
	NChunks   int64

	partPath string    // absolute path of the .part temp file
	file     *os.File  // open handle for WriteAt
	hasher   hash.Hash // running SHA-256 over committed bytes

	bits      []byte // bitmap, one bit per chunk index
	recvCount int64  // number of committed chunks (always a contiguous prefix)
	bytesRecv int64  // committed bytes, total

	state       UploadState
	lastChunkAt time.Time
	createdAt   time.Time
	errMsg      string
}

func newUpload(id string, filename string, totalSize, chunkSize int64, partPath string) (*Upload, error) {
	nChunks := (totalSize + chunkSize - 1) / chunkSize
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &Upload{
		ID:          id,
		Filename:    filename,
		TotalSize:   totalSize,
		ChunkSize:   chunkSize,
		NChunks:     nChunks,
		partPath:    partPath,
		file:        f,
		hasher:      sha256.New(),
		bits:        make([]byte, (nChunks+7)/8),
		state:       StateInProgress,
		lastChunkAt: now,
		createdAt:   now,
	}, nil
}

func (u *Upload) hasChunk(i int64) bool {
	return u.bits[i/8]&(1<<uint(i%8)) != 0
}

func (u *Upload) setChunk(i int64) {
	u.bits[i/8] |= 1 << uint(i%8)
}

// receivedIndices returns the chunk indices that have been committed, up to
// the given limit (used by /status to keep the payload bounded).
func (u *Upload) receivedIndices(limit int) []int64 {
	// See isComplete(): committed chunks are always a contiguous prefix of the
	// chunk space because the server only ever accepts the next in-order chunk.
	out := make([]int64, 0, limit)
	for i := int64(0); i < u.recvCount && int64(len(out)) < int64(limit); i++ {
		out = append(out, i)
	}
	return out
}

func (u *Upload) isComplete() bool {
	return u.recvCount == u.NChunks && u.bytesRecv == u.TotalSize
}

// committedPrefixLen is the number of received chunks; kept next to recvCount
// for clarity at call sites.
func (u *Upload) committedPrefixLen() int64 { return u.recvCount }

func (u *Upload) hashHex() string {
	return hex.EncodeToString(u.hasher.Sum(nil))
}

// stateString returns the current state without holding the lock past the read.
func (u *Upload) stateString() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return string(u.state)
}

func (u *Upload) closeFile() error {
	if u.file == nil {
		return nil
	}
	err := u.file.Close()
	u.file = nil
	return err
}

// removePart closes (if open) and deletes the .part file.
func (u *Upload) removePart() {
	_ = u.closeFile()
	if u.partPath != "" {
		_ = os.Remove(u.partPath)
	}
}