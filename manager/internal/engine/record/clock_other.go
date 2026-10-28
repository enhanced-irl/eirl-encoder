//go:build !linux

package record

// clockSynced elsewhere (the tests on the Mac): the system keeps its own time.
func clockSynced() bool { return true }
