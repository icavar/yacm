// Package wintoast is a no-op stand-in; see ../README.md.
package wintoast

// IClassFactory mirrors the upstream COM type.
type IClassFactory struct{}

// ClassFactory mirrors the upstream variable.
var ClassFactory = &IClassFactory{}

// registerClassFactory is referenced by Wails via go:linkname.
func registerClassFactory(*IClassFactory) error { return nil }

var _ = registerClassFactory
