package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/chaitu426/minibox/internal/runtime"
)

type ExecRequest struct {
	ContainerID string   `json:"id"`
	Command     []string `json:"command"`
	Interactive bool     `json:"interactive"`
}

func ExecContainerHandler(w http.ResponseWriter, r *http.Request) {
	var req ExecRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.ContainerID == "" {
		http.Error(w, "Container ID is required", http.StatusBadRequest)
		return
	}
	if len(req.Command) == 0 {
		http.Error(w, "Command is required", http.StatusBadRequest)
		return
	}

	// Resolve PID from container ID
	containers := runtime.GetAllContainers()
	c, ok := containers[req.ContainerID]
	if !ok {
		http.Error(w, "Container not found", http.StatusNotFound)
		return
	}
	if c.Status != "running" {
		http.Error(w, "Container is not running", http.StatusBadRequest)
		return
	}

	if req.Interactive {
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

		bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n")
		bufrw.Flush()

		stdin := io.MultiReader(dec.Buffered(), conn)
		err = runtime.ExecCommandInteractive(r.Context(), c.PID, req.Command, stdin, conn)
		if err != nil {
			fmt.Fprintf(conn, "\n[error] exec failed: %v\n", err)
		}
		return
	}

	// Non-interactive exec
	output, err := runtime.ExecCommand(r.Context(), c.PID, req.Command)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(output)
}
