//go:build windows

package service

import (
	"os"
	"syscall"
)

// fileBirthTime returns the creation time in milliseconds since the epoch.
// Node's fs.Stats.birthtime is the Windows creation timestamp; the panel sorts
// directory listings by it, so the value has to come from the same field.
func fileBirthTime(info os.FileInfo) int64 {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.CreationTime.Nanoseconds() / 1e6
	}
	return info.ModTime().UnixMilli()
}
