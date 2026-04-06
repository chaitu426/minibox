package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chaitu426/mini-docker/internal/config"
	"github.com/chaitu426/mini-docker/internal/daemon"
	"github.com/chaitu426/mini-docker/internal/network"
	"github.com/chaitu426/mini-docker/internal/runtime"
	"github.com/chaitu426/mini-docker/internal/storage"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		runtime.RunContainer()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runtime.RunInit()
		return
	}

	// Ensure DataRoot and necessary directories exist
	dirs := []string{
		config.DataRoot,
		filepath.Join(config.DataRoot, "images"),
		filepath.Join(config.DataRoot, "containers"),
		filepath.Join(config.DataRoot, "layers"),
		filepath.Join(config.DataRoot, "base_layers"),
		filepath.Join(config.DataRoot, "tmp"),
		filepath.Join(config.DataRoot, "blobs", "sha256"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Printf("Error creating directory %s: %v\n", dir, err)
		}
	}

	// Phase 4: Auto-index blobs for lazy loading
	indexExistingBlobs()

	// Set up the host bridge network for container isolation
	if err := network.SetupBridge(); err != nil {
		fmt.Printf("Warning: network bridge setup failed: %v\n", err)
	}

	d := daemon.NewDaemon()
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stopCh
		fmt.Println("\n[daemon] shutdown requested")
		_ = d.Shutdown(context.Background())
	}()

	if err := d.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "daemon exited: %v\n", err)
		os.Exit(1)
	}
}

func indexExistingBlobs() {
	blobsPath := filepath.Join(config.DataRoot, "blobs", "sha256")
	files, err := os.ReadDir(blobsPath)
	if err != nil {
		return
	}
	fmt.Printf("➜ Indexing blobs in %s for lazy loading...\n", blobsPath)
	for _, f := range files {
		if !f.IsDir() && !strings.HasSuffix(f.Name(), ".index.json") {
			if err := storage.IndexLayer(filepath.Join(blobsPath, f.Name())); err != nil {
				fmt.Printf("Error indexing blob %s: %v\n", f.Name(), err)
			}
		}
	}

	// Also index alpine
	alpinePath := filepath.Join(config.DataRoot, "alpine.tar.gz")
	if _, err := os.Stat(alpinePath); err == nil {
		if err := storage.IndexLayer(alpinePath); err != nil {
			fmt.Printf("Error indexing alpine: %v\n", err)
		}
	}
}
