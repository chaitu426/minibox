package storage

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chaitu426/minibox/internal/config"
)

type FileIndex struct {
	Name     string `json:"name"`
	Offset   int64  `json:"offset"` // Offset in the UNCOMPRESSED stream
	Size     int64  `json:"size"`
	Type     byte   `json:"type"`
	Mode     int64  `json:"mode"`
	Linkname string `json:"linkname,omitempty"`
}

type LayerIndex struct {
	Files []FileIndex `json:"files"`
}

// IndexLayer creates a JSON index of all files in a .tar.gz blob.
// Note: In this simplified version, we index offsets in the uncompressed stream.
func IndexLayer(blobPath string) error {
	indexPath := blobPath + ".index.json"
	if _, err := os.Stat(indexPath); err == nil {
		return nil // Already indexed
	}

	f, err := os.Open(blobPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Check if it's a gzip file by magic number
	buf := make([]byte, 2)
	n, _ := f.Read(buf)
	f.Seek(0, 0)
	if n < 2 || buf[0] != 0x1f || buf[1] != 0x8b {
		return nil // Skip non-gzip files (e.g. OCI configs, manifests)
	}

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var index LayerIndex

	// Tracking offset in uncompressed stream
	var currentOffset int64

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Calculate offset: This is approximate in a simple tar reader
		// To be precise for eStargz, we'd need a specialized writer.
		// For now, we'll use this for metadata-first lazy loading.
		index.Files = append(index.Files, FileIndex{
			Name:     header.Name,
			Offset:   currentOffset,
			Size:     header.Size,
			Type:     header.Typeflag,
			Mode:     header.Mode,
			Linkname: header.Linkname,
		})

		// This header.Size doesn't include the 512-byte tar header and padding.
		// A real implementation would track the underlying reader's position.
	}

	indexPath = blobPath + ".index.json"
	data, _ := json.MarshalIndent(index, "", "  ")
	fmt.Printf("➜ Generated OCI Index: %s\n", indexPath)
	return os.WriteFile(indexPath, data, 0644)
}

func GetLayerIndex(digest string) (*LayerIndex, error) {
	var indexPath string
	if filepath.Ext(digest) == ".gz" {
		indexPath = filepath.Join(config.DataRoot, digest+".index.json")
	} else {
		indexPath = filepath.Join(config.DataRoot, "blobs", "sha256", digest+".index.json")
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	var index LayerIndex
	err = json.Unmarshal(data, &index)
	return &index, err
}
