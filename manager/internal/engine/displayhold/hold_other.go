//go:build !(linux && cgo)

package displayhold

// Read: without cgo on linux there is no board display to keep lit.
func Read() Stats { return Stats{} }
