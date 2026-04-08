package builder

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/models"
	"github.com/chaitu426/minibox/internal/storage"
	"github.com/chaitu426/minibox/internal/utils"
	containerRuntime "github.com/chaitu426/minibox/internal/runtime"
)

var blockPrefixRe = regexp.MustCompile(`^\[[A-Za-z0-9._-]+\]\s`)

// prefixLineWriter prefixes each output line and writes it atomically, so parallel blocks
// don't interleave partial lines in the streamed build logs.
type prefixLineWriter struct {
	mu     *sync.Mutex
	prefix string
	out    io.Writer
	buf    []byte
	lastActivity time.Time
}

func newPrefixLineWriter(mu *sync.Mutex, prefix string, out io.Writer) *prefixLineWriter {
	return &prefixLineWriter{mu: mu, prefix: prefix, out: out, lastActivity: time.Now()}
}

func (w *prefixLineWriter) LastActivity() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastActivity
}

func (w *prefixLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i+1]
		w.buf = w.buf[i+1:]

		w.mu.Lock()
		if len(line) > 0 && !blockPrefixRe.Match(line) {
			if _, err := io.WriteString(w.out, w.prefix); err != nil {
				w.mu.Unlock()
				return len(p), err
			}
		}
		if _, err := w.out.Write(line); err != nil {
			w.mu.Unlock()
			return len(p), err
		}
		w.mu.Unlock()
	}
	w.mu.Lock()
	w.lastActivity = time.Now()
	w.mu.Unlock()
	return len(p), nil
}

type activityTracker interface {
	LastActivity() time.Time
}

func withPulse(out io.Writer, work func() error) error {
	tracker, ok := out.(activityTracker)
	if !ok {
		return work()
	}

	done := make(chan error, 1)
	go func() {
		done <- work()
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			if time.Since(tracker.LastActivity()) >= 10*time.Second {
				fmt.Fprintln(out, "... (still working)")
			}
		}
	}
}

func BuildImage(ctx context.Context, cfile *models.Cfile, imageName string, contextDir string, out io.Writer) error {
	buildStart := time.Now()
	fmt.Fprintf(out, "[build] START image=%s\n", imageName)

	layersPath := filepath.Join(config.DataRoot, "layers")
	blobsPath := filepath.Join(config.DataRoot, "blobs", "sha256")
	os.MkdirAll(layersPath, 0755)
	os.MkdirAll(blobsPath, 0755)

	// Load .miniboxignore
	ignorePatterns := make(map[string]bool)
	ignorePath := filepath.Join(contextDir, ".miniboxignore")
	if data, err := os.ReadFile(ignorePath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				ignorePatterns[line] = true
			}
		}
	}

	var ignoreFn func(string) bool
	if len(ignorePatterns) > 0 {
		ignoreFn = func(path string) bool {
			rel, _ := filepath.Rel(contextDir, path)
			return ignorePatterns[rel] || ignorePatterns[filepath.Base(path)]
		}
	}

	var currentHash string
	var lowerDirs []string
	currentWorkdir := "/"

	// 1. Resolve Base Image (Unified Resolver)
	t0 := time.Now()
	baseLowerDirs, baseEnv, err := resolveBaseImage(ctx, cfile.BaseImage, out)
	if err != nil {
		return fmt.Errorf("failed to resolve base image %s: %w", cfile.BaseImage, err)
	}
	
	fmt.Fprintf(out, "[base] %s: ready (%s)\n", cfile.BaseImage, fmtDur(time.Since(t0)))
	currentHash = utils.GetHash(cfile.BaseImage)
	lowerDirs = baseLowerDirs

	// ── Phase 5: DAG-based block execution ────────────────────────────────
	if len(cfile.Blocks) > 0 {
		if err := buildFromBlocks(ctx, cfile, contextDir, layersPath, blobsPath, ignoreFn,
			&currentHash, &lowerDirs, baseEnv, out); err != nil {
			return err
		}
	} else {
		// Linear execution (legacy support)
		fmt.Fprintln(out, "[build] mode=legacy-linear")
		for i := 0; i < len(cfile.Instructions); {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("build aborted: %w", err)
			}
			inst := cfile.Instructions[i]

			if inst.Type == models.TypeRun {
				if err := buildSequentialStep(inst, i+1, &currentHash, &lowerDirs, &currentWorkdir, cfile.Env, baseEnv, contextDir, layersPath, ignoreFn, out); err != nil {
					return err
				}
				i++
				continue
			}

			j := i
			for j < len(cfile.Instructions) && cfile.Instructions[j].Type != models.TypeRun {
				j++
			}

			if j > i {
				if err := buildParallelSteps(cfile.Instructions[i:j], i+1, &currentHash, &lowerDirs, &currentWorkdir, contextDir, layersPath, ignoreFn, out); err != nil {
					return err
				}
				i = j
			}
		}
	}

	// 3. Finalize OCI Image (Phase 3 Optimization: Parallel Layer Processing)
	tFinalize := time.Now()
	fmt.Fprintf(out, "[finalize] writing %d layer(s)\n", len(lowerDirs))
	layerDescriptors := make([]models.OCIDescriptor, len(lowerDirs))
	diffIDs := make([]string, len(lowerDirs))

	type layerResult struct {
		index  int
		digest string
		size   int64
		err    error
	}
	results := make(chan layerResult, len(lowerDirs))

	for i, layerDir := range lowerDirs {
		go func(idx int, lDir string) {
			digest, size, err := saveLayerAsBlob(lDir, blobsPath, out)
			results <- layerResult{idx, digest, size, err}
		}(i, layerDir)
	}

	for i := 0; i < len(lowerDirs); i++ {
		res := <-results
		if res.err != nil {
			return fmt.Errorf("failed to save layer as blob: %w", res.err)
		}

		// OCI manifest layers are bottom-most...top-most
		// lowerDirs is top-most...bottom-most
		// So the index in layerDescriptors should be inverted
		manifestIdx := len(lowerDirs) - 1 - res.index
		layerDescriptors[manifestIdx] = models.OCIDescriptor{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Digest:    "sha256:" + res.digest,
			Size:      res.size,
		}
		diffIDs[manifestIdx] = "sha256:" + res.digest
	}

	// Create Config Blob
	labels := map[string]string{}
	if len(cfile.HealthcheckCmd) > 0 {
		labels["mini.healthcheck.cmd"] = strings.Join(cfile.HealthcheckCmd, "\x1f")
		iv := cfile.HealthcheckIntervalSec
		if iv <= 0 {
			iv = 30
		}
		labels["mini.healthcheck.interval"] = strconv.Itoa(iv)
	}
	imageConfig := models.OCIConfig{
		Architecture: "amd64",
		OS:           "linux",
		RootFS: models.RootFS{
			Type:    "layers",
			DiffIDs: diffIDs,
		},
		Config: models.ContainerConfig{
			Cmd:        cfile.Cmd,
			Env:        utils.MapToEnvSlice(cfile.Env),
			WorkingDir: cfile.Workdir,
			Labels:     labels,
		},
	}
	configData, _ := json.Marshal(imageConfig)
	configDigest := utils.CalculateDigest(configData)
	configPath := filepath.Join(blobsPath, configDigest)
	os.WriteFile(configPath, configData, 0644)

	// Create Manifest Blob
	manifest := models.OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: models.OCIDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    "sha256:" + configDigest,
			Size:      int64(len(configData)),
		},
		Layers: layerDescriptors,
	}
	manifestData, _ := json.Marshal(manifest)
	manifestDigest := utils.CalculateDigest(manifestData)
	manifestPath := filepath.Join(blobsPath, manifestDigest)
	os.WriteFile(manifestPath, manifestData, 0644)

	// Update Index
	if err := updateOCIIndex(imageName, manifestDigest, int64(len(manifestData))); err != nil {
		return err
	}

	fmt.Fprintf(out, "[finalize] done (%s)\n", fmtDur(time.Since(tFinalize)))
	fmt.Fprintf(out, "[build] DONE image=%s manifest=%s (%s)\n", imageName, manifestDigest[:12], fmtDur(time.Since(buildStart)))

	// Phase 6: Invalidate the daemon's image config cache to ensure the new build is picked up immediately
	containerRuntime.InvalidateImageCache(imageName)

	return nil
}

func fmtDur(d time.Duration) string {
	// Docker-like compact durations.
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%ds", m, s)
}

func saveLayerAsBlob(layerDir string, blobsPath string, out io.Writer) (string, int64, error) {
	// Optimization: check if we already have a cached digest for this layer directory
	digestPath := layerDir + ".digest"
	if data, err := os.ReadFile(digestPath); err == nil {
		cachedDigest := strings.TrimSpace(string(data))
		blobPath := filepath.Join(blobsPath, cachedDigest)
		if fi, err := os.Stat(blobPath); err == nil {
			// Found cached blob, skip compression
			return cachedDigest, fi.Size(), nil
		}
	}

	tmpFile := filepath.Join(config.DataRoot, fmt.Sprintf("tmp-layer-%s.tar.gz", filepath.Base(layerDir)))
	f, err := os.Create(tmpFile)
	if err != nil {
		return "", 0, err
	}

	hash := sha256.New()
	mw := io.MultiWriter(f, hash)

	if err := withPulse(out, func() error {
		return utils.CreateTarGz(layerDir, mw)
	}); err != nil {
		f.Close()
		os.Remove(tmpFile)
		return "", 0, err
	}
	f.Close()

	digest := hex.EncodeToString(hash.Sum(nil))
	blobPath := filepath.Join(blobsPath, digest)

	// Save the mapping for next time
	_ = os.WriteFile(digestPath, []byte(digest), 0644)

	if err := os.Rename(tmpFile, blobPath); err != nil {
		return "", 0, err
	}

	fi, _ := os.Stat(blobPath)
	
	// Phase 4 Optimization: Index the layer for Lazy Loading
	_ = storage.IndexLayer(blobPath)
	
	return digest, fi.Size(), nil
}

// ─── Phase 5: DAG Block Scheduler ─────────────────────────────────────────────

// buildFromBlocks executes the block dependency graph, running ready blocks concurrently.
func buildFromBlocks(ctx context.Context, cfile *models.Cfile, contextDir, layersPath, blobsPath string,
	ignoreFn func(string) bool,
	currentHash *string, lowerDirs *[]string, baseEnv []string, out io.Writer) error {

	done := make(map[string]bool)
	blockLayerDir := make(map[string]string)
	blockWorkdir := make(map[string]string)

	fmt.Fprintf(out, "[build] mode=dag blocks=%d\n", len(cfile.Blocks))

	writeMu := &sync.Mutex{}
	waveIdx := 0
	totalCached := 0
	totalBuilt := 0

	// getTransitiveDeps gets ALL layers needed to run the block (Needs only, NOT BNeeds since BNeeds are just copied from)
	var getTransitiveDeps func(block string, visited map[string]bool)
	getTransitiveDeps = func(block string, visited map[string]bool) {
		for _, b := range cfile.Blocks {
			if b.Name == block {
				for _, dep := range b.Needs {
					if !visited[dep] {
						visited[dep] = true
						getTransitiveDeps(dep, visited)
					}
				}
				break
			}
		}
	}

	for len(done) < len(cfile.Blocks) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("build aborted: %w", err)
		}

		var wave []*models.Block
		for _, b := range cfile.Blocks {
			if done[b.Name] {
				continue
			}
			satisfied := true
			// Both Needs and BNeeds must be done before this block can START
			for _, dep := range append(b.Needs, b.BNeeds...) {
				if !done[dep] {
					satisfied = false
					break
				}
			}
			if satisfied {
				wave = append(wave, b)
			}
		}
		if len(wave) == 0 {
			return fmt.Errorf("circular or unsatisfiable block dependencies detected")
		}

		waveIdx++
		var waveNames []string
		for _, b := range wave {
			waveNames = append(waveNames, b.Name)
		}
		writeMu.Lock()
		fmt.Fprintf(out, "[dag] wave=%d ready=%s\n", waveIdx, strings.Join(waveNames, ","))
		writeMu.Unlock()

		for _, b := range wave {
			done[b.Name] = true
		}

		type result struct {
			name     string
			layerDir string
			workdir  string
			cached   bool
			dur      time.Duration
			err      error
		}
		results := make(chan result, len(wave))

		for _, blk := range wave {
			baseLowers := append([]string{}, *lowerDirs...)
			go func(b *models.Block, base []string) {
				bw := newPrefixLineWriter(writeMu, "["+b.Name+"] ", out)
				inheritedWorkdir := "/"

				transitive := make(map[string]bool)
				getTransitiveDeps(b.Name, transitive)

				var depLowers []string
				for _, prev := range cfile.Blocks {
					if transitive[prev.Name] {
						if ld, ok := blockLayerDir[prev.Name]; ok {
							depLowers = append(depLowers, ld)
						}
					}
				}
				
				for _, dep := range b.Needs {
					if wd, ok := blockWorkdir[dep]; ok && wd != "/" {
						inheritedWorkdir = wd
					}
				}

				layerDir, outWorkdir, cached, dur, err := buildBlock(b, *currentHash, depLowers,
					contextDir, layersPath, cfile.Env, baseEnv, ignoreFn, inheritedWorkdir, base, blockLayerDir, bw)
				results <- result{b.Name, layerDir, outWorkdir, cached, dur, err}
			}(blk, baseLowers)
		}

		waveCached := 0
		waveBuilt := 0
		var waveDur time.Duration
		var waveErr error
		
		for i := 0; i < len(wave); i++ {
			res := <-results
			if res.err != nil {
				if waveErr == nil {
					waveErr = fmt.Errorf("block %s: %w", res.name, res.err)
				}
			}
			if res.cached {
				waveCached++
			} else {
				waveBuilt++
			}
			waveDur += res.dur
			blockLayerDir[res.name] = res.layerDir
			blockWorkdir[res.name] = res.workdir
		}
		
		if waveErr != nil {
			return waveErr
		}

		fmt.Fprintf(out, "[dag] wave=%d done cached=%d built=%d cpu_time=%s\n", waveIdx, waveCached, waveBuilt, fmtDur(waveDur))
		totalCached += waveCached
		totalBuilt += waveBuilt

		for _, b := range wave {
			*currentHash = utils.GetHash(*currentHash + b.Name)
		}
	}

	// Calculate which blocks are actually needed in the final image.
	// The implicit target is the LAST block defined in the file.
	finalTarget := cfile.Blocks[len(cfile.Blocks)-1].Name
	finalTransitive := map[string]bool{finalTarget: true}
	getTransitiveDeps(finalTarget, finalTransitive)

	for i := 0; i < len(cfile.Blocks); i++ {
		b := cfile.Blocks[i]
		if finalTransitive[b.Name] {
			if ld := blockLayerDir[b.Name]; ld != "" {
				*lowerDirs = append([]string{ld}, *lowerDirs...)
			}
		}
	}
	fmt.Fprintf(out, "[dag-summary] blocks=%d cached=%d built=%d\n", len(cfile.Blocks), totalCached, totalBuilt)

	return nil
}

// buildBlock executes a single block, returning the path to its output layer dir and the final workdir.
// baseLowerDirs: the base alpine + any previously committed layers (for the full OverlayFS stack).
func buildBlock(b *models.Block, parentHash string, depLowers []string,
	contextDir, layersPath string, env map[string]string, baseEnv []string,
	ignoreFn func(string) bool, inheritedWorkdir string, baseLowerDirs []string, blockLayerDir map[string]string, out io.Writer) (string, string, bool, time.Duration, error) {

	blockStart := time.Now()
	fmt.Fprintf(out, "START needs=%s workdir=%s\n", strings.Join(b.Needs, ","), inheritedWorkdir)

	currentWorkdir := inheritedWorkdir

	// Compute layer cache key
	cmdStr := ""
	for _, inst := range b.Instructions {
		cmdStr += string(inst.Type) + strings.Join(inst.Args, " ")
		// COPY cache must include source content, otherwise changed files can incorrectly
		// reuse old layers (observed with missing scripts in cached source blocks).
		if inst.Type == models.TypeCopy && len(inst.Args) > 0 {
			if strings.HasPrefix(strings.ToUpper(inst.Args[0]), "FROM=") {
				fromBlockName := strings.TrimPrefix(inst.Args[0], "FROM=")
				if ld, ok := blockLayerDir[fromBlockName]; ok {
					cmdStr += filepath.Base(ld) // The base is the sha256 layer hash
				}
			} else {
				src := filepath.Join(contextDir, inst.Args[0])
				if contentHash, err := utils.HashDir(src); err == nil {
					cmdStr += contentHash
				}
			}
		}
	}
	if b.AutoDeps {
		cmdStr += "auto-deps"
	}
	// Make cache key depend on inherited workdir, so if graph topology changes, it busts cache.
	layerHash := utils.GetHash(parentHash + strings.Join(b.Needs, ",") + currentWorkdir + cmdStr)
	layerDir := filepath.Join(layersPath, layerHash)

	// To cache properly, we need to know the final workdir. Since it's deterministic based on the instructions,
	// let's compute what the final workdir would be without mounting.
	finalWorkdir := currentWorkdir
	for _, inst := range b.Instructions {
		if inst.Type == models.TypeWorkdir && len(inst.Args) > 0 {
			finalWorkdir = inst.Args[0]
		}
	}

	markerPath := filepath.Join(layerDir, ".minibox_layer_complete")
	if _, err := os.Stat(markerPath); err == nil {
		fmt.Fprintf(out, "CACHED (%s)\n", fmtDur(time.Since(blockStart)))
		return layerDir, finalWorkdir, true, time.Since(blockStart), nil
	}
	
	// If the directory exists but the marker doesn't, it was an incomplete or failed build.
	// Clean it up so we can start fresh.
	if _, err := os.Stat(layerDir); err == nil {
		fmt.Fprintf(out, "Detected corrupted cache for %s, rebuilding...\n", layerHash[:12])
		os.RemoveAll(layerDir)
	}

	// Build the full lower stack: base alpine + deps
	allLowers := append(baseLowerDirs, depLowers...)

	tmpUpper := filepath.Join(config.DataRoot, "tmp", layerHash, "upper")
	
	// Clean slate: remove any leftover from a previously aborted build
	os.RemoveAll(filepath.Join(config.DataRoot, "tmp", layerHash))
	os.MkdirAll(tmpUpper, 0755)

	tmpWork := filepath.Join(config.DataRoot, "tmp", layerHash, "work")
	tmpRoot := filepath.Join(config.DataRoot, "tmp", layerHash, "root")
	
	setupMounts := func() error {
		if _, err := os.Stat(tmpRoot); err == nil {
			return nil // Already setup
		}
		os.MkdirAll(tmpWork, 0755)
		os.MkdirAll(tmpRoot, 0755)
		return nil
	}

	// Pre-determine if we need a mount for this block
	needsMount := b.AutoDeps
	if !needsMount {
		for _, inst := range b.Instructions {
			if inst.Type == models.TypeRun {
				needsMount = true
				break
			}
		}
	}

	var procPath, sysPath string
	if needsMount {
		if err := setupMounts(); err != nil {
			return "", "", false, time.Since(blockStart), err
		}
		// Mount the full overlay once for the duration of the block
		opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
			strings.Join(allLowers, ":"), tmpUpper, tmpWork)
		if err := syscall.Mount("overlay", tmpRoot, "overlay", 0, opts); err != nil {
			return "", "", false, time.Since(blockStart), fmt.Errorf("overlay mount failed: %w", err)
		}

		// Set up persistent mounts inside the chroot
		os.MkdirAll(filepath.Join(tmpRoot, "etc"), 0755)
		utils.CopyFile("/etc/resolv.conf", filepath.Join(tmpRoot, "etc", "resolv.conf"))
		
		tmpDirPath := filepath.Join(tmpRoot, "tmp")
		os.MkdirAll(tmpDirPath, 01777)
		os.Chmod(tmpDirPath, 01777)

		devPath := filepath.Join(tmpRoot, "dev")
		procPath = filepath.Join(tmpRoot, "proc")
		sysPath = filepath.Join(tmpRoot, "sys")
		os.MkdirAll(devPath, 0755)
		os.MkdirAll(procPath, 0755)
		os.MkdirAll(sysPath, 0755)
		
		nullDev := filepath.Join(devPath, "null")
		zeroDev := filepath.Join(devPath, "zero")
		randomDev := filepath.Join(devPath, "random")
		urandomDev := filepath.Join(devPath, "urandom")

		syscall.Mknod(nullDev, syscall.S_IFCHR|0666, int(1<<8|3))
		syscall.Mknod(zeroDev, syscall.S_IFCHR|0666, int(1<<8|5))
		syscall.Mknod(randomDev, syscall.S_IFCHR|0666, int(1<<8|8))
		syscall.Mknod(urandomDev, syscall.S_IFCHR|0666, int(1<<8|9))

		os.Chmod(nullDev, 0666)
		os.Chmod(zeroDev, 0666)
		os.Chmod(randomDev, 0666)
		os.Chmod(urandomDev, 0666)

		syscall.Mount("proc", procPath, "proc", 0, "")
		syscall.Mount("sysfs", sysPath, "sysfs", syscall.MS_RDONLY, "")
	}

	cleanupMounts := func() {
		if needsMount {
			syscall.Unmount(procPath, 0)
			syscall.Unmount(sysPath, 0)
			if err := syscall.Unmount(tmpRoot, 0); err != nil {
				fmt.Fprintf(out, "[builder] Warning: failed to unmount overlay: %v\n", err)
			}
		}
	}
	defer cleanupMounts()

	// Run each instruction
	for _, inst := range b.Instructions {
		switch inst.Type {
		case models.TypeRun:
			// Chroot and run via /bin/sh
			shellCmd := strings.Join(inst.Args, " ")
			if currentWorkdir != "/" && currentWorkdir != "" {
				shellCmd = fmt.Sprintf("cd %s && %s", currentWorkdir, shellCmd)
			}
			cmd := exec.Command("chroot", tmpRoot, "/bin/sh", "-c", shellCmd)
			cmd.Env = mergeEnvs(baseEnv, env)
			cmd.Stdout = out
			cmd.Stderr = out
			err := withPulse(out, func() error {
				return cmd.Run()
			})
			if err != nil {
				return "", "", false, time.Since(blockStart), fmt.Errorf("run %v: %w", inst.Args, err)
			}

		case models.TypeCopy:
			if len(inst.Args) < 2 {
				continue
			}
			
			// Handle COPY FROM=block src dest
			if strings.HasPrefix(strings.ToUpper(inst.Args[0]), "FROM=") {
				if len(inst.Args) < 3 {
					return "", "", false, time.Since(blockStart), fmt.Errorf("copy FROM requires block, src, and dest")
				}
				fromBlockName := strings.TrimPrefix(inst.Args[0], "FROM=")
				layerDir, ok := blockLayerDir[fromBlockName]
				if !ok || layerDir == "" {
					return "", "", false, time.Since(blockStart), fmt.Errorf("copy FROM block %s not found or not completed", fromBlockName)
				}
				
				src := filepath.Join(layerDir, strings.TrimPrefix(inst.Args[1], "/"))
				dest := filepath.Join(tmpUpper, strings.TrimPrefix(inst.Args[2], "/"))
				
				fmt.Fprintf(out, "copy %s -> %s\n", inst.Args[1], inst.Args[2])
				if err := utils.CopyRecursive(src, dest, nil); err != nil {
					return "", "", false, time.Since(blockStart), fmt.Errorf("copy %v: %w", inst.Args, err)
				}
				continue
			}
			src := filepath.Join(contextDir, strings.TrimPrefix(inst.Args[0], "/"))
			dest := filepath.Join(tmpUpper, strings.TrimPrefix(inst.Args[1], "/"))
			if err := utils.CopyRecursive(src, dest, ignoreFn); err != nil {
				return "", "", false, time.Since(blockStart), fmt.Errorf("copy %v: %w", inst.Args, err)
			}

		case models.TypeWorkdir:
			if len(inst.Args) > 0 {
				currentWorkdir = inst.Args[0]
				os.MkdirAll(filepath.Join(tmpUpper, strings.TrimPrefix(inst.Args[0], "/")), 0755)
			}
		}
	}

	// auto-deps: detect package files and run the right installer via chroot
	if b.AutoDeps {
		if err := runAutoDeps(tmpRoot, tmpUpper, allLowers, tmpWork, currentWorkdir, env, baseEnv, true, out); err != nil {
			return "", "", false, time.Since(blockStart), fmt.Errorf("auto-deps in block %s: %w", b.Name, err)
		}
	}

	// Explicitly cleanup mounts BEFORE renaming the layer directory.
	// This ensures OverlayFS locks on tmpUpper are released.
	cleanupMounts()

	// Persist the built layer
	if err := os.Rename(tmpUpper, layerDir); err != nil {
		// Fallback for cross-device move or busy mount
		if err := os.MkdirAll(layerDir, 0755); err != nil {
			return "", "", false, time.Since(blockStart), fmt.Errorf("failed to create layer dir: %w", err)
		}
		if err := utils.CopyRecursive(tmpUpper, layerDir, nil); err != nil {
			return "", "", false, time.Since(blockStart), fmt.Errorf("failed to persist build layer: %w", err)
		}
	}

	// Atomic completion: create the marker file only after all data is moved/copied.
	markerPath = filepath.Join(layerDir, ".minibox_layer_complete")
	os.WriteFile(markerPath, []byte(time.Now().Format(time.RFC3339)), 0644)

	os.RemoveAll(filepath.Join(config.DataRoot, "tmp", layerHash))

	fmt.Fprintf(out, "DONE (%s)\n", fmtDur(time.Since(blockStart)))
	return layerDir, currentWorkdir, false, time.Since(blockStart), nil
}

// runAutoDeps detects known manifest files and installs dependencies accordingly using chroot.
func runAutoDeps(root, upper string, lowers []string, work, workdir string, env map[string]string, baseEnv []string, alreadyMounted bool, out io.Writer) error {
	// Mount overlay to inspect files and run installers
	tmpRoot := root
	if !alreadyMounted && len(lowers) > 0 {
		opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
			strings.Join(lowers, ":"), upper, work)
		if err := syscall.Mount("overlay", root, "overlay", 0, opts); err != nil {
			return fmt.Errorf("overlay mount failed in auto-deps: %w", err)
		}
		defer syscall.Unmount(root, 0)

		// Set up /proc, /tmp, /dev, DNS inside the chroot (required for pip/npm)
		os.MkdirAll(filepath.Join(tmpRoot, "etc"), 0755)
		utils.CopyFile("/etc/resolv.conf", filepath.Join(tmpRoot, "etc", "resolv.conf"))
		
		tmpDirPath := filepath.Join(tmpRoot, "tmp")
		os.MkdirAll(tmpDirPath, 01777)
		os.Chmod(tmpDirPath, 01777)

		devPath := filepath.Join(tmpRoot, "dev")
		procPath := filepath.Join(tmpRoot, "proc")
		sysPath := filepath.Join(tmpRoot, "sys")
		os.MkdirAll(devPath, 0755)
		os.MkdirAll(procPath, 0755)
		os.MkdirAll(sysPath, 0755)
		
		nullDev := filepath.Join(devPath, "null")
		zeroDev := filepath.Join(devPath, "zero")
		randomDev := filepath.Join(devPath, "random")
		urandomDev := filepath.Join(devPath, "urandom")

		syscall.Mknod(nullDev, syscall.S_IFCHR|0666, int(1<<8|3))
		syscall.Mknod(zeroDev, syscall.S_IFCHR|0666, int(1<<8|5))
		syscall.Mknod(randomDev, syscall.S_IFCHR|0666, int(1<<8|8))
		syscall.Mknod(urandomDev, syscall.S_IFCHR|0666, int(1<<8|9))

		os.Chmod(nullDev, 0666)
		os.Chmod(zeroDev, 0666)
		os.Chmod(randomDev, 0666)
		os.Chmod(urandomDev, 0666)

		syscall.Mount("proc", procPath, "proc", 0, "")
		defer syscall.Unmount(procPath, 0)
		syscall.Mount("sysfs", sysPath, "sysfs", syscall.MS_RDONLY, "")
		defer syscall.Unmount(sysPath, 0)
	}

	type detector struct {
		file    string
		command []string
		label   string
	}
	detectors := []detector{
		{"package.json", []string{"npm", "install"}, "npm install"},
		{"requirements.txt", []string{"pip", "install", "-r", "requirements.txt"}, "pip install"},
		{"go.mod", []string{"go", "mod", "download"}, "go mod download"},
		{"Cargo.toml", []string{"cargo", "build"}, "cargo build"},
	}

	// We look for files on the HOST view of the overlay
	hostCwd := filepath.Join(tmpRoot, strings.TrimPrefix(workdir, "/"))

	for _, d := range detectors {
		if _, err := os.Stat(filepath.Join(hostCwd, d.file)); err == nil {
			fmt.Fprintf(out, "   ---> auto-deps: detected %s → running %s\n", d.file, d.label)

			// Build the chroot shell command
			shellCmd := strings.Join(d.command, " ")
			if workdir != "/" && workdir != "" {
				shellCmd = fmt.Sprintf("cd %s && %s", workdir, shellCmd)
			}

			cmd := exec.Command("chroot", tmpRoot, "/bin/sh", "-c", shellCmd)
			cmd.Env = mergeEnvs(baseEnv, env)
			cmd.Stdout = out
			cmd.Stderr = out

			if err := withPulse(out, func() error {
				return cmd.Run()
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// func mapToSlice(m map[string]string) []string {
// 	var out []string
// 	for k, v := range m {
// 		out = append(out, k+"="+v)
// 	}
// 	return out
// }

func updateOCIIndex(imageName string, manifestDigest string, size int64) error {
	indexPath := filepath.Join(config.DataRoot, "index.json")
	var index models.OCIImageIndex

	data, err := os.ReadFile(indexPath)
	if err == nil {
		json.Unmarshal(data, &index)
	} else {
		index.SchemaVersion = 2
	}

	found := false
	newDescriptor := models.OCIDescriptor{
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Digest:    "sha256:" + manifestDigest,
		Size:      size,
		Annotations: map[string]string{
			"org.opencontainers.image.ref.name": imageName,
		},
	}

	for i, m := range index.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == imageName {
			index.Manifests[i] = newDescriptor
			found = true
			break
		}
	}

	if !found {
		index.Manifests = append(index.Manifests, newDescriptor)
	}

	indexData, _ := json.MarshalIndent(index, "", "  ")
	return os.WriteFile(indexPath, indexData, 0644)
}

func resolveBaseImage(ctx context.Context, baseImage string, out io.Writer) ([]string, []string, error) {
	if baseImage == "scratch" {
		return nil, nil, nil
	}

	// 1. Try local resolution (previously built images)
	layers, err := containerRuntime.ResolveImageLayers(baseImage)
	if err == nil {
		imgConfig, _ := containerRuntime.ResolveImageConfig(baseImage)
		var env []string
		if imgConfig != nil {
			env = imgConfig.Config.Env
		}
		fmt.Fprintf(out, "[base] %s: using local image\n", baseImage)
		return layers, env, nil
	}

	// 2. Try local tarball resolution
	if strings.HasSuffix(baseImage, ".tar") || strings.HasSuffix(baseImage, ".tar.gz") {
		if _, err := os.Stat(baseImage); err == nil {
			baseImageSafe := strings.ReplaceAll(filepath.Base(baseImage), ".", "_")
			basePath := filepath.Join(config.DataRoot, "base_layers", "archive_"+baseImageSafe)
			
			// Optimization: skip if already extracted
			if _, err := os.Stat(basePath); os.IsNotExist(err) {
				os.MkdirAll(basePath, 0755)
				fmt.Fprintf(out, "[base] %s: extracting local archive...\n", baseImage)
				if strings.HasSuffix(baseImage, ".tar.gz") {
					err = utils.ExtractTarGz(baseImage, basePath)
				} else {
					f, _ := os.Open(baseImage)
					defer f.Close()
					tr := tar.NewReader(f)
					err = utils.ExtractTarStream(tr, basePath)
				}
				if err != nil {
					return nil, nil, fmt.Errorf("failed to extract base archive: %w", err)
				}
			} else {
				fmt.Fprintf(out, "[base] %s: using cached archive extract\n", baseImage)
			}
			return []string{basePath}, nil, nil
		}
	}

	// 3. Fallback to OCI pull
	baseImageSafe := strings.ReplaceAll(baseImage, ":", "_")
	baseImageSafe = strings.ReplaceAll(baseImageSafe, "/", "_")
	basePath := filepath.Join(config.DataRoot, "base_layers", baseImageSafe)

	if _, err := os.Stat(filepath.Join(basePath, "bin")); os.IsNotExist(err) {
		if err := FetchOCIImage(baseImage, basePath, out); err != nil {
			return nil, nil, fmt.Errorf("failed to fetch base image %s: %w", baseImage, err)
		}
	} else {
		fmt.Fprintf(out, "[base] %s: already cached\n", baseImage)
	}

	return []string{basePath}, loadBaseEnv(basePath), nil
}

func loadBaseEnv(basePath string) []string {
	// For legacy flattended base images, we can try to guess or load a .env file if it exists
	var env []string
	configPath := filepath.Join(basePath, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return env
	}
	var ociConfig models.OCIConfig
	if err := json.Unmarshal(data, &ociConfig); err != nil {
		return env
	}
	return ociConfig.Config.Env
}

func mergeEnvs(base []string, user map[string]string) []string {
	res := append([]string{}, base...)
	if len(res) == 0 {
		res = []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/go/bin",
			"HOME=/root",
		}
	}

	for k, v := range user {
		found := false
		prefix := k + "="
		for i, e := range res {
			if strings.HasPrefix(e, prefix) {
				res[i] = prefix + v
				found = true
				break
			}
		}
		if !found {
			res = append(res, prefix+v)
		}
	}
	return res
}

func buildSequentialStep(inst models.Instruction, stepNum int, currentHash *string, lowerDirs *[]string, currentWorkdir *string, env map[string]string, baseEnv []string, contextDir, layersPath string, ignoreFn func(string) bool, out io.Writer) error {
	instStr := fmt.Sprintf("%s %v", inst.Type, inst.Args)
	nextHash := utils.GetHash(*currentHash + instStr)
	layerPath := filepath.Join(layersPath, nextHash)

	fmt.Fprintf(out, "Step %d: %s\n", stepNum, instStr)

	if _, err := os.Stat(layerPath); err == nil {
		fmt.Fprintln(out, " ---> Using cache")
		*lowerDirs = append([]string{layerPath}, *lowerDirs...)
		*currentHash = nextHash
		return nil
	}

	fmt.Fprintln(out, " ---> Building...")
	tmpUpper := filepath.Join(config.DataRoot, "tmp", nextHash, "upper")
	tmpWork := filepath.Join(config.DataRoot, "tmp", nextHash, "work")
	tmpRoot := filepath.Join(config.DataRoot, "tmp", nextHash, "root")
	os.MkdirAll(tmpUpper, 0755)
	os.MkdirAll(tmpWork, 0755)
	os.MkdirAll(tmpRoot, 0755)

	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", strings.Join(*lowerDirs, ":"), tmpUpper, tmpWork)
	if err := syscall.Mount("overlay", tmpRoot, "overlay", 0, opts); err != nil {
		return fmt.Errorf("overlay mount failed: %w", err)
	}

	// Setup and Run
	os.MkdirAll(filepath.Join(tmpRoot, "etc"), 0755)
	utils.CopyFile("/etc/resolv.conf", filepath.Join(tmpRoot, "etc", "resolv.conf"))
	devPath, procPath := filepath.Join(tmpRoot, "dev"), filepath.Join(tmpRoot, "proc")
	sysPath := filepath.Join(tmpRoot, "sys")
	os.MkdirAll(devPath, 0755)
	os.MkdirAll(procPath, 0755)
	os.MkdirAll(sysPath, 0755)
	syscall.Mknod(filepath.Join(devPath, "null"), syscall.S_IFCHR|0666, int(1<<8|3))
	syscall.Mknod(filepath.Join(devPath, "zero"), syscall.S_IFCHR|0666, int(1<<8|5))
	syscall.Mknod(filepath.Join(devPath, "random"), syscall.S_IFCHR|0666, int(1<<8|8))
	syscall.Mknod(filepath.Join(devPath, "urandom"), syscall.S_IFCHR|0666, int(1<<8|9))
	syscall.Mount("proc", procPath, "proc", 0, "")
	syscall.Mount("sysfs", sysPath, "sysfs", syscall.MS_RDONLY, "")

	shellCmd := strings.Join(inst.Args, " ")
	if *currentWorkdir != "/" {
		shellCmd = fmt.Sprintf("cd %s && %s", *currentWorkdir, shellCmd)
	}
	cmd := exec.Command("chroot", tmpRoot, "/bin/sh", "-c", shellCmd)

	// Set up environment for the build step
	cmd.Env = mergeEnvs(baseEnv, env)

	cmd.Stdout, cmd.Stderr = out, out
	buildErr := withPulse(out, func() error {
		return cmd.Run()
	})

	syscall.Unmount(procPath, 0)
	syscall.Unmount(sysPath, 0)
	syscall.Unmount(devPath, 0)
	syscall.Unmount(tmpRoot, 0)

	if buildErr != nil {
		return buildErr
	}

	os.Rename(tmpUpper, layerPath)
	os.RemoveAll(filepath.Join(config.DataRoot, "tmp", nextHash))
	*lowerDirs = append([]string{layerPath}, *lowerDirs...)
	*currentHash = nextHash
	return nil
}

func buildParallelSteps(instructions []models.Instruction, startStep int, currentHash *string, lowerDirs *[]string, currentWorkdir *string, contextDir, layersPath string, ignoreFn func(string) bool, out io.Writer) error {
	type result struct {
		index     int
		hash      string
		layerPath string
		workdir   string
		err       error
	}

	results := make(chan result, len(instructions))
	tempHash := *currentHash

	for i, inst := range instructions {
		instStr := fmt.Sprintf("%s %v", inst.Type, inst.Args)
		nextHash := utils.GetHash(tempHash + instStr)
		if inst.Type == models.TypeCopy {
			src := filepath.Join(contextDir, inst.Args[0])
			if contentHash, err := utils.HashDir(src); err == nil {
				nextHash = utils.GetHash(nextHash + contentHash)
			}
		}

		go func(idx int, stepNum int, instruction models.Instruction, h string) {
			lPath := filepath.Join(layersPath, h)
			newWorkdir := ""
			if instruction.Type == models.TypeWorkdir {
				newWorkdir = instruction.Args[0]
			}

			if _, err := os.Stat(lPath); err == nil {
				results <- result{idx, h, lPath, newWorkdir, nil}
				return
			}

			// Preparation (no mount needed for static steps)
			tmpUpper := filepath.Join(config.DataRoot, "tmp", h, "upper")
			os.MkdirAll(tmpUpper, 0755)

			var buildErr error
			switch instruction.Type {
			case models.TypeWorkdir:
				buildErr = os.MkdirAll(filepath.Join(tmpUpper, strings.TrimPrefix(instruction.Args[0], "/")), 0755)
			case models.TypeCopy:
				src := filepath.Join(contextDir, instruction.Args[0])
				dest := filepath.Join(tmpUpper, strings.TrimPrefix(instruction.Args[1], "/"))
				buildErr = utils.CopyRecursive(src, dest, ignoreFn)
			}

			if buildErr == nil {
				os.Rename(tmpUpper, lPath)
				os.RemoveAll(filepath.Join(config.DataRoot, "tmp", h))
			}
			results <- result{idx, h, lPath, newWorkdir, buildErr}
		}(i, startStep+i, inst, nextHash)

		tempHash = nextHash
	}

	// Collect and apply in order
	orderedResults := make([]result, len(instructions))
	for i := 0; i < len(instructions); i++ {
		res := <-results
		orderedResults[res.index] = res
	}

	for i, res := range orderedResults {
		if res.err != nil {
			return fmt.Errorf("parallel step %d failed: %w", startStep+i, res.err)
		}
		fmt.Fprintf(out, "Step %d: %s %v (Parallel Ready)\n", startStep+i, instructions[i].Type, instructions[i].Args)
		*lowerDirs = append([]string{res.layerPath}, *lowerDirs...)
		*currentHash = res.hash
		if res.workdir != "" {
			*currentWorkdir = res.workdir
		}
	}

	return nil
}


