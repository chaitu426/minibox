//go:build linux

package runtime

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	bpfLD    = 0x00
	bpfW     = 0x00
	bpfABS   = 0x20
	bpfJMP   = 0x05
	bpfJEQ   = 0x15
	bpfK     = 0x00
	bpfRET   = 0x06
	secErr1  = 0x00050000 | 1 // SECCOMP_RET_ERRNO | EPERM
	secAllow = 0x7fff0000     // SECCOMP_RET_ALLOW
)

func bpfStmt(code uint16, k uint32) syscall.SockFilter {
	return syscall.SockFilter{Code: code, K: k}
}

func bpfJump(code uint16, k uint32, jt, jf uint8) syscall.SockFilter {
	return syscall.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// deniedSyscalls returns a Docker-inspired deny list for the container workload (after chroot).
// This is a DENYLIST — everything not listed is allowed. The list covers:
//   - Kernel module / kexec loading
//   - Namespace escapes (unshare, setns, clone3)
//   - Mount API (both legacy and new fsopen/fsmount/move_mount)
//   - Cross-process memory access without ptrace (process_vm_*)
//   - Kernel key management (add_key, keyctl)
//   - eBPF and perf (frequent privilege-escalation targets)
//   - io_uring (historically exploitable: CVE-2022-29582, CVE-2023-2598, etc.)
//   - NUMA memory control (mbind, migrate_pages, move_pages)
func deniedSyscalls() []uint32 {
	return []uint32{
		//Kernel / boot
		uint32(unix.SYS_REBOOT),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_KEXEC_FILE_LOAD),

		//Kernel modules
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_CREATE_MODULE),
		uint32(unix.SYS_QUERY_MODULE),
		uint32(unix.SYS_GET_KERNEL_SYMS),

		//Namespace escapes
		// unshare: container can create new user ns → gain caps → escape.
		uint32(unix.SYS_UNSHARE),
		// setns: attach process to an existing (host) namespace.
		uint32(unix.SYS_SETNS),
		// clone3: newer clone API; CLONE_NEWUSER flag allows same escape as unshare.
		uint32(unix.SYS_CLONE3),

		//Mount (legacy + new kernel mount API)
		// Blocking only SYS_MOUNT leaves fsopen/fsmount/move_mount as bypasses.
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_MOUNT_SETATTR),
		uint32(unix.SYS_FSOPEN),       // new mount API step 1
		uint32(unix.SYS_FSMOUNT),      // new mount API step 2
		uint32(unix.SYS_MOVE_MOUNT),   // new mount API step 3
		uint32(unix.SYS_FSPICK),       // reconfigure existing mount
		uint32(unix.SYS_OPEN_TREE),    // clone/move mount trees

		//Cross-process memory access
		// process_vm_* lets a process r/w another's memory without ptrace.
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		// kcmp: compares kernel resources (fds, mm) across processes.
		uint32(unix.SYS_KCMP),
		// ptrace: classic attach-and-read attack.
		uint32(unix.SYS_PTRACE),
		// name_to_handle_at + open_by_handle_at: raw inode handle → host fs escape.
		uint32(unix.SYS_NAME_TO_HANDLE_AT),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),

		//eBPF / perf
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_PERF_EVENT_OPEN),

		//io_uring (multiple critical CVEs)
		uint32(unix.SYS_IO_URING_SETUP),
		uint32(unix.SYS_IO_URING_ENTER),
		uint32(unix.SYS_IO_URING_REGISTER),

		//Kernel key management
		uint32(unix.SYS_ADD_KEY),
		uint32(unix.SYS_REQUEST_KEY),
		uint32(unix.SYS_KEYCTL),

		// NUMA / memory policy
		uint32(unix.SYS_MBIND),
		uint32(unix.SYS_MIGRATE_PAGES),
		uint32(unix.SYS_MOVE_PAGES),

		//Swap / accounting
		uint32(unix.SYS_SWAPON),
		uint32(unix.SYS_SWAPOFF),
		uint32(unix.SYS_ACCT),

		//Misc privileged interfaces 
		uint32(unix.SYS_SYSFS),
		uint32(unix.SYS_NFSSERVCTL),
		uint32(unix.SYS_IOPL),
		uint32(unix.SYS_IOPERM),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_CLOCK_ADJTIME),
		uint32(unix.SYS_CLOCK_SETTIME),
		uint32(unix.SYS_LOOKUP_DCOOKIE),
		uint32(unix.SYS_VHANGUP),
		uint32(unix.SYS_SETHOSTNAME),
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
