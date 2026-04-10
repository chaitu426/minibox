package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// RunInit is a tiny init process for PID 1 in the container.
// It forwards signals to the child and reaps zombies.
func RunInit() {
	if len(os.Args) < 4 || os.Args[2] != "--" {
		fmt.Fprintln(os.Stderr, "invalid init arguments")
		os.Exit(1)
	}
	cmdArgs := os.Args[3:]
	os.Exit(runInitCmd(cmdArgs, nil, 0, 0))
}

// runInitCmd runs the workload as PID1 logic (signal forwarding + zombie reaping).
// If env is nil, the current process environment is used.
func runInitCmd(cmdArgs []string, env []string, uid, gid int) int {
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "init: no command provided")
		return 127
	}

	// Ensure we use the container's PATH for LookPath
	if env != nil {
		for _, e := range env {
			if len(e) > 5 && e[:5] == "PATH=" {
				os.Setenv("PATH", e[5:])
				break
			}
		}
	}

	binary, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: command not found: %s (checked PATH=%s)\n", cmdArgs[0], os.Getenv("PATH"))
		return 127
	}

	cmd := exec.Command(binary, cmdArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if uid != 0 || gid != 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		}
		// Also set ambient capabilities if needed? No, Postgres just needs setuid.
	}

	// If we're running with a PTY, set it as the controlling terminal
	if fd := int(os.Stdin.Fd()); fd >= 0 {
		// TIOCSCTTY sets the controlling terminal. 0 means don't steal if already set.
		_ = unix.IoctlSetInt(fd, unix.TIOCSCTTY, 0)
	}

	if env != nil {
		cmd.Env = env
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "init: failed to start child: %v\n", err)
		return 1
	}

	sigCh := make(chan os.Signal, 16)
	signal.Notify(sigCh)
	defer signal.Stop(sigCh)
	go func() {
		for s := range sigCh {
			if sig, ok := s.(syscall.Signal); ok {
				_ = syscall.Kill(-cmd.Process.Pid, sig)
			}
		}
	}()

	// Reap any zombies while waiting for main child.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.ECHILD {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "init: wait error: %v\n", err)
			break
		}
		if pid == cmd.Process.Pid {
			if ws.Exited() {
				return ws.ExitStatus()
			}
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return 1
		}
	}
	return 0
}
