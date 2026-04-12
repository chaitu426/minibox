package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type copyTask struct {
	src string
	dst string
}

// Parallel directory copy.
func CopyRecursiveParallel(src, dst string, ignore func(string) bool) error {
	numWorkers := runtime.NumCPU() * 2
	if numWorkers > 16 {
		numWorkers = 16
	}

	tasks := make(chan copyTask, 1000)
	errChan := make(chan error, 1)
	var wg sync.WaitGroup

	// Start worker pool.
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range tasks {
				if err := copyIndividual(task.src, task.dst); err != nil {
					select {
					case errChan <- err:
					default:
					}
					return
				}
			}
		}()
	}

	// Walk and send tasks.
	go func() {
		defer close(tasks)
		err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if ignore != nil && ignore(path) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			destPath := filepath.Join(dst, rel)

			if info.IsDir() {
				return os.MkdirAll(destPath, 0755)
			}

			// Queue task.
			select {
			case tasks <- copyTask{path, destPath}:
			case <-errChan: // Stop if a worker failed
				return fmt.Errorf("worker failure")
			}
			return nil
		})
		if err != nil && err.Error() != "worker failure" {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	// Wait until done.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

func copyIndividual(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return copySymlink(src, dst)
	}

	if info.Mode().IsRegular() {
		// Try reflink (FICLONE).
		if err := CopyReflink(src, dst); err == nil {
			return nil
		}
		// Byte copy fallback.
		return CopyFile(src, dst)
	}

	return nil
}
