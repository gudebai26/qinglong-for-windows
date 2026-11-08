package auth

import "strings"

// Platform derives the session platform from the User-Agent exactly like
// getPlatform() in back/config/util.ts.
//
// Order matters: Android UAs contain "Linux" and iPad UAs may contain
// "Macintosh", so the mobile checks must not be reordered.
func Platform(userAgent string) string {
	ua := strings.ToLower(userAgent)

	system := "unknow"
	switch {
	case containsAny(ua, "windows", "win32", "win64", "wow32", "wow64"):
		system = "windows"
	case containsAny(ua, "macintosh", "macintel"):
		system = "macos"
	case strings.Contains(ua, "x11"):
		system = "linux"
	case containsAny(ua, "android", "adr"):
		system = "android"
	case containsAny(ua, "ios", "iphone", "ipad", "ipod", "iwatch"):
		system = "ios"
	case strings.Contains(ua, "openharmony"):
		system = "openharmony"
	}

	switch system {
	case "windows", "macos", "linux":
		return "desktop"
	case "android", "ios", "openharmony":
		return "mobile"
	}
	if strings.Contains(ua, "mobile") {
		return "mobile"
	}
	return "desktop"
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
