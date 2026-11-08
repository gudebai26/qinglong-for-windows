//go:build windows

package platform

import (
	"unsafe"
)

// memoryStatusEx mirrors MEMORYSTATUSEX from sysinfoapi.h.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// kernel32Proc and the timezone helpers live in timezone_windows.go; the DLL
// handle is shared so the process only loads it once.
var globalMemoryStatusProc = kernel32Proc.NewProc("GlobalMemoryStatusEx")

// Memory returns total and free physical memory in bytes, matching
// os.totalmem()/os.freemem() on Windows.
func Memory() (total, free uint64) {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	result, _, _ := globalMemoryStatusProc.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return 0, 0
	}
	return status.TotalPhys, status.AvailPhys
}

// LoadAvg reports the 1/5/15 minute load average. Windows has no such metric
// and Node's os.loadavg() returns zeros there.
func LoadAvg() [3]float64 { return [3]float64{} }
