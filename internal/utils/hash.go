package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// GetHash returns a SHA256 hash of the given string
func GetHash(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// HashFile returns a SHA256 hash of a file's content
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashDir returns a SHA256 hash of a directory tree (names and content)
func HashDir(path string) (string, error) {
	h := sha256.New()

	var files []string
	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Sort files to ensure deterministic hashing
	sort.Strings(files)

	for _, f := range files {
		// Write relative path to hash
		rel, _ := filepath.Rel(path, f)
		h.Write([]byte(rel))

		// Write file content hash
		fhash, err := HashFile(f)
		if err != nil {
			return "", err
		}
		h.Write([]byte(fhash))
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// CalculateDigest returns a SHA256 hex string of the given data
func CalculateDigest(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// MapToEnvSlice converts a map of env vars to a standard string slice (KEY=VALUE)
func MapToEnvSlice(env map[string]string) []string {
	var res []string
	for k, v := range env {
		res = append(res, k+"="+v)
	}
	return res
}
