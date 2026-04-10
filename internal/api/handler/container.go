package handler

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

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
	ShmSize      int               `json:"shm_size"`      // /dev/shm MB; 0 → runtime default (256 MB)
	Interactive  bool              `json:"interactive"`   // Whether to allocate a PTY and stream stdin
	Detached     bool              `json:"detached"`
	PortMap      map[string]string `json:"ports"` // hostPort → containerPort
	Volumes      map[string]string `json:"volumes"`
	NamedVolumes map[string]string `json:"named_volumes"` // volumeName -> containerPath
	Env          []string          `json:"env"`
	User         string            `json:"user"`
	Name         string            `json:"name"`
	Project      string            `json:"project"`
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

	dec := json.NewDecoder(r.Body)
	err := dec.Decode(&req)
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

	// Command resolution (OCI/Docker spec):
	// 1. If user provides command, it usually appends to/overrides depending on Entrypoint.
	// 2. We always resolve image config to find Entrypoint/Cmd defaults.
	imgConfig, err := runtime.ResolveImageConfig(req.Image)
	if err == nil {
		if len(req.Command) > 0 {
			// If image has Entrypoint, user Command appends to it.
			if len(imgConfig.Config.Entrypoint) > 0 {
				req.Command = append(imgConfig.Config.Entrypoint, req.Command...)
			}
		} else {
			// If no user Command, use Entrypoint + Cmd
			req.Command = append(imgConfig.Config.Entrypoint, imgConfig.Config.Cmd...)
		}
	}

	if len(req.Command) == 0 {
		http.Error(w, "no command provided and image has no default command", http.StatusBadRequest)
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
		ShmSize:     req.ShmSize,
		User:        req.User,
	}

	if req.Detached {
		output, err := runtime.RunCommand(r.Context(), containerID, req.Image, opts, true, req.PortMap, req.Volumes, req.Env, req.Command, req.Name, req.Project)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(output)
		return
	}

	if req.Interactive {
		// Hijack the connection for raw TCP interaction
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "webserver doesn't support hijacking", http.StatusInternalServerError)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()

		// Send success header manually since we've hijacked the connection
		bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n")
		bufrw.Flush()

		// Combine any data already buffered by the JSON decoder with the hijacked connection
		stdin := io.MultiReader(dec.Buffered(), conn)
		err = runtime.RunCommandInteractive(r.Context(), containerID, req.Image, opts, req.PortMap, req.Volumes, req.Env, req.Command, stdin, conn, req.Name, req.Project)
		if err != nil {
			fmt.Fprintf(conn, "\n[error] %v\n", err)
		}
		return
	}

	// Non-interactive foreground: stream output to client as it arrives
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	output, err2 := runtime.RunCommand(r.Context(), containerID, req.Image, opts, false, req.PortMap, req.Volumes, req.Env, req.Command, req.Name, req.Project)
	if err2 != nil {
		fmt.Fprintf(w, "\n[error] %v\n", err2)
		return
	}
	w.Write(output)
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
	follow := r.URL.Query().Get("follow") == "1"

	logPath, err := security.ContainerFile(config.DataRoot, id, "container.log")
	if err != nil {
		http.Error(w, "invalid container id", http.StatusBadRequest)
		return
	}

	if !follow {
		http.ServeFile(w, r, logPath)
		return
	}

	// Follow mode: tail the file
	f, err := os.Open(logPath)
	if err != nil {
		http.Error(w, "failed to open log", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	
	// Seek to end or start? For compose logs, usually we show existing then tail.
	// But to keep it simple, let's just start from current position.
	
	for {
		line, err := io.Copy(w, f)
		if err != nil && err != io.EOF {
			break
		}
		if line > 0 && flusher != nil {
			flusher.Flush()
		}
		
		// Check if request context is done
		select {
		case <-r.Context().Done():
			return
		default:
			// Poor man's tail: sleep and retry on EOF
			if err == io.EOF {
				// Check if container is still running? 
				// For now, just keep tailing until client disconnects.
				time.Sleep(200 * time.Millisecond)
				continue
			}
		}
	}
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
