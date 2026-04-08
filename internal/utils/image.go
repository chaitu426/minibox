package utils

import (
	"strings"
)

// ImageRef represents a parsed OCI image reference.
type ImageRef struct {
	Registry string
	Repo     string
	Tag      string
}

// ParseImageRef splits an image string (e.g., "ubuntu:latest", "quay.io/coreos/etcd")
// into its registry, repository, and tag components.
func ParseImageRef(ref string) ImageRef {
	r := ImageRef{
		Registry: "registry-1.docker.io",
		Tag:      "latest",
	}

	// 1. Separate tag if present
	if strings.Contains(ref, ":") {
		parts := strings.SplitN(ref, ":", 2)
		ref = parts[0]
		r.Tag = parts[1]
	}

	// 2. Determine registry and repository
	parts := strings.Split(ref, "/")
	
	// If the first part looks like a domain name (contains a dot or colon)
	// it's considered a registry.
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		r.Registry = parts[0]
		r.Repo = strings.Join(parts[1:], "/")
	} else {
		// Default to Docker Hub library for official images
		r.Repo = ref
		if !strings.Contains(r.Repo, "/") {
			r.Repo = "library/" + r.Repo
		}
	}

	return r
}
