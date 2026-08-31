package server

import (
	"os"
	"path/filepath"
)

// checkWritable verifies the destination folder accepts a real read/write/delete
// cycle up front, so permission problems are caught before the phone connects.
func checkWritable(dir string) error {
	p := filepath.Join(dir, ".quickdrop-write-test")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("test")); err != nil {
		_ = f.Close()
		_ = os.Remove(p)
		return err
	}
	_ = f.Close()
	_ = os.Remove(p)
	return nil
}