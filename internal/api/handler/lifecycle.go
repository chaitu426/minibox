package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chaitu426/mini-docker/internal/config"
	"github.com/chaitu426/mini-docker/internal/models"
	"github.com/chaitu426/mini-docker/internal/runtime"
	"github.com/chaitu426/mini-docker/internal/security"
	"github.com/chaitu426/mini-docker/internal/storage"
)

type ImageInfo struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

func ListImagesHandler(w http.ResponseWriter, r *http.Request) {
	indexPath := filepath.Join(config.DataRoot, "index.json")
	var images []ImageInfo

	data, err := os.ReadFile(indexPath)
	if err == nil {
		var index models.OCIImageIndex
		if json.Unmarshal(data, &index) == nil {
			for _, m := range index.Manifests {
				if name := m.Annotations["org.opencontainers.image.ref.name"]; name != "" {
					images = append(images, ImageInfo{
						Name:      name,
						SizeBytes: imageTotalSize(strings.TrimPrefix(m.Digest, "sha256:"), m.Size),
					})
				}
			}
		}
	}

	json.NewEncoder(w).Encode(images)
}

func imageTotalSize(manifestDigest string, manifestSize int64) int64 {
	if manifestDigest == "" {
		return 0
	}
	manifestPath := filepath.Join(config.DataRoot, "blobs", "sha256", manifestDigest)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return manifestSize
	}
	var manifest models.OCIManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return manifestSize
	}
	total := manifestSize
	total += manifest.Config.Size
	for _, l := range manifest.Layers {
		total += l.Size
	}
	return total
}

func RmiHandler(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	if image == "" {
		http.Error(w, "missing image", http.StatusBadRequest)
		return
	}
	if err := security.ValidImageName(image); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	indexPath := filepath.Join(config.DataRoot, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		http.Error(w, "failed to read index", http.StatusInternalServerError)
		return
	}

	var index struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			MediaType   string            `json:"mediaType"`
			Digest      string            `json:"digest"`
			Size        int64             `json:"size"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(data, &index); err != nil {
		http.Error(w, "corrupt index", http.StatusInternalServerError)
		return
	}

	removed := false
	var newManifests = make([]struct {
		MediaType   string            `json:"mediaType"`
		Digest      string            `json:"digest"`
		Size        int64             `json:"size"`
		Annotations map[string]string `json:"annotations"`
	}, 0)

	for _, m := range index.Manifests {
		if m.Annotations["org.opencontainers.image.ref.name"] == image {
			removed = true
		} else {
			newManifests = append(newManifests, m)
		}
	}

	if removed {
		index.Manifests = newManifests
		newData, _ := json.Marshal(index)
		os.WriteFile(indexPath, newData, 0644)
		w.Write([]byte("Image " + image + " removed\n"))
	} else {
		http.Error(w, "image not found", http.StatusNotFound)
	}
}

func SaveImageHandler(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	path := r.URL.Query().Get("path")
	if image == "" || path == "" {
		http.Error(w, "missing image or path", http.StatusBadRequest)
		return
	}
	if err := security.ValidImageName(image); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := storage.SaveImage(image, path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write([]byte("Saved image " + image + " to " + path + "\n"))
}

func LoadImageHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	img, err := storage.LoadImage(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write([]byte("Loaded image " + img + " from " + path + "\n"))
}

func SystemPruneHandler(w http.ResponseWriter, r *http.Request) {
	report, err := storage.PruneSystem()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to prune: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func StopContainerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if !security.ValidContainerID(id) {
		http.Error(w, "invalid container id", http.StatusBadRequest)
		return
	}
	containers := runtime.GetAllContainers()
	c, exists := containers[id]
	if !exists {
		http.Error(w, "container not found", http.StatusNotFound)
		return
	}
	if c.Status != "running" || c.PID <= 0 {
		w.Write([]byte("Container " + id + " not running\n"))
		return
	}

	timeoutSec := 10
	if v := r.URL.Query().Get("t"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 600 {
			timeoutSec = n
		}
	}

	_ = syscall.Kill(c.PID, syscall.SIGTERM)
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		// If pid doesn't exist, we consider it stopped.
		if err := syscall.Kill(c.PID, 0); err != nil {
			runtime.MarkContainerExited(id, 0)
			w.Write([]byte("Container " + id + " stopped\n"))
			return
		}
		time.Sleep(150 * time.Millisecond)
	}

	_ = syscall.Kill(c.PID, syscall.SIGKILL)
	runtime.MarkContainerExited(id, 137)
	w.Write([]byte("Container " + id + " killed (timeout)\n"))
}

func KillContainerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if !security.ValidContainerID(id) {
		http.Error(w, "invalid container id", http.StatusBadRequest)
		return
	}
	containers := runtime.GetAllContainers()
	c, exists := containers[id]
	if !exists {
		http.Error(w, "container not found", http.StatusNotFound)
		return
	}
	if c.PID <= 0 {
		http.Error(w, "container pid not found", http.StatusBadRequest)
		return
	}
	_ = syscall.Kill(c.PID, syscall.SIGKILL)
	runtime.MarkContainerExited(id, 137)
	w.Write([]byte("Container " + id + " killed\n"))
}

func RmContainerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	dir, err := security.ContainerDir(config.DataRoot, id)
	if err != nil {
		http.Error(w, "invalid container id", http.StatusBadRequest)
		return
	}
	
	if err := security.SafeToDelete(config.DataRoot, dir); err != nil {
		http.Error(w, "safety violation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	runtime.DeleteContainer(id)
	os.RemoveAll(dir)
	w.Write([]byte("Container " + id + " removed\n"))
}
