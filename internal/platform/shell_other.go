//go:build !windows

package platform

// gitInstallRoots is empty outside Windows: bash comes from the system PATH.
func gitInstallRoots() []string { return nil }
