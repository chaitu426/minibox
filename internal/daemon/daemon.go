package daemon

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/chaitu426/mini-docker/internal/api"
	"github.com/chaitu426/mini-docker/internal/config"
	"github.com/chaitu426/mini-docker/internal/network"
)

type Daemon struct {
	server *http.Server
}

func NewDaemon() *Daemon {
	return &Daemon{
		server: &http.Server{
			Addr:    config.HTTPAddr,
			Handler: api.NewRouter(),
		},
	}
}

func (d *Daemon) Start() error {
	fmt.Println("Daemon listening on", config.HTTPAddr)
	return d.server.ListenAndServe()
}

func (d *Daemon) Shutdown(ctx context.Context) error {
	shCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = network.TeardownBridge()
	return d.server.Shutdown(shCtx)
}
