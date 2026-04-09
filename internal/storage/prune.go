package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/models"
)

type PruneReport struct {
	BlobsRemoved         int   `json:"blobs_removed"`
	BytesFreed           int64 `json:"bytes_freed"`
	FUSEMountsKilled     int   `json:"fuse_mounts_killed"`
	BuildCacheRemoved    int   `json:"build_cache_removed,omitempty"`
	BuildCacheBytesFreed int64 `json:"build_cache_bytes_freed,omitempty"`
}

type PruneOptions struct {
	BuildCache bool
}

// PruneSystem completely garbage collects orphaned blobs and lazy mounts.
// Optional: can also delete the build cache (DataRoot/layers) when requested.
func PruneSystem() (*PruneReport, error) {
	return PruneSystemWithOptions(PruneOptions{})
}

func PruneSystemWithOptions(opts PruneOptions) (*PruneReport, error) {
	report := &PruneReport{}

	indexPath := filepath.Join(config.DataRoot, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return report, nil // Nothing to prune
	}

	var index models.OCIImageIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("corrupt index.json: %v", err)
	}

	activeBlobs := make(map[string]bool)

	// 1. Gather all active blobs from the index and manifests
	for _, m := range index.Manifests {
		digest := strings.TrimPrefix(m.Digest, "sha256:")
		activeBlobs[digest] = true

		manifestPath := filepath.Join(config.DataRoot, "blobs", "sha256", digest)
		mData, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var manifest models.OCIManifest
		json.Unmarshal(mData, &manifest)

		configDigest := strings.TrimPrefix(manifest.Config.Digest, "sha256:")
		activeBlobs[configDigest] = true

		for _, l := range manifest.Layers {
			lDigest := strings.TrimPrefix(l.Digest, "sha256:")
			activeBlobs[lDigest] = true
		}
	}

	// 2. Scan blobs directory and remove dangling
	blobsDir := filepath.Join(config.DataRoot, "blobs", "sha256")
	entries, _ := os.ReadDir(blobsDir)
	for _, e := range entries {
		if !e.IsDir() && !activeBlobs[e.Name()] {
			// Orphaned blob
			fullPath := filepath.Join(blobsDir, e.Name())
			if info, err := os.Stat(fullPath); err == nil {
				report.BytesFreed += info.Size()
			}

			// Clean up index file as well if it exists
			os.Remove(fullPath + ".index.json")
			os.Remove(fullPath)
			report.BlobsRemoved++
		}
	}

	// 3. Scan lazy FUSE mounts and teardown dangling
	lazyDir := filepath.Join(config.DataRoot, "lazy")
	lazyEntries, _ := os.ReadDir(lazyDir)
	for _, e := range lazyEntries {
		if e.IsDir() && !activeBlobs[e.Name()] {
			fullPath := filepath.Join(lazyDir, e.Name())
			_ = syscall.Unmount(fullPath, 0)
			os.RemoveAll(fullPath)
			report.FUSEMountsKilled++

			// Remove associated cache
			os.RemoveAll(filepath.Join(config.DataRoot, "cache", e.Name()))
		}
	}

	// 4. Scan extracted full-layers and remove dangling
	extractedDir := filepath.Join(config.DataRoot, "extracted")
	extEntries, _ := os.ReadDir(extractedDir)
	for _, e := range extEntries {
		if e.IsDir() && !activeBlobs[e.Name()] {
			os.RemoveAll(filepath.Join(extractedDir, e.Name()))
		}
	}

	// 5. Clean up old tmp layers that failed to finish
	os.RemoveAll(filepath.Join(config.DataRoot, "tmp"))

	// 6. Optional: remove build cache layers (content-addressed upperdirs).
	if opts.BuildCache {
		layersDir := filepath.Join(config.DataRoot, "layers")
		entries, _ := os.ReadDir(layersDir)
		for _, e := range entries {
			// Best-effort: count bytes and delete directories/files.
			full := filepath.Join(layersDir, e.Name())
			if info, err := os.Stat(full); err == nil {
				report.BuildCacheBytesFreed += info.Size()
			}
			_ = os.RemoveAll(full)
			report.BuildCacheRemoved++
		}
	}

	return report, nil
}
