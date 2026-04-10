package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sync"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/models"
	"github.com/chaitu426/minibox/internal/network"
	"github.com/chaitu426/minibox/internal/security"
	"github.com/chaitu426/minibox/internal/storage"
	"github.com/chaitu426/minibox/internal/storage/lazy"
	"github.com/chaitu426/minibox/internal/utils"
)

var (
	configCache = make(map[string]*models.OCIConfig)
	cacheMu     sync.RWMutex
)

// containerExecEnv is the daemon environment with a single container-oriented PATH.
// Avoid duplicate PATH keys from appending after os.Environ() — POSIX leaves duplicate
// handling unspecified; ash often honors the first assignment when looking up commands.
func containerExecEnv() []string {
	const pathEnv = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	out := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "PATH=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, pathEnv)
	return out
}

type ContainerOptions struct {
	User        string            `json:"user"`
	MemoryMB    int               `json:"memory"`
	CPUMax      int               `json:"cpu"`
	CPUSet      string            `json:"cpuset"`
	IOWeight    int               `json:"io_weight"`
	OOMScoreAdj int               `json:"oom_score_adj"`
	Sysctls     map[string]string `json:"sysctls"`
	DBMode      bool              `json:"db_mode"`
	ShmSize     int               `json:"shm_size"` // /dev/shm size in MB (default 256 MB for DB containers)
	Hostname    string            `json:"hostname,omitempty"`
}

// RunCommand runs a container and returns all output at once (used for detached mode).
func RunCommand(ctx context.Context, containerID string, image string, opts ContainerOptions, detached bool, portMap map[string]string, volumes map[string]string, userEnv []string, cmdArgs []string, name string, project string) ([]byte, error) {
	if !security.ValidContainerID(containerID) {
		return nil, fmt.Errorf("invalid container id")
	}
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("no command provided")
	}

	// Resolve image metadata early so the child doesn't need to parse OCI again.
	imgConfig, err := ResolveImageConfig(image)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image config: %v", err)
	}
	configJSON, _ := json.Marshal(imgConfig)

	lowerDirs, err := ResolveImageLayers(image)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image layers: %v", err)
	}
	layersJSON, _ := json.Marshal(lowerDirs)

	volumesJSON, _ := json.Marshal(volumes)
	userEnvJSON, _ := json.Marshal(userEnv)

	opts.Hostname = name
	optsJSON, _ := json.Marshal(opts)
	args := append([]string{"child", containerID, image, string(optsJSON), string(configJSON), string(layersJSON), string(volumesJSON), string(userEnvJSON)}, cmdArgs...)
	// Detached containers must not inherit the request context; the HTTP handler returns
	// immediately and cancels it, which would kill the container process.
	var cmd *exec.Cmd
	if detached {
		cmd = exec.Command("/proc/self/exe", args...)
	} else {
		cmd = exec.CommandContext(ctx, "/proc/self/exe", args...)
	}
	cmd.Env = append(os.Environ(), "MINIBOX_CHILD_NEWNS=1")

	// Prepare container directories and permissions for the rootless child
	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	os.MkdirAll(filepath.Join(containerPath, "upper"), 0755)
	os.MkdirAll(filepath.Join(containerPath, "work"), 0755)
	os.MkdirAll(filepath.Join(containerPath, "rootfs"), 0755)
	// Container runs as host root for now
	_ = exec.Command("chown", "-R", "0:0", containerPath).Run()

	// Set up OverlayFS from host root before starting child
	if err := MountRootfs(containerID, lowerDirs); err != nil {
		return nil, fmt.Errorf("failed to mount rootfs: %v", err)
	}

	logFile, _ := os.Create(filepath.Join(containerPath, "container.log"))

	var out bytes.Buffer
	if detached {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		mwriter := io.MultiWriter(&out, logFile)
		cmd.Stdout = mwriter
		cmd.Stderr = mwriter
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWNET |
			syscall.CLONE_NEWIPC,
	}

	err = cmd.Start()

	if err != nil {
		return nil, err
	}

	// Set up container networking
	ip := network.AllocateIP()
	if netErr := network.SetupContainerNetwork(cmd.Process.Pid, containerID, ip, portMap); netErr != nil {
		fmt.Printf("[warn] network setup failed: %v\n", netErr)
	}

	info := ContainerInfo{
		ID:        containerID,
		Name:      name,
		Project:   project,
		Image:     image,
		Command:   strings.Join(cmdArgs, " "),
		PID:       cmd.Process.Pid,
		Status:    "running",
		Health:    "none",
		CreatedAt: time.Now(),
		ExitCode:  0,
		Ports:     portMap,
	}
	info.IP = ip
	RegisterContainer(info)
	syncServiceDiscovery(containerID, project)
	startHealthMonitor(containerID, cmd.Process.Pid, imgConfig)

	if detached {
		go func() {
			err := cmd.Wait()
			MarkContainerExited(containerID, exitCode(err))
			network.TeardownContainerNetwork(containerID, portMap, ip)
			logFile.Close()
		}()
		return []byte(containerID + "\n"), nil
	}

	err = cmd.Wait()
	MarkContainerExited(containerID, exitCode(err))
	network.TeardownContainerNetwork(containerID, portMap, ip)
	logFile.Close()

	if err != nil && ctx.Err() != nil {
		return out.Bytes(), fmt.Errorf("container run aborted by client: %w", ctx.Err())
	}
	return out.Bytes(), err
}

// RunCommandInteractive starts a container with a PTY and bridges it to the provided IO streams.
func RunCommandInteractive(ctx context.Context, containerID string, image string, opts ContainerOptions, portMap map[string]string, volumes map[string]string, userEnv []string, cmdArgs []string, stdin io.Reader, stdout io.Writer, name string, project string) error {
	if !security.ValidContainerID(containerID) {
		return fmt.Errorf("invalid container id")
	}

	// 1. Allocate PTY
	ptymaster, ptyslave, err := utils.StartPTY()
	if err != nil {
		fmt.Printf("[error] pty allocation failed: %v\n", err)
		return fmt.Errorf("pty allocation failed: %w", err)
	}
	defer ptymaster.Close()
	defer ptyslave.Close()

	fmt.Printf("[debug] PTY allocated: slave=%s\n", ptyslave.Name())

	// 2. Resolve image metadata
	imgConfig, err := ResolveImageConfig(image)
	if err != nil {
		return fmt.Errorf("failed to resolve image config: %v", err)
	}
	configJSON, _ := json.Marshal(imgConfig)
	lowerDirs, err := ResolveImageLayers(image)
	if err != nil {
		return fmt.Errorf("failed to resolve image layers: %v", err)
	}
	layersJSON, _ := json.Marshal(lowerDirs)
	volumesJSON, _ := json.Marshal(volumes)
	userEnvJSON, _ := json.Marshal(userEnv)
	opts.Hostname = name
	optsJSON, _ := json.Marshal(opts)

	// 3. Prepare child process
	args := append([]string{"child", containerID, image, string(optsJSON), string(configJSON), string(layersJSON), string(volumesJSON), string(userEnvJSON)}, cmdArgs...)
	cmd := exec.CommandContext(ctx, "/proc/self/exe", args...)
	cmd.Env = append(os.Environ(), "MINIBOX_CHILD_NEWNS=1")

	// PTY slave becomes Stdin/Stdout/Stderr for the child
	cmd.Stdin = ptyslave
	cmd.Stdout = ptyslave
	cmd.Stderr = ptyslave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC,
		Setsid:     true,
		Setctty:    true,
		Ctty:       0,
	}

	// 4. Prepare container FS (same as non-interactive)
	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	os.MkdirAll(filepath.Join(containerPath, "upper"), 0755)
	os.MkdirAll(filepath.Join(containerPath, "work"), 0755)
	os.MkdirAll(filepath.Join(containerPath, "rootfs"), 0755)
	_ = exec.Command("chown", "-R", "0:0", containerPath).Run()
	if err := MountRootfs(containerID, lowerDirs); err != nil {
		return fmt.Errorf("failed to mount rootfs: %v", err)
	}

	logFile, _ := os.Create(filepath.Join(containerPath, "container.log"))
	defer logFile.Close()

	fmt.Printf("[debug] starting interactive child: %v\n", cmdArgs)
	if err := cmd.Start(); err != nil {
		fmt.Printf("[error] child start failed: %v\n", err)
		return err
	}
	fmt.Printf("[debug] child started pid=%d\n", cmd.Process.Pid)

	// Networking
	ip := network.AllocateIP()
	if netErr := network.SetupContainerNetwork(cmd.Process.Pid, containerID, ip, portMap); netErr != nil {
		fmt.Printf("[warn] network setup failed: %v\n", netErr)
	}

	info := ContainerInfo{
		ID:        containerID,
		Name:      name,
		Project:   project,
		Image:     image,
		Command:   strings.Join(cmdArgs, " "),
		PID:       cmd.Process.Pid,
		Status:    "running",
		CreatedAt: time.Now(),
		Ports:     portMap,
		IP:        ip,
	}
	RegisterContainer(info)
	syncServiceDiscovery(containerID, project)
	startHealthMonitor(containerID, cmd.Process.Pid, imgConfig)

	// 5. Bridge I/O
	done := make(chan struct{})
	go func() {
		// Pipe stdin to PTY master
		fmt.Printf("[debug] starting stdin -> ptymaster relay\n")
		_, _ = io.Copy(ptymaster, stdin)
		fmt.Printf("[debug] stdin relay finished\n")
	}()

	go func() {
		// Pipe PTY master to stdout AND logfile with flushing
		fmt.Printf("[debug] starting ptymaster -> stdout relay\n")
		buf := make([]byte, 32*1024)
		for {
			n, err := ptymaster.Read(buf)
			if n > 0 {
				_, _ = stdout.Write(buf[:n])
				_, _ = logFile.Write(buf[:n])
				if flusher, ok := stdout.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			if err != nil {
				fmt.Printf("[debug] ptymaster read finished: %v\n", err)
				break
			}
		}
		close(done)
	}()

	// Wait for process to exit or context to be cancelled
	err = cmd.Wait()
	MarkContainerExited(containerID, exitCode(err))
	network.TeardownContainerNetwork(containerID, portMap, ip)

	// Briefly wait to ensure terminal output is flushed
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}

	return err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
	}
	return 1
}

func startHealthMonitor(containerID string, pid int, cfg *models.OCIConfig) {
	if cfg == nil || cfg.Config.Labels == nil {
		return
	}
	joined := cfg.Config.Labels["mini.healthcheck.cmd"]
	if joined == "" || pid <= 0 {
		return
	}
	parts := strings.Split(joined, "\x1f")
	if len(parts) == 0 {
		return
	}
	cmdStr := strings.Join(parts, " ")
	interval := 30
	if v := cfg.Config.Labels["mini.healthcheck.interval"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = n
		}
	}
	_ = UpdateContainerHealth(containerID, "starting")
	go func() {
		t := time.NewTicker(time.Duration(interval) * time.Second)
		defer t.Stop()
		for range t.C {
			cs := GetAllContainers()
			c, ok := cs[containerID]
			if !ok || c.Status != "running" || c.PID <= 0 {
				return
			}
			cmd := exec.Command("nsenter", "-t", strconv.Itoa(pid), "-m", "-u", "-n", "-i", "-p", "--", "/bin/sh", "-c", cmdStr)
			if err := cmd.Run(); err != nil {
				_ = UpdateContainerHealth(containerID, "unhealthy")
			} else {
				_ = UpdateContainerHealth(containerID, "healthy")
			}
		}
	}()
}

// RunCommandStream runs a container in foreground mode, streaming output directly to out in real-time.
func RunCommandStream(ctx context.Context, containerID string, image string, opts ContainerOptions, portMap map[string]string, volumes map[string]string, userEnv []string, cmdArgs []string, out io.Writer, name string, project string) error {
	if !security.ValidContainerID(containerID) {
		return fmt.Errorf("invalid container id")
	}
	if len(cmdArgs) == 0 {
		return fmt.Errorf("no command provided")
	}

	// Resolve image metadata early so the child doesn't need to parse OCI again.
	imgConfig, err := ResolveImageConfig(image)
	if err != nil {
		return fmt.Errorf("failed to resolve image config: %v", err)
	}
	configJSON, _ := json.Marshal(imgConfig)

	lowerDirs, err := ResolveImageLayers(image)
	if err != nil {
		return fmt.Errorf("failed to resolve image layers: %v", err)
	}
	layersJSON, _ := json.Marshal(lowerDirs)

	volumesJSON, _ := json.Marshal(volumes)
	userEnvJSON, _ := json.Marshal(userEnv)

	opts.Hostname = name
	optsJSON, _ := json.Marshal(opts)
	args := append([]string{"child", containerID, image, string(optsJSON), string(configJSON), string(layersJSON), string(volumesJSON), string(userEnvJSON)}, cmdArgs...)
	cmd := exec.CommandContext(ctx, "/proc/self/exe", args...)
	cmd.Env = append(os.Environ(), "MINIBOX_CHILD_NEWNS=1")

	// Prepare container directories and permissions for the rootless child
	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	os.MkdirAll(filepath.Join(containerPath, "upper"), 0755)
	os.MkdirAll(filepath.Join(containerPath, "work"), 0755)
	os.MkdirAll(filepath.Join(containerPath, "rootfs"), 0755)
	// Container runs as host root for now
	_ = exec.Command("chown", "-R", "0:0", containerPath).Run()

	// Set up OverlayFS from host root before starting child
	if err := MountRootfs(containerID, lowerDirs); err != nil {
		return fmt.Errorf("failed to mount rootfs: %v", err)
	}

	logFile, _ := os.Create(filepath.Join(containerPath, "container.log"))
	defer logFile.Close()

	mwriter := io.MultiWriter(out, logFile)
	cmd.Stdout = mwriter
	cmd.Stderr = mwriter

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWNET |
			syscall.CLONE_NEWIPC,
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Set up container networking
	ip := network.AllocateIP()
	if netErr := network.SetupContainerNetwork(cmd.Process.Pid, containerID, ip, portMap); netErr != nil {
		fmt.Fprintf(out, "[warn] network setup failed: %v\n", netErr)
	} else {
		fmt.Fprintf(out, "[network] Container IP: %s\n", ip)
	}

	info := ContainerInfo{
		ID:        containerID,
		Name:      name,
		Project:   project,
		Image:     image,
		Command:   strings.Join(cmdArgs, " "),
		PID:       cmd.Process.Pid,
		Status:    "running",
		Health:    "none",
		CreatedAt: time.Now(),
		ExitCode:  0,
		Ports:     portMap,
	}
	info.IP = ip
	RegisterContainer(info)
	syncServiceDiscovery(containerID, project)
	startHealthMonitor(containerID, cmd.Process.Pid, imgConfig)

	err = cmd.Wait()
	MarkContainerExited(containerID, exitCode(err))
	network.TeardownContainerNetwork(containerID, portMap, ip)
	UnmountRootfs(containerID)
	return err
}

// MountRootfs prepares the layered filesystem for the container.
func MountRootfs(containerID string, lowerDirs []string) error {
	containerPath := filepath.Join(config.DataRoot, "containers", containerID)
	upperDir := filepath.Join(containerPath, "upper")
	workDir := filepath.Join(containerPath, "work")
	rootfs := filepath.Join(containerPath, "rootfs")

	os.MkdirAll(upperDir, 0755)
	os.MkdirAll(workDir, 0755)
	os.MkdirAll(rootfs, 0755)
	os.MkdirAll(filepath.Join(upperDir, ".old_root"), 0700)

	// CHOWN directories to host user so the mapped container root can access them
	// CHOWN directories to host root
	_ = exec.Command("chown", "-R", "0:0", containerPath).Run()

	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", strings.Join(lowerDirs, ":"), upperDir, workDir)
	if err := syscall.Mount("overlay", rootfs, "overlay", 0, opts); err != nil {
		return err
	}

	// Make the mount point a mount-point itself and private for User Namespaces
	if err := syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("failed to bind mount rootfs: %v", err)
	}
	return syscall.Mount("", rootfs, "", syscall.MS_PRIVATE|syscall.MS_REC, "")
}

func ResolveImageLayers(imageName string) ([]string, error) {
	indexPath := filepath.Join(config.DataRoot, "index.json")
	var index models.OCIImageIndex

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read OCI index: %v", err)
	}
	json.Unmarshal(data, &index)

	var manifestDigest string
	for _, m := range index.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == imageName {
			manifestDigest = strings.TrimPrefix(m.Digest, "sha256:")
			break
		}
	}

	if manifestDigest == "" {
		return nil, fmt.Errorf("image %s not found in OCI index", imageName)
	}

	manifestPath := filepath.Join(config.DataRoot, "blobs", "sha256", manifestDigest)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest %s: %v", manifestDigest, err)
	}

	var manifest models.OCIManifest
	json.Unmarshal(manifestData, &manifest)

	var layers []string
	extractedRoot := filepath.Join(config.DataRoot, "extracted")

	type layerResult struct {
		index int
		path  string
		err   error
	}
	results := make(chan layerResult, len(manifest.Layers))

	for i, l := range manifest.Layers {
		go func(idx int, layer models.OCIDescriptor) {
			digest := strings.TrimPrefix(layer.Digest, "sha256:")
			blobPath := filepath.Join(config.DataRoot, "blobs", "sha256", digest)

			// Fast path: if we already have an extracted view, use it directly.
			// This avoids tar extraction and FUSE lazy-mount overhead on startup.
			destPath := filepath.Join(extractedRoot, digest)
			if info, err := os.Stat(destPath); err == nil && info.IsDir() && !isDirEmpty(destPath) {
				results <- layerResult{idx, destPath, nil}
				return
			}

			// Try Lazy Loading if index exists
			index, err := storage.GetLayerIndex(digest)
			if err != nil {
				// Fallback for differently named base image layers if they actually exist
				if idx == 0 && len(manifest.Layers) == 1 {
					if _, statErr := os.Stat(filepath.Join(config.DataRoot, "alpine.tar.gz")); statErr == nil {
						index, err = storage.GetLayerIndex("alpine.tar.gz")
						blobPath = filepath.Join(config.DataRoot, "alpine.tar.gz")
					}
				}
			}

			if err == nil {
				mountPath := filepath.Join(config.DataRoot, "lazy", digest)
				cachePath := filepath.Join(config.DataRoot, "cache", digest)
				fmt.Printf("[runtime] lazy-mount layer=%s\n", digest[:12])
				if err := lazy.StartLazyMount(blobPath, mountPath, cachePath, index); err == nil {
					results <- layerResult{idx, mountPath, nil}
					return
				}
			}

			// Fallback to full extraction
			os.MkdirAll(destPath, 0755)
			if info, err := os.Stat(destPath); os.IsNotExist(err) || (err == nil && info.IsDir() && isDirEmpty(destPath)) {
				fmt.Printf("[runtime] extract layer=%s\n", digest[:12])
				if err := utils.ExtractTarGz(blobPath, destPath); err != nil {
					results <- layerResult{idx, "", fmt.Errorf("failed to extract layer %s: %v", digest, err)}
					return
				}
			}
			results <- layerResult{idx, destPath, nil}
		}(i, l)
	}

	layerPaths := make([]string, len(manifest.Layers))
	for i := 0; i < len(manifest.Layers); i++ {
		res := <-results
		if res.err != nil {
			return nil, res.err
		}
		layerPaths[res.index] = res.path
	}

	for i := len(layerPaths) - 1; i >= 0; i-- {
		layers = append(layers, layerPaths[i])
	}
	return layers, nil
}

// UnmountRootfs cleans up the layered filesystem.
func UnmountRootfs(containerID string) {
	rootfs := filepath.Join(config.DataRoot, "containers", containerID, "rootfs")
	syscall.Unmount(rootfs, 0)
}

func InvalidateImageCache(imageName string) {
	cacheMu.Lock()
	delete(configCache, imageName)
	cacheMu.Unlock()
}

func ResolveImageConfig(imageName string) (*models.OCIConfig, error) {
	cacheMu.RLock()
	if cfg, ok := configCache[imageName]; ok {
		cacheMu.RUnlock()
		return cfg, nil
	}
	cacheMu.RUnlock()

	indexPath := filepath.Join(config.DataRoot, "index.json")
	var index models.OCIImageIndex

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read OCI index: %v", err)
	}
	json.Unmarshal(data, &index)

	var manifestDigest string
	for _, m := range index.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == imageName {
			manifestDigest = strings.TrimPrefix(m.Digest, "sha256:")
			break
		}
	}

	if manifestDigest == "" {
		return nil, fmt.Errorf("image %s not found", imageName)
	}

	manifestPath := filepath.Join(config.DataRoot, "blobs", "sha256", manifestDigest)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}

	var manifest models.OCIManifest
	json.Unmarshal(manifestData, &manifest)

	configDigest := strings.TrimPrefix(manifest.Config.Digest, "sha256:")
	configPath := filepath.Join(config.DataRoot, "blobs", "sha256", configDigest)
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var imgConfig models.OCIConfig
	json.Unmarshal(configData, &imgConfig)

	cacheMu.Lock()
	configCache[imageName] = &imgConfig
	cacheMu.Unlock()

	return &imgConfig, nil
}

func isDirEmpty(name string) bool {
	f, err := os.Open(name)
	if err != nil {
		return true
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	return err == io.EOF
}

// ExecCommand runs a command inside a running container's namespaces and returns output.
func ExecCommand(ctx context.Context, pid int, cmdArgs []string) ([]byte, error) {
	nsenterPath, err := exec.LookPath("nsenter")
	if err != nil {
		return nil, fmt.Errorf("nsenter not found: %v", err)
	}

	pidStr := strconv.Itoa(pid)
	args := []string{"-t", pidStr, "-m", "-u", "-n", "-i", "-p", "--"}
	args = append(args, cmdArgs...)

	cmd := exec.CommandContext(ctx, nsenterPath, args...)
	cmd.Env = containerExecEnv()
	return cmd.CombinedOutput()
}

// ExecCommandInteractive runs a command inside a running container's namespaces with a PTY.
func ExecCommandInteractive(ctx context.Context, pid int, cmdArgs []string, stdin io.Reader, stdout io.Writer) error {
	nsenterPath, err := exec.LookPath("nsenter")
	if err != nil {
		return fmt.Errorf("nsenter not found: %v", err)
	}

	pidStr := strconv.Itoa(pid)
	args := []string{"-t", pidStr, "-m", "-u", "-n", "-i", "-p", "--"}
	args = append(args, cmdArgs...)

	// For interactive exec, we use a PTY just like RunCommandInteractive
	ptymaster, ptyslave, err := utils.StartPTY()
	if err != nil {
		return fmt.Errorf("pty allocation failed: %w", err)
	}
	defer ptymaster.Close()
	defer ptyslave.Close()

	cmd := exec.CommandContext(ctx, nsenterPath, args...)
	cmd.Env = containerExecEnv()
	cmd.Stdin = ptyslave
	cmd.Stdout = ptyslave
	cmd.Stderr = ptyslave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(ptymaster, stdin)
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptymaster.Read(buf)
			if n > 0 {
				_, _ = stdout.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()

	err = cmd.Wait()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}
	return err
}

func syncServiceDiscovery(containerID, project string) {
	if project == "" {
		return
	}
	containers := GetAllContainers()
	var hostsContent strings.Builder
	hostsContent.WriteString("127.0.0.1\tlocalhost\n")
	hostsContent.WriteString("::1\tlocalhost ip6-localhost ip6-loopback\n")
	hostsContent.WriteString("ff02::1\tip6-allnodes\n")
	hostsContent.WriteString("ff02::2\tip6-allrouters\n\n")

	for _, c := range containers {
		if c.Project == project && c.Status == "running" && c.IP != "" {
			if c.Name != "" {
				hostsContent.WriteString(fmt.Sprintf("%s\t%s\n", c.IP, c.Name))
			}
			hostsContent.WriteString(fmt.Sprintf("%s\t%s\n", c.IP, c.ID))
		}
	}

	hostsPath := filepath.Join(config.DataRoot, "containers", containerID, "rootfs", "etc", "hosts")
	// Ensure etc dir exists in rootfs
	os.MkdirAll(filepath.Dir(hostsPath), 0755)
	_ = os.WriteFile(hostsPath, []byte(hostsContent.String()), 0644)
}
