//go:build linux

package runtime

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// dropContainerCapabilities clears ambient caps and drops every capability from the bounding set.
// Best-effort: failures are ignored (older kernels, user-namespace quirks).
func dropContainerCapabilities() {
	_ = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0)

	last := 40
	if b, err := os.ReadFile("/proc/sys/kernel/cap_last_cap"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= 0 {
			last = n
		}
	}
	for c := 0; c <= last; c++ {
		_ = unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0)
	}
}

// setContainerRLimits applies conservative rlimits similar to typical container defaults.
func setContainerRLimits() {
	_ = unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
	_ = unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: 1024, Max: 1024})
	_ = unix.Setrlimit(unix.RLIMIT_NPROC, &unix.Rlimit{Cur: 4096, Max: 4096})
}
