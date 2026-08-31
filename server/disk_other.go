//go:build !windows && !unix

package server

// freeDiskBytes is a best-effort fallback for exotic platforms: no pre-check,
// write failures surface at upload time.
func freeDiskBytes(dir string) (int64, error) {
	return 1 << 62, nil
}