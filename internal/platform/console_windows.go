//go:build windows

package platform

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
)

const (
	codePageUTF8          = 65001
	enableVirtualTerminal = 0x0004
)

// EnableUTF8Console switches the console output code page to UTF-8 and turns on
// ANSI escape processing, returning a function that restores the previous
// state plus the code page that was active before the switch.
//
// cmd.exe starts in the OEM code page (936 on Chinese Windows) while Go always
// writes UTF-8 bytes. Without this switch every Chinese log line is decoded as
// GBK and shows up as mojibake. The restore function matters because
// start.bat prints GBK text after the panel exits.
func EnableUTF8Console() (func(), uint32) {
	previous, _, _ := procGetConsoleOutputCP.Call()
	_, _, _ = procSetConsoleOutputCP.Call(codePageUTF8)

	restoreMode := func() {}
	if handle, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil {
		var mode uint32
		if r, _, _ := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode))); r != 0 {
			if mode&enableVirtualTerminal == 0 {
				_, _, _ = procSetConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminal))
				restoreMode = func() {
					_, _, _ = procSetConsoleMode.Call(uintptr(handle), uintptr(mode))
				}
			}
		}
	}

	return func() {
		_, _, _ = procSetConsoleOutputCP.Call(previous)
		restoreMode()
	}, uint32(previous)
}

// ConsoleCodePage reports the console output code page currently in effect.
// The boot log prints it after the switch so a console that refused UTF-8 is
// visible instead of showing up later as mojibake.
func ConsoleCodePage() uint32 {
	cp, _, _ := procGetConsoleOutputCP.Call()
	return uint32(cp)
}

// ConsoleSupportsEmoji reports whether the console can render the emoji the
// container image uses.
//
// Traditional conhost cannot: its font (Consolas, 点阵) ships no emoji glyphs,
// so 🚀 comes out as "?" or an empty box no matter which code page is active.
// Windows Terminal sets WT_SESSION and falls back to Segoe UI Emoji, so it can.
// Setting QL_LOG_ASCII forces the ASCII rendering either way.
func ConsoleSupportsEmoji() bool {
	switch os.Getenv("QL_LOG_ASCII") {
	case "1":
		return false
	case "0":
		return true
	}
	return os.Getenv("WT_SESSION") != ""
}
