//go:build linux && cgo

package gst

/*
#include <malloc.h>
*/
import "C"

// TrimMalloc gives back to the system what glibc keeps free in its arenas (malloc_trim): GStreamer's
// streaming threads each get an arena, and what a closed pipeline freed stays there, counted in the RSS
// (ENGINE_GO.md §14). True if something was released.
func TrimMalloc() bool { return C.malloc_trim(0) == 1 }
