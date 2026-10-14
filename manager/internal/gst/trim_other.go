//go:build !(linux && cgo)

package gst

// TrimMalloc is a no-op without glibc.
func TrimMalloc() bool { return false }
