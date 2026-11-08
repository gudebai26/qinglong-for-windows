//go:build !windows && !linux

package platform

// Memory is unavailable on this platform; the dashboard reports zeros the way
// Node does when os.totalmem() returns 0.
func Memory() (total, free uint64) { return 0, 0 }

// LoadAvg has no portable source outside Linux and Windows.
func LoadAvg() [3]float64 { return [3]float64{} }
