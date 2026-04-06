package utils

import (
	"io"
	"os"
	"path/filepath"
)

// CopyFile copies a single file from src to dst.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// CopyRecursive copies a file or directory from src to dst with an optional ignore filter.
func CopyRecursive(src, dst string, ignore func(string) bool) error {
	if ignore != nil && ignore(src) {
		return nil
	}

	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return copyDir(src, dst, ignore)
	}
	return CopyFile(src, dst)
}

func copyDir(src, dst string, ignore func(string) bool) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		sp := filepath.Join(src, entry.Name())
		dp := filepath.Join(dst, entry.Name())

		if ignore != nil && ignore(sp) {
			continue
		}

		if entry.IsDir() {
			if err := copyDir(sp, dp, ignore); err != nil {
				return err
			}
		} else {
			if err := CopyFile(sp, dp); err != nil {
				return err
			}
		}
	}
	return nil
}
