package runtime

import (
	"bufio"
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

// envChildMountNS is for checking CLONE_NEWNS.
// Child will unshare if flag is missing.
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
		fmt.Printf("[warn] unmarshal container options: %v\n", err)
	}

	var imgConfig models.OCIConfig
	if err := json.Unmarshal([]byte(configJSON), &imgConfig); err != nil {
		fmt.Printf("[warn] unmarshal OCI config: %v\n", err)
	}

	var lowerDirs []string
	if err := json.Unmarshal([]byte(layersJSON), &lowerDirs); err != nil {
		fmt.Printf("[warn] unmarshal layer info: %v\n", err)
	}

	var volumes map[string]string
	if err := json.Unmarshal([]byte(volumesJSON), &volumes); err != nil {
		fmt.Printf("[warn] unmarshal volume info: %v\n", err)
	}

	var userEnv []string
	if err := json.Unmarshal([]byte(userEnvJSON), &userEnv); err != nil {
		fmt.Printf("[warn] unmarshal user env: %v\n", err)
	}

	// Cgroups V2 limits deployment
	cgPath := filepath.Join("/sys/fs/cgroup/minibox", containerID)
	os.MkdirAll(cgPath, 0755)

	// Enable controllers on the parent cgroup so limits on this child cgroup
	// are actually enforced. Without this, cpu/memory/io limits silently fail.
	parentCtrl := filepath.Join("/sys/fs/cgroup/minibox", "cgroup.subtree_control")
	os.WriteFile(parentCtrl, []byte("+cpu +memory +io"), 0700)

	if opts.MemoryMB != 0 {
		memoryMax := strconv.Itoa(opts.MemoryMB * 1024 * 1024)
		os.WriteFile(filepath.Join(cgPath, "memory.max"), []byte(memoryMax), 0700)
	}

	if opts.CPUMax != 0 {
		// cpu.max format: "$QUOTA $PERIOD"
		// opts.CPUMax is treated as a percentage of one core (1–100).
		// period is fixed at 100000 µs; quota scales linearly.
		const period = 100000
		quota := opts.CPUMax * period / 100
		cpuMax := fmt.Sprintf("%d %d", quota, period)
		os.WriteFile(filepath.Join(cgPath, "cpu.max"), []byte(cpuMax), 0700)
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

	hostname := containerID
	if opts.Hostname != "" {
		hostname = opts.Hostname
	}
	syscall.Sethostname([]byte(hostname))

	setOOMScoreAdj(opts.OOMScoreAdj)

	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	rootfs := filepath.Join(containerPath, "rootfs")

	// Bind mount user-defined volumes into the rootfs before chrooting
	for hostPath, targetContainerPath := range volumes {
		targetPath := filepath.Join(rootfs, targetContainerPath)
		os.MkdirAll(hostPath, 0755)
		os.MkdirAll(targetPath, 0755)
		if err := unix.Mount(hostPath, targetPath, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			fmt.Printf("[warn] bind mount host=%s target=%s err=%v\n", hostPath, targetContainerPath, err)
		}
	}

	// Apan chroot vapartoy karan pivot_root ne host OS udala hota. Ata sathi asach thivu bhau.
	// It's still strong isolation when combined with caps and seccomp.
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
	// hidepid=2: hides other processes' /proc/<pid> entries from the container.
	// Without this, any process inside can read /proc/<pid>/environ, cmdline,
	// fd/ etc. of every other process — a common container info-leak vector.
	if err := unix.Mount("proc", "/proc", "proc", procFlags, "hidepid=2"); err != nil {
		// hidepid=2 requires a PID namespace; fall back gracefully if not available.
		if err2 := unix.Mount("proc", "/proc", "proc", procFlags, ""); err2 != nil {
			fmt.Printf("[warn] mount /proc: %v\n", err2)
		} else {
			fmt.Printf("[warn] mount /proc hidepid=2 failed (%v), mounted without hidepid\n", err)
		}
	}

	os.MkdirAll("/sys", 0755)
	// Mount sysfs. Read-only remount kela pahije, nahi tar jhol hoto.
	sysFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("sysfs", "/sys", "sysfs", sysFlags, ""); err != nil {
		fmt.Printf("[warn] mount /sys: %v\n", err)
	}
	sysRemountFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_RDONLY | unix.MS_REMOUNT)
	if err := unix.Mount("", "/sys", "", sysRemountFlags, ""); err != nil {
		fmt.Printf("[warn] remount /sys read-only: %v\n", err)
	}

	os.MkdirAll("/dev", 0755)
	devFlags := uintptr(unix.MS_NOSUID)
	if err := unix.Mount("tmpfs", "/dev", "tmpfs", devFlags, ""); err != nil {
		fmt.Printf("[warn] mount /dev tmpfs: %v\n", err)
	}

	// /dev/shm — shared memory required by Postgres (shared_buffers) and other DBs.
	// Default to 256 MB; opts.ShmSize (in MB) overrides when set.
	shmSizeMB := 256
	if opts.ShmSize > 0 {
		shmSizeMB = opts.ShmSize
	}
	os.MkdirAll("/dev/shm", 01777)
	shmFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_STRICTATIME)
	shmOpts := fmt.Sprintf("mode=1777,size=%dm", shmSizeMB)
	if err := unix.Mount("tmpfs", "/dev/shm", "tmpfs", shmFlags, shmOpts); err != nil {
		fmt.Printf("[warn] mount /dev/shm: %v\n", err)
	}
	os.Chmod("/dev/shm", 01777)

	// /dev/pts — pseudo-terminal slave devices; required by Postgres initdb and many DB tools.
	os.MkdirAll("/dev/pts", 0755)
	ptsFlags := uintptr(unix.MS_NOSUID | unix.MS_NOEXEC)
	if err := unix.Mount("devpts", "/dev/pts", "devpts", ptsFlags, "newinstance,ptmxmode=0666,mode=620"); err != nil {
		fmt.Printf("[warn] mount /dev/pts: %v\n", err)
	}

	// /tmp — general-purpose temp dir used widely by Postgres, MongoDB, and Redis.
	os.MkdirAll("/tmp", 01777)
	tmpFlags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_STRICTATIME)
	if err := unix.Mount("tmpfs", "/tmp", "tmpfs", tmpFlags, "mode=1777"); err != nil {
		fmt.Printf("[warn] mount /tmp: %v\n", err)
	}
	os.Chmod("/tmp", 01777)

	// Create real device nodes. Many programs (including postgres initdb, mongod, redis) rely on
	// /dev/null being a real character device, not a regular file.
	type devNode struct {
		name  string
		major uint32
		minor uint32
		mode  uint32
	}
	nodes := []devNode{
		{"null", 1, 3, 0666},
		{"zero", 1, 5, 0666},
		{"random", 1, 8, 0666},
		{"urandom", 1, 9, 0666},
		{"full", 1, 7, 0666},
		{"tty", 5, 0, 0666},
		{"console", 5, 1, 0600}, // console device used by some init scripts
		{"ptmx", 5, 2, 0666},    // PTY master — needed by Postgres / OpenSSH inside container
	}
	for _, n := range nodes {
		p := filepath.Join("/dev", n.name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		dev := int(unix.Mkdev(n.major, n.minor))
		if err := unix.Mknod(p, unix.S_IFCHR|n.mode, dev); err != nil {
			// Best-effort; older kernels/userns may deny mknod.
			// Fallback to bind-mounting from host.
			hostPath := filepath.Join("/dev", n.name)
			if f, createErr := os.Create(p); createErr == nil {
				f.Close()
				if bindErr := unix.Mount(hostPath, p, "", unix.MS_BIND, ""); bindErr != nil {
					fmt.Printf("[warn] create /dev/%s - mknod failed: %v, bind mount failed: %v\n", n.name, err, bindErr)
				}
			} else {
				fmt.Printf("[warn] create /dev/%s: %v\n", n.name, err)
			}
		} else {
			// Enforce permissions, ignoring host umask.
			os.Chmod(p, os.FileMode(n.mode))
		}
	}

	// /dev/stdin, /dev/stdout, /dev/stderr — symlinks expected by many scripts
	for _, link := range []struct{ name, target string }{
		{"stdin", "/proc/self/fd/0"},
		{"stdout", "/proc/self/fd/1"},
		{"stderr", "/proc/self/fd/2"},
		{"fd", "/proc/self/fd"},
	} {
		p := filepath.Join("/dev", link.name)
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			_ = os.Symlink(link.target, p)
		}
	}

	if imgConfig.Config.WorkingDir != "" {
		if err := os.Chdir(imgConfig.Config.WorkingDir); err != nil {
			fmt.Printf("[warn] chdir workdir=%s err=%v\n", imgConfig.Config.WorkingDir, err)
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

	// DB containers often need to initialize/chown their data directory on first boot.
	// In DB mode we keep capabilities so the entrypoint can create directories and set ownership.
	if !opts.DBMode {
		dropContainerCapabilities()
	}
	setContainerRLimits()
	if err := EnableSeccomp(); err != nil {
		fmt.Printf("[warn] seccomp: %v\n", err)
	}

	applySysctls(opts.Sysctls)

	// Resolve the user to run as. If not specified by CLI, use the image default.
	userStr := opts.User
	if userStr == "" {
		userStr = imgConfig.Config.User
	}
	uid, gid, _ := resolveUser(userStr)

	os.Exit(runInitCmd(cmdArgs, env, uid, gid))
}

// resolveUser parses /etc/passwd and /etc/group to find UID/GID for a given user string (user[:group]).
func resolveUser(userStr string) (uid, gid int, err error) {
	if userStr == "" {
		return 0, 0, nil // Default to root
	}

	parts := strings.SplitN(userStr, ":", 2)
	userName := parts[0]
	groupName := ""
	if len(parts) > 1 {
		groupName = parts[1]
	}

	// Try numeric first
	if u, err := strconv.Atoi(userName); err == nil {
		uid = u
		gid = u // Default GID to UID if not specified
		if groupName != "" {
			if g, err := strconv.Atoi(groupName); err == nil {
				gid = g
			}
		}
		return uid, gid, nil
	}

	// Parse /etc/passwd
	f, err := os.Open("/etc/passwd")
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			cols := strings.Split(line, ":")
			if len(cols) >= 3 && cols[0] == userName {
				uid, _ = strconv.Atoi(cols[2])
				gid, _ = strconv.Atoi(cols[3])
				break
			}
		}
	}

	// If group specified by name, parse /etc/group
	if groupName != "" {
		if g, err := strconv.Atoi(groupName); err == nil {
			gid = g
		} else {
			f, err := os.Open("/etc/group")
			if err == nil {
				defer f.Close()
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					line := scanner.Text()
					cols := strings.Split(line, ":")
					if len(cols) >= 3 && cols[0] == groupName {
						gid, _ = strconv.Atoi(cols[2])
						break
					}
				}
			}
		}
	}

	return uid, gid, nil
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
			fmt.Printf("[warn] sysctl %s=%s: %v\n", key, value, err)
		}
	}
}
