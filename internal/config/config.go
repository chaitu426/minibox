package config

import (
	"os"
	"strconv"
	"strings"
)

// DataRoot is the persistent state directory for images, containers, and blobs.
var DataRoot = "/var/lib/mini-docker"

// HTTPAddr is the bind address for the API daemon (default loopback-only).
var HTTPAddr = "127.0.0.1:8080"

// BuildPathPrefixes lists filesystem roots allowed as build context directories.
// Set MINI_DOCKER_BUILD_PREFIXES to a comma-separated list (e.g. "/home,/tmp,/src").
var BuildPathPrefixes []string

// SubUIDBase is the first host UID/GID used for user-namespace mapping (Docker rootless style).
// Container UID 0 maps to SubUIDBase on the host, not to root (UID 0).
var SubUIDBase = 100000

// SubUIDCount is the size of the single [container → host] ID map (default 65536).
var SubUIDCount = 65536

// APIToken, if non-empty, requires Authorization: Bearer <token> or X-API-Token on every request.
var APIToken string

// EncryptionKey is used to encrypt container metadata at rest. Expected 32-byte hex string.
var EncryptionKey string

func init() {
	if v := os.Getenv("MINI_DOCKER_DATA_ROOT"); v != "" {
		DataRoot = strings.TrimSpace(v)
	}
	if v := os.Getenv("MINI_DOCKER_HTTP_ADDR"); v != "" {
		HTTPAddr = strings.TrimSpace(v)
	}
	if v := os.Getenv("MINI_DOCKER_BUILD_PREFIXES"); v != "" {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				BuildPathPrefixes = append(BuildPathPrefixes, p)
			}
		}
	}
	if len(BuildPathPrefixes) == 0 {
		BuildPathPrefixes = []string{
			"/home",
			"/tmp",
			"/var/lib/mini-docker",
			"/root",
			"/srv",
			"/opt",
			"/usr/local/src",
		}
	}
	if v := os.Getenv("MINI_DOCKER_SUBUID_BASE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			SubUIDBase = n
		}
	}
	if v := os.Getenv("MINI_DOCKER_SUBUID_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			SubUIDCount = n
		}
	}
	APIToken = strings.TrimSpace(os.Getenv("MINI_DOCKER_API_TOKEN"))
	EncryptionKey = strings.TrimSpace(os.Getenv("MINI_DOCKER_ENCRYPTION_KEY"))
}
