package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/models"
	"github.com/chaitu426/minibox/internal/security"
	"golang.org/x/sys/unix"
)

// envChildMountNS is set by the daemon when spawning the child with CLONE_NEWNS. A second
// unshare(CLONE_NEWNS) in that child often fails with EPERM; we only unshare when this is
// unset (e.g. bare `minibox child` for defense in depth).
const envChildMountNS = "MINIBOX_CHILD_NEWNS"

func RunContainer() {
	if len(os.Args) < 8 {
		fmt.Fprintln(os.Stderr, "Invalid arguments to child process")
		os.Exit(1)
	}

	containerID := os.Args[2]
	if !security.ValidContainerID(containerID) {
		fmt.Fprintln(os.Stderr, "refusing to run: invalid container id")
		os.Exit(1)
	}

	// Mount namespace: parent already uses CLONE_NEWNS (see process.go). Only unshare again
	// when spawned without that flag (direct `child` invocation) so host mounts stay safe.
	if os.Getenv(envChildMountNS) != "1" {
		if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
			fmt.Fprintf(os.Stderr, "refusing to run: could not unshare mount namespace: %v\n", err)
			os.Exit(1)
		}
	}

	memMB := os.Args[4]
	cpu := os.Args[5]
	configJSON := os.Args[6]
	layersJSON := os.Args[7]
	cmdArgs := os.Args[8:]
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "no command provided")
		os.Exit(1)
	}

	var imgConfig models.OCIConfig
	if err := json.Unmarshal([]byte(configJSON), &imgConfig); err != nil {
		fmt.Printf("Warning: failed to unmarshal OCI config: %v\n", err)
	}

	var lowerDirs []string
	if err := json.Unmarshal([]byte(layersJSON), &lowerDirs); err != nil {
		fmt.Printf("Warning: failed to unmarshal layer info: %v\n", err)
	}

	// Cgroups V2 limits deployment
	cgPath := filepath.Join("/sys/fs/cgroup/minibox", containerID)
	os.MkdirAll(cgPath, 0755)

	if memMB != "0" {
		memBytes, _ := strconv.Atoi(memMB)
		memoryMax := strconv.Itoa(memBytes * 1024 * 1024)
		os.WriteFile(filepath.Join(cgPath, "memory.max"), []byte(memoryMax), 0700)
	}

	if cpu != "0" {
		os.WriteFile(filepath.Join(cgPath, "cpu.max"), []byte(cpu), 0700)
	}

	pidStr := strconv.Itoa(os.Getpid())
	os.WriteFile(filepath.Join(cgPath, "cgroup.procs"), []byte(pidStr), 0700)

	syscall.Sethostname([]byte(containerID))

	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	rootfs := filepath.Join(containerPath, "rootfs")

	// Rootfs isolation in child:
	// - Overlay mount for this container was already created by the daemon (MountRootfs).
	// - In rootless mode, additional mounts (bind/pivot_root) inside the user namespace
	//   frequently fail with EPERM. Instead, chroot into the prepared rootfs. Combined
	//   with namespaces, caps drop, and seccomp this is still strong isolation and
	//   avoids fragile mount patterns on restrictive kernels.
	if err := os.Chdir(rootfs); err != nil {
		fmt.Fprintf(os.Stderr, "Error changing directory to rootfs (%s): %v\n", rootfs, err)
		os.Exit(1)
	}
	if err := syscall.Chroot("."); err != nil {
		fmt.Fprintf(os.Stderr, "Error chrooting into rootfs (%s): %v\n", rootfs, err)
		os.Exit(1)
	}
	if err := os.Chdir("/"); err != nil {
		fmt.Fprintf(os.Stderr, "Error changing directory to / inside chroot: %v\n", err)
		os.Exit(1)
	}

	os.MkdirAll("/proc", 0755)
	procFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("proc", "/proc", "proc", procFlags, ""); err != nil {
		fmt.Printf("Warning: /proc mount failed: %v\n", err)
	}

	os.MkdirAll("/dev", 0755)
	devFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if err := unix.Mount("tmpfs", "/dev", "tmpfs", devFlags, ""); err != nil {
		fmt.Printf("Warning: /dev tmpfs mount failed: %v\n", err)
	}

	devices := []string{"null", "zero", "random", "urandom", "full", "tty"}
	for _, dev := range devices {
		devPath := filepath.Join("/dev", dev)
		os.OpenFile(devPath, os.O_CREATE|os.O_RDWR, 0666)
	}

	if imgConfig.Config.WorkingDir != "" {
		if err := os.Chdir(imgConfig.Config.WorkingDir); err != nil {
			fmt.Printf("Warning: failed to change workdir to %s: %v\n", imgConfig.Config.WorkingDir, err)
		}
	}

	env := os.Environ()
	env = append(env, imgConfig.Config.Env...)

	dropContainerCapabilities()
	setContainerRLimits()
	if err := EnableSeccomp(); err != nil {
		fmt.Println("Warning: Failed to enable seccomp:", err)
	}

	os.Exit(runInitCmd(cmdArgs, env))
}
