//go:build !linux

package runtime

// EnableSeccomp is a no-op on non-Linux platforms.
func EnableSeccomp() error { return nil }
