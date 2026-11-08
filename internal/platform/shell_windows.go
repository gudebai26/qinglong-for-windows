//go:build windows

package platform

import "golang.org/x/sys/windows/registry"

// gitInstallRoots returns the install directories Git for Windows recorded.
//
// This is the only reliable way to find a copy installed outside PATH and the
// default folders (D:\Tools\Git, a portable extraction, a per-user install).
func gitInstallRoots() []string {
	var roots []string
	for _, key := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\GitForWindows`},
		{registry.CURRENT_USER, `SOFTWARE\GitForWindows`},
	} {
		k, err := registry.OpenKey(key.root, key.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if value, _, err := k.GetStringValue("InstallPath"); err == nil && value != "" {
			roots = append(roots, value)
		}
		_ = k.Close()
	}
	return roots
}
