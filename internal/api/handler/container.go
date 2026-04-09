package handler

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/runtime"
	"github.com/chaitu426/minibox/internal/security"
)

type RunRequest struct {
	Image        string            `json:"image"`
	Command      []string          `json:"command"`
	MemoryMB     int               `json:"memory"`
	CPUMax       int               `json:"cpu"`
	CPUSet       string            `json:"cpuset"`        // e.g. "0,1"
	IOWeight     int               `json:"io_weight"`     // 1-1000
	OOMScoreAdj  int               `json:"oom_score_adj"` // -1000 to 1000
	Sysctls      map[string]string `json:"sysctls"`
	DBMode       bool              `json:"db_mode"`
	Detached     bool              `json:"detached"`
	PortMap      map[string]string `json:"ports"` // hostPort → containerPort
	Volumes      map[string]string `json:"volumes"`
	NamedVolumes map[string]string `json:"named_volumes"` // volumeName -> containerPath
	Env          []string          `json:"env"`
}

func generateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func validatePortMap(m map[string]string) error {
	for hp, cp := range m {
		if err := security.ValidHostPort(hp); err != nil {
			return fmt.Errorf("host port %q: %w", hp, err)
		}
		if err := security.ValidHostPort(cp); err != nil {
			return fmt.Errorf("container port %q: %w", cp, err)
		}
	}
	return nil
}

func RunContainerHandler(w http.ResponseWriter, r *http.Request) {
	var req RunRequest

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Image == "" {
		http.Error(w, "Image name is required", http.StatusBadRequest)
		return
	}
	if err := security.ValidImageName(req.Image); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validatePortMap(req.PortMap); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Command) == 0 {
		if imgConfig, err := runtime.ResolveImageConfig(req.Image); err == nil {
			// OCI spec: Entrypoint + Cmd = the full execution command
			req.Command = append(imgConfig.Config.Entrypoint, imgConfig.Config.Cmd...)
		}
	}
	if len(req.Command) == 0 {
		http.Error(w, "no command provided and image has no default command; pass a command (e.g. `minibox run <image> <cmd...>` or `minibox db run <image> <cmd...>`)", http.StatusBadRequest)
		return
	}

	if req.PortMap == nil {
		req.PortMap = map[string]string{}
	}
	if req.Volumes == nil {
		req.Volumes = map[string]string{}
	}
	if req.NamedVolumes == nil {
		req.NamedVolumes = map[string]string{}
	}
	if req.Sysctls == nil {
		req.Sysctls = map[string]string{}
	}

	// Resolve daemon-managed named volumes into host paths under DataRoot/volumes.
	// This makes DB-style persistent containers safer and consistent for users.
	for name, cpath := range req.NamedVolumes {
		if err := security.ValidVolumeName(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if cpath == "" || cpath[0] != '/' {
			http.Error(w, "invalid container volume path", http.StatusBadRequest)
			return
		}
		hostPath := filepath.Join(config.DataRoot, "volumes", name)
		if err := os.MkdirAll(hostPath, 0755); err != nil {
			http.Error(w, "failed to create volume", http.StatusInternalServerError)
			return
		}
		req.Volumes[hostPath] = cpath
	}

	containerID := generateID()

	opts := runtime.ContainerOptions{
		MemoryMB:    req.MemoryMB,
		CPUMax:      req.CPUMax,
		CPUSet:      req.CPUSet,
		IOWeight:    req.IOWeight,
		OOMScoreAdj: req.OOMScoreAdj,
		Sysctls:     req.Sysctls,
		DBMode:      req.DBMode,
	}

	if req.Detached {
		output, err := runtime.RunCommand(r.Context(), containerID, req.Image, opts, true, req.PortMap, req.Volumes, req.Env, req.Command)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(output)
		return
	}

	// Foreground: stream output to client as it arrives
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sw := &streamWriter{w: w, f: flusher}
	runErr := runtime.RunCommandStream(r.Context(), containerID, req.Image, opts, req.PortMap, req.Volumes, req.Env, req.Command, sw)
	if runErr != nil {
		fmt.Fprintf(w, "Error: %v\n", runErr)
		flusher.Flush()
	}
}

type streamWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (s *streamWriter) Write(p []byte) (n int, err error) {
	if s.w == nil {
		return 0, fmt.Errorf("response writer is nil")
	}
	n, err = s.w.Write(p)
	if err != nil {
		return n, err
	}
	if s.f != nil {
		// Use a local variable to avoid race if s.f is cleared (though unlikely here)
		f := s.f
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Recovered from panic in flusher.Flush: %v\n", r)
			}
		}()
		f.Flush()
	}
	return
}

func ListContainersHandler(w http.ResponseWriter, r *http.Request) {
	containers := runtime.GetAllContainers()
	json.NewEncoder(w).Encode(containers)
}

func LogsContainerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	logPath, err := security.ContainerFile(config.DataRoot, id, "container.log")
	if err != nil {
		http.Error(w, "invalid container id", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, logPath)
}
func GetStatsHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing container id", http.StatusBadRequest)
		return
	}
	if !security.ValidContainerID(id) {
		http.Error(w, "invalid container id", http.StatusBadRequest)
		return
	}

	stats, err := runtime.GetContainerStats(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(stats)
}
