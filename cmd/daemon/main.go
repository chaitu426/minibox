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

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/daemon"
	"github.com/chaitu426/minibox/internal/network"
	"github.com/chaitu426/minibox/internal/runtime"
	"github.com/chaitu426/minibox/internal/storage"
	"github.com/chaitu426/minibox/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("miniboxd %s\n", version.Version)
		return
	}
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

	// Fast startup: don't block on indexing or bridge bring-up.
	// - Disable startup indexing with MINIBOX_INDEX_ON_STARTUP=0
	// - Disable startup bridge bring-up with MINIBOX_BRIDGE_ON_STARTUP=0
	if os.Getenv("MINIBOX_INDEX_ON_STARTUP") != "0" {
		go indexExistingBlobs()
	}
	if os.Getenv("MINIBOX_BRIDGE_ON_STARTUP") != "0" {
		go func() {
			if err := network.SetupBridge(); err != nil {
				fmt.Printf("[warn] network bridge setup failed: %v\n", err)
			}
		}()
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
	fmt.Printf("[startup] indexing blobs in %s\n", blobsPath)
	for _, f := range files {
		if !f.IsDir() && !strings.HasSuffix(f.Name(), ".index.json") {
			if err := storage.IndexLayer(filepath.Join(blobsPath, f.Name())); err != nil {
				fmt.Printf("[warn] index blob=%s err=%v\n", f.Name(), err)
			}
		}
	}

	// Also index alpine
	alpinePath := filepath.Join(config.DataRoot, "alpine.tar.gz")
	if _, err := os.Stat(alpinePath); err == nil {
		if err := storage.IndexLayer(alpinePath); err != nil {
			fmt.Printf("[warn] index alpine err=%v\n", err)
		}
	}
}
