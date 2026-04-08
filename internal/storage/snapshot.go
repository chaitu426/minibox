package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chaitu426/minibox/internal/utils"
)

// SnapshotVolume creates a point-in-time snapshot of a volume's data.
// It leverages FICLONE (reflink) for zero-copy, near-instant cloning on supported filesystems (Btrfs, XFS).
func SnapshotVolume(volumePath string, snapshotID string) (string, error) {
	if _, err := os.Stat(volumePath); err != nil {
		return "", fmt.Errorf("volume path not found: %w", err)
	}

	snapshotRoot := filepath.Join(filepath.Dir(volumePath), "snapshots", snapshotID)
	if err := os.MkdirAll(snapshotRoot, 0755); err != nil {
		return "", fmt.Errorf("failed to create snapshot root: %w", err)
	}

	start := time.Now()
	// CopyRecursiveParallel will attempt CopyReflink for every regular file
	if err := utils.CopyRecursiveParallel(volumePath, snapshotRoot, nil); err != nil {
		return "", fmt.Errorf("snapshot copy failed: %w", err)
	}

	fmt.Printf("➜ Snapshot %s created in %v\n", snapshotID, time.Since(start))
	return snapshotRoot, nil
}
