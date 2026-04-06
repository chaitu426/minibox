package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/chaitu426/mini-docker/internal/builder"
	"github.com/chaitu426/mini-docker/internal/config"
	"github.com/chaitu426/mini-docker/internal/parser"
	"github.com/chaitu426/mini-docker/internal/security"
)

type BuildRequest struct {
	ImageName      string `json:"image"`
	MiniBoxContent string `json:"minibox"`
	Context        string `json:"context"`
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return
}

func BuildImageHandler(w http.ResponseWriter, r *http.Request) {
	var req BuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	tmp, err := os.CreateTemp("", "MiniBox-*")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())

	if strings.TrimSpace(req.MiniBoxContent) == "" {
		http.Error(w, "MiniBox content is required", http.StatusBadRequest)
		return
	}
	if _, err := tmp.WriteString(req.MiniBoxContent); err != nil {
		http.Error(w, "Failed to write temp MiniBox", http.StatusInternalServerError)
		return
	}
	tmp.Close()

	cfile, err := parser.ParseBoxfile(tmp.Name())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := security.ValidImageName(req.ImageName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	buildRoot, err := security.ResolveAllowedPath(req.Context, config.BuildPathPrefixes)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid build context: %v", err), http.StatusBadRequest)
		return
	}
	req.Context = buildRoot

	flusher, _ := w.(http.Flusher)
	fw := &flushWriter{w: w, f: flusher}
	w.Header().Set("Transfer-Encoding", "chunked")

	if err := builder.BuildImage(r.Context(), cfile, req.ImageName, req.Context, fw); err != nil {
		fmt.Fprintf(fw, "Build failed: %v\n", err)
		return
	}
	w.Write([]byte("Build completed successfully\n"))
}
