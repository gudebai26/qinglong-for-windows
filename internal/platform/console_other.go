//go:build !windows

package platform

import "os"

// EnableUTF8Console is a no-op outside Windows: terminals there are UTF-8 by
// default and Go writes UTF-8 bytes directly.
func EnableUTF8Console() (func(), uint32) { return func() {}, 0 }

// ConsoleCodePage reports the console output code page. Unix terminals are
// UTF-8 already, so the constant is what the boot log should show.
func ConsoleCodePage() uint32 { return 65001 }

// ConsoleSupportsEmoji is true on Unix terminals, which ship emoji fonts.
// QL_LOG_ASCII still allows forcing the plain rendering.
func ConsoleSupportsEmoji() bool {
	return os.Getenv("QL_LOG_ASCII") != "1"
}
