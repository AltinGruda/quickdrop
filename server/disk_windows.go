//go:build windows

package server

import "golang.org/x/sys/windows"

// freeDiskBytes reports the bytes available to the caller on the volume
// containing dir.
func freeDiskBytes(dir string) (int64, error) {
	ptr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}