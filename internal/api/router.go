package api

import (
	"net/http"

	"github.com/chaitu426/minibox/internal/api/handler"
)

// maxBytes wraps a handler with a request body size limit.
func maxBytes(n int64, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		h(w, r)
	}
}

func NewRouter() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", handler.PingHandler)

	mux.HandleFunc("POST /containers/run", maxBytes(4<<20, handler.RunContainerHandler))
	mux.HandleFunc("POST /containers/build", maxBytes(64<<20, handler.BuildImageHandler))

	mux.HandleFunc("GET /containers", handler.ListContainersHandler)
	mux.HandleFunc("GET /containers/logs", handler.LogsContainerHandler)
	mux.HandleFunc("GET /containers/stats", handler.GetStatsHandler)

	mux.HandleFunc("GET /images", handler.ListImagesHandler)
	mux.HandleFunc("POST /images/save", handler.SaveImageHandler)
	mux.HandleFunc("POST /images/load", handler.LoadImageHandler)

	mux.HandleFunc("POST /images/remove", handler.RmiHandler)
	mux.HandleFunc("POST /containers/stop", handler.StopContainerHandler)
	mux.HandleFunc("POST /containers/kill", handler.KillContainerHandler)
	mux.HandleFunc("POST /containers/remove", handler.RmContainerHandler)
	mux.HandleFunc("POST /system/prune", handler.SystemPruneHandler)

	return requireAPIToken(secureHeaders(mux))
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
