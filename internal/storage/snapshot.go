package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chaitu426/minibox/internal/utils"
)

// Snapshot volume data. Instant clone via reflink (FICLONE).
func SnapshotVolume(volumePath string, snapshotID string) (string, error) {
	if _, err := os.Stat(volumePath); err != nil {
		return "", fmt.Errorf("volume path not found: %w", err)
	}

	snapshotRoot := filepath.Join(filepath.Dir(volumePath), "snapshots", snapshotID)
	if err := os.MkdirAll(snapshotRoot, 0755); err != nil {
		return "", fmt.Errorf("failed to create snapshot root: %w", err)
	}

	start := time.Now()
	// Parallel copy with reflink support.
	if err := utils.CopyRecursiveParallel(volumePath, snapshotRoot, nil); err != nil {
		return "", fmt.Errorf("snapshot copy failed: %w", err)
	}

	fmt.Printf("[storage] snapshot created id=%s dur=%v\n", snapshotID, time.Since(start))
	return snapshotRoot, nil
}
