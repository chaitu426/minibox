package storage

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/models"
)

type imageArchiveMeta struct {
	Image          string `json:"image"`
	ManifestDigest string `json:"manifest_digest"`
}

func SaveImage(image, outPath string) error {
	indexPath := filepath.Join(config.DataRoot, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return err
	}
	var index models.OCIImageIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return err
	}

	manifestDigest := ""
	for _, m := range index.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == image {
			manifestDigest = strings.TrimPrefix(m.Digest, "sha256:")
			break
		}
	}
	if manifestDigest == "" {
		return fmt.Errorf("image not found: %s", image)
	}

	manifestPath := filepath.Join(config.DataRoot, "blobs", "sha256", manifestDigest)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest models.OCIManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return err
	}

	blobs := []string{manifestDigest, strings.TrimPrefix(manifest.Config.Digest, "sha256:")}
	for _, l := range manifest.Layers {
		blobs = append(blobs, strings.TrimPrefix(l.Digest, "sha256:"))
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()

	meta, _ := json.Marshal(imageArchiveMeta{Image: image, ManifestDigest: manifestDigest})
	if err := writeTarBytes(tw, "meta.json", meta); err != nil {
		return err
	}
	for _, b := range blobs {
		p := filepath.Join(config.DataRoot, "blobs", "sha256", b)
		if err := writeTarFile(tw, filepath.Join("blobs", "sha256", b), p); err != nil {
			return err
		}
		if _, err := os.Stat(p + ".index.json"); err == nil {
			if err := writeTarFile(tw, filepath.Join("blobs", "sha256", b+".index.json"), p+".index.json"); err != nil {
				return err
			}
		}
	}
	return nil
}

func LoadImage(inPath string) (string, error) {
	f, err := os.Open(inPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	tr := tar.NewReader(f)

	var meta imageArchiveMeta
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		target := filepath.Join(config.DataRoot, hdr.Name)
		if hdr.Name == "meta.json" {
			b, _ := io.ReadAll(tr)
			_ = json.Unmarshal(b, &meta)
			continue
		}
		if hdr.FileInfo().IsDir() {
			_ = os.MkdirAll(target, hdr.FileInfo().Mode())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", err
		}
		w, err := os.Create(target)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return "", err
		}
		w.Close()
	}
	if meta.Image == "" || meta.ManifestDigest == "" {
		return "", fmt.Errorf("invalid archive: missing meta")
	}

	// Upsert index entry.
	indexPath := filepath.Join(config.DataRoot, "index.json")
	var index models.OCIImageIndex
	if b, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(b, &index)
	} else {
		index.SchemaVersion = 2
	}
	manifestPath := filepath.Join(config.DataRoot, "blobs", "sha256", meta.ManifestDigest)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", err
	}
	size := int64(len(manifestData))
	updated := false
	for i, m := range index.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == meta.Image {
			index.Manifests[i].Digest = "sha256:" + meta.ManifestDigest
			index.Manifests[i].Size = size
			updated = true
		}
	}
	if !updated {
		index.Manifests = append(index.Manifests, models.OCIDescriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    "sha256:" + meta.ManifestDigest,
			Size:      size,
			Annotations: map[string]string{
				"org.opencontainers.image.ref.name": meta.Image,
			},
		})
	}
	idxData, _ := json.MarshalIndent(index, "", "  ")
	if err := os.WriteFile(indexPath, idxData, 0644); err != nil {
		return "", err
	}
	return meta.Image, nil
}

func writeTarBytes(tw *tar.Writer, name string, b []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0644, Size: int64(len(b))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func writeTarFile(tw *tar.Writer, name, src string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: name, Mode: 0644, Size: st.Size()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

