package utils

import (
	"io"
	"os"
	"os/exec"
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

// CopyRecursive copies a file, directory, or symlink from src to dst with an optional ignore filter.
func CopyRecursive(src, dst string, ignore func(string) bool) error {
	if ignore != nil && ignore(src) {
		return nil
	}

	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	// ADVANCED OPTIMIZATIONS (Docker-like)
	if info.IsDir() {
		// Optimization 1: Parallel Copy Method (Worker Pool)
		if err := CopyRecursiveParallel(src, dst, ignore); err == nil {
			return nil
		}
		// Fallback to manual copyDir if parallel fails
	}

	if info.Mode().IsRegular() {
		// Optimization 2: Reflink (FICLONE) for regular files
		// Since we already checked `ignore(src)` at the top, a single regular file is safe to copy
		err := CopyReflink(src, dst)
		if err == nil {
			return nil
		}
	}

	// Optimization 3: System 'cp -aT' as a general fast fallback on Linux
	// ONLY safe if there is absolutely no ignore list!
	if ignore == nil {
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		cmd := exec.Command("cp", "-aT", src, dst)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return copySymlink(src, dst)
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

		if entry.Type()&os.ModeSymlink != 0 {
			if err := copySymlink(sp, dp); err != nil {
				return err
			}
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

func copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	// Remove destination if it exists (to avoid "file exists" error on symlink creation)
	os.Remove(dst)
	return os.Symlink(target, dst)
}
