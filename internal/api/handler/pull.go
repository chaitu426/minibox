package handler

import (
	"fmt"
	"net/http"

	"github.com/chaitu426/minibox/internal/builder"
)

func PullImageHandler(w http.ResponseWriter, r *http.Request) {
	imageName := r.URL.Query().Get("image")
	if imageName == "" {
		http.Error(w, "Image name is required", http.StatusBadRequest)
		return
	}

	flusher, _ := w.(http.Flusher)
	fw := &flushWriter{w: w, f: flusher}
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Content-Type", "text/plain")

	if err := builder.PullOCIImage(imageName, fw); err != nil {
		fmt.Fprintf(fw, "[error] pull failed: %v\n", err)
		return
	}

	fmt.Fprintf(fw, "[pull] Done\n")
}
