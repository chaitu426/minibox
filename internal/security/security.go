package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 8-digit hex IDs.
var containerIDRe = regexp.MustCompile(`^[a-f0-9]{8}$`)
var volumeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

var (
	ErrInvalidContainerID = errors.New("invalid container id")
	ErrPathEscape         = errors.New("path escapes allowed directory")
	ErrBuildPathNotAllowed = errors.New("build context path is not under an allowed prefix")
)

// Validate ID format.
func ValidContainerID(id string) bool {
	return containerIDRe.MatchString(id)
}

// Validate volume name.
func ValidVolumeName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("invalid volume name")
	}
	if !volumeNameRe.MatchString(name) {
		return fmt.Errorf("invalid volume name")
	}
	return nil
}

// Get container absolute path.
func ContainerDir(dataRoot, id string) (string, error) {
	if !ValidContainerID(id) {
		return "", ErrInvalidContainerID
	}
	return filepath.Abs(filepath.Join(dataRoot, "containers", id))
}

// Resolve file inside container dir.
func ContainerFile(dataRoot, id string, parts ...string) (string, error) {
	dir, err := ContainerDir(dataRoot, id)
	if err != nil {
		return "", err
	}
	p := filepath.Join(append([]string{dir}, parts...)...)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if abs != dir && !strings.HasPrefix(abs+string(filepath.Separator), dir+string(filepath.Separator)) {
		return "", ErrPathEscape
	}
	return abs, nil
}

// Resolve path under allowed prefixes.
func ResolveAllowedPath(path string, allowedPrefixes []string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("build context must be a directory")
	}
	for _, prefix := range allowedPrefixes {
		prefix = filepath.Clean(prefix)
		prefixAbs, err := filepath.Abs(prefix)
		if err != nil {
			continue
		}
		if abs == prefixAbs || strings.HasPrefix(abs+string(filepath.Separator), prefixAbs+string(filepath.Separator)) {
			return abs, nil
		}
	}
	return "", ErrBuildPathNotAllowed
}

// Validate image name.
func ValidImageName(name string) error {
	if name == "" || len(name) > 256 {
		return fmt.Errorf("invalid image name")
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("invalid image name")
	}
	return nil
}

// Validate TCP port.
func ValidHostPort(s string) error {
	if s == "" {
		return fmt.Errorf("empty port")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return fmt.Errorf("non-numeric port")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port out of range")
	}
	return nil
}

// Protected system paths.
var forbiddenPaths = []string{
	"/", "/home", "/root", "/usr", "/etc", "/var", "/boot", "/bin", "/sbin", "/dev", "/proc", "/sys", "/run",
}

// Safety check before delete.
func SafeToDelete(dataRoot, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	for _, fp := range forbiddenPaths {
		if abs == fp || abs == filepath.Clean(fp) {
			return fmt.Errorf("refusing to delete protected system path: %s", abs)
		}
	}

	rootAbs, err := filepath.Abs(dataRoot)
	if err != nil {
		return err
	}

	if abs == rootAbs {
		return fmt.Errorf("refusing to delete the entire data root: %s", abs)
	}

	if !strings.HasPrefix(abs+string(filepath.Separator), rootAbs+string(filepath.Separator)) {
		return fmt.Errorf("path escapes allowed data directory: %s", abs)
	}

	return nil
}
