//go:build !windows

package service

import "os"

// fileBirthTime falls back to the modification time: Go's portable Stat does
// not expose a birth time and Node's own birthtime is unreliable on Linux.
func fileBirthTime(info os.FileInfo) int64 {
	return info.ModTime().UnixMilli()
}
