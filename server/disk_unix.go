//go:build unix

package server

import "golang.org/x/sys/unix"

// freeDiskBytes reports the bytes available to the caller on the volume
// containing dir.
func freeDiskBytes(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}