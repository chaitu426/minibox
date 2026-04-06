//go:build linux

package runtime

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	bpfLD   = 0x00
	bpfW    = 0x00
	bpfABS  = 0x20
	bpfJMP  = 0x05
	bpfJEQ  = 0x15
	bpfK    = 0x00
	bpfRET  = 0x06
	secErr1 = 0x00050000 | 1 // SECCOMP_RET_ERRNO | EPERM
	secAllow = 0x7fff0000     // SECCOMP_RET_ALLOW
)

func bpfStmt(code uint16, k uint32) syscall.SockFilter {
	return syscall.SockFilter{Code: code, K: k}
}

func bpfJump(code uint16, k uint32, jt, jf uint8) syscall.SockFilter {
	return syscall.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// deniedSyscalls returns a Docker-inspired deny list for the container workload (after pivot_root).
func deniedSyscalls() []uint32 {
	return []uint32{
		uint32(unix.SYS_REBOOT),
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_KEXEC_FILE_LOAD),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_CREATE_MODULE),
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_SWAPON),
		uint32(unix.SYS_SWAPOFF),
		uint32(unix.SYS_SYSFS),
		uint32(unix.SYS_QUERY_MODULE),
		uint32(unix.SYS_GET_KERNEL_SYMS),
		uint32(unix.SYS_ACCT),
		uint32(unix.SYS_NFSSERVCTL),
		uint32(unix.SYS_IOPL),
		uint32(unix.SYS_IOPERM),
		uint32(unix.SYS_ADD_KEY),
		uint32(unix.SYS_REQUEST_KEY),
		uint32(unix.SYS_KEYCTL),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_CLOCK_ADJTIME),
		uint32(unix.SYS_CLOCK_SETTIME),
		uint32(unix.SYS_LOOKUP_DCOOKIE),
		uint32(unix.SYS_VHANGUP),
		uint32(unix.SYS_SETHOSTNAME),
		uint32(unix.SYS_MOUNT_SETATTR),
	}
}

func buildSeccompFilter() []syscall.SockFilter {
	denied := deniedSyscalls()
	f := make([]syscall.SockFilter, 0, 2+len(denied)*2+1)
	// seccomp_data.nr at offset 0 (see kernel struct seccomp_data).
	f = append(f, bpfStmt(bpfLD|bpfW|bpfABS, 0))
	for _, nr := range denied {
		f = append(f, bpfJump(bpfJMP|bpfJEQ|bpfK, nr, 0, 1))
		f = append(f, bpfStmt(bpfRET|bpfK, secErr1))
	}
	f = append(f, bpfStmt(bpfRET|bpfK, secAllow))
	return f
}

// EnableSeccomp installs PR_SET_NO_NEW_PRIVS and a seccomp-bpf filter that denies
// privileged kernel interfaces (subset of Docker's default profile).
func EnableSeccomp() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %w", err)
	}

	filter := buildSeccompFilter()
	prog := syscall.SockFprog{
		Len:    uint16(len(filter)),
		Filter: &filter[0],
	}

	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return fmt.Errorf("seccomp filter: %w", err)
	}
	return nil
}
