//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

// timeZoneInformation mirrors TIME_ZONE_INFORMATION. The name fields are
// localized, which is exactly what Node prints in Date.prototype.toString()
// ("Sat Nov 07 2026 15:01:09 GMT+0800 (中国标准时间)").
type timeZoneInformation struct {
	Bias         int32
	StandardName [32]uint16
	StandardDate syscall.Systemtime
	StandardBias int32
	DaylightName [32]uint16
	DaylightDate syscall.Systemtime
	DaylightBias int32
}

var (
	kernel32Proc                      = syscall.NewLazyDLL("kernel32.dll")
	getTimeZoneInformationProc        = kernel32Proc.NewProc("GetTimeZoneInformation")
	timeZoneIDDaylight         uint32 = 2
)

func utf16ToString(values []uint16) string {
	length := 0
	for length < len(values) && values[length] != 0 {
		length++
	}
	return syscall.UTF16ToString(values[:length])
}

// LocalTimeZoneName returns the localized name of the current Windows time
// zone, or "" when it cannot be read.
func LocalTimeZoneName() string {
	var info timeZoneInformation
	result, _, _ := getTimeZoneInformationProc.Call(uintptr(unsafe.Pointer(&info)))
	if result == uintptr(^uint32(0)) {
		return ""
	}
	if uint32(result) == timeZoneIDDaylight {
		if name := utf16ToString(info.DaylightName[:]); name != "" {
			return name
		}
	}
	return utf16ToString(info.StandardName[:])
}

// SetSystemTimezone is a no-op on Windows: the zone is a system setting the
// panel only records. Node returns true without touching the OS.
func SetSystemTimezone(timezone string) bool { return true }
