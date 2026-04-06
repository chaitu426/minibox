package api

import (
	"net/http"
	"strings"

	"github.com/chaitu426/mini-docker/internal/config"
)

// requireAPIToken returns middleware that enforces MINI_DOCKER_API_TOKEN when set.
func requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := config.APIToken
		if tok == "" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == tok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-API-Token") == tok {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="mini-docker"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}
