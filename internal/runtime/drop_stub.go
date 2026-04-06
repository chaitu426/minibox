//go:build !linux

package runtime

func dropContainerCapabilities() {}

func setContainerRLimits() {}
