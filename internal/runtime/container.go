package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if len(os.Args) < 10 {
		fmt.Fprintf(os.Stderr, "[ERROR] Invalid arguments to child process: got %d, want at least 10\n", len(os.Args))
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

	optsJSON := os.Args[4]
	configJSON := os.Args[5]
	layersJSON := os.Args[6]
	volumesJSON := os.Args[7]
	userEnvJSON := os.Args[8]
	cmdArgs := os.Args[9:]
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "no command provided")
		os.Exit(1)
	}

	var opts ContainerOptions
	if err := json.Unmarshal([]byte(optsJSON), &opts); err != nil {
		fmt.Printf("Warning: failed to unmarshal ContainerOptions: %v\n", err)
	}

	var imgConfig models.OCIConfig
	if err := json.Unmarshal([]byte(configJSON), &imgConfig); err != nil {
		fmt.Printf("Warning: failed to unmarshal OCI config: %v\n", err)
	}

	var lowerDirs []string
	if err := json.Unmarshal([]byte(layersJSON), &lowerDirs); err != nil {
		fmt.Printf("Warning: failed to unmarshal layer info: %v\n", err)
	}

	var volumes map[string]string
	if err := json.Unmarshal([]byte(volumesJSON), &volumes); err != nil {
		fmt.Printf("Warning: failed to unmarshal volume info: %v\n", err)
	}

	var userEnv []string
	if err := json.Unmarshal([]byte(userEnvJSON), &userEnv); err != nil {
		fmt.Printf("Warning: failed to unmarshal user env info: %v\n", err)
	}

	// Cgroups V2 limits deployment
	cgPath := filepath.Join("/sys/fs/cgroup/minibox", containerID)
	os.MkdirAll(cgPath, 0755)

	if opts.MemoryMB != 0 {
		memoryMax := strconv.Itoa(opts.MemoryMB * 1024 * 1024)
		os.WriteFile(filepath.Join(cgPath, "memory.max"), []byte(memoryMax), 0700)
	}

	if opts.CPUMax != 0 {
		// cpu.max format: $QUOTA $PERIOD (e.g. "100000 100000" for 1 core)
		os.WriteFile(filepath.Join(cgPath, "cpu.max"), []byte(strconv.Itoa(opts.CPUMax)), 0700)
	}

	if opts.CPUSet != "" {
		os.WriteFile(filepath.Join(cgPath, "cpuset.cpus"), []byte(opts.CPUSet), 0700)
	}

	if opts.IOWeight != 0 {
		// io.weight format: $WEIGHT (1-1000, default 100)
		os.WriteFile(filepath.Join(cgPath, "io.weight"), []byte(strconv.Itoa(opts.IOWeight)), 0700)
	}

	pidStr := strconv.Itoa(os.Getpid())
	os.WriteFile(filepath.Join(cgPath, "cgroup.procs"), []byte(pidStr), 0700)

	syscall.Sethostname([]byte(containerID))

	setOOMScoreAdj(opts.OOMScoreAdj)

	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	rootfs := filepath.Join(containerPath, "rootfs")

	// Bind mount user-defined volumes into the rootfs before chrooting
	for hostPath, targetContainerPath := range volumes {
		targetPath := filepath.Join(rootfs, targetContainerPath)
		os.MkdirAll(hostPath, 0755)
		os.MkdirAll(targetPath, 0755)
		if err := unix.Mount(hostPath, targetPath, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			fmt.Printf("Warning: failed to bind mount %s to %s: %v\n", hostPath, targetContainerPath, err)
		}
	}

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

	os.MkdirAll("/sys", 0755)
	sysFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_RDONLY)
	if err := unix.Mount("sysfs", "/sys", "sysfs", sysFlags, ""); err != nil {
		fmt.Printf("Warning: /sys mount failed: %v\n", err)
	}

	os.MkdirAll("/dev", 0755)
	devFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if err := unix.Mount("tmpfs", "/dev", "tmpfs", devFlags, ""); err != nil {
		fmt.Printf("Warning: /dev tmpfs mount failed: %v\n", err)
	}

	os.MkdirAll("/dev/shm", 01777)
	shmFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_STRICTATIME)
	if err := unix.Mount("tmpfs", "/dev/shm", "tmpfs", shmFlags, "mode=1777,size=65536k"); err != nil {
		fmt.Printf("Warning: /dev/shm mount failed: %v\n", err)
	}
	os.Chmod("/dev/shm", 01777)

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

	// Sanitize environment: start with image-defined variables and add sane defaults
	// to avoid leaking host variables like HOME or USER which cause permission issues.
	var env []string
	env = append(env, imgConfig.Config.Env...)
	env = append(env, userEnv...)

	hasHome, hasPath, hasUser, hasTerm := false, false, false, false
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			hasHome = true
		}
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
		if strings.HasPrefix(e, "USER=") {
			hasUser = true
		}
		if strings.HasPrefix(e, "TERM=") {
			hasTerm = true
		}
	}

	if !hasHome {
		env = append(env, "HOME=/root")
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if !hasUser {
		env = append(env, "USER=root")
	}
	if !hasTerm {
		if t := os.Getenv("TERM"); t != "" {
			env = append(env, "TERM="+t)
		} else {
			env = append(env, "TERM=xterm")
		}
	}
	env = append(env, "HOSTNAME="+containerID)

	dropContainerCapabilities()
	setContainerRLimits()
	if err := EnableSeccomp(); err != nil {
		fmt.Println("Warning: Failed to enable seccomp:", err)
	}

	applySysctls(opts.Sysctls)

	os.Exit(runInitCmd(cmdArgs, env))
}

func setOOMScoreAdj(adj int) {
	if adj == 0 {
		return
	}
	// OOM score adj range is -1000 to 1000
	path := "/proc/self/oom_score_adj"
	_ = os.WriteFile(path, []byte(strconv.Itoa(adj)), 0644)
}

func applySysctls(sysctls map[string]string) {
	for key, value := range sysctls {
		path := filepath.Join("/proc/sys", strings.ReplaceAll(key, ".", "/"))
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			fmt.Printf("Warning: failed to set sysctl %s=%s: %v\n", key, value, err)
		}
	}
}
