package utils

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"bufio"
	"path/filepath"
)

// ExtractTarGz extracts a gzip compressed tarball directly into a destination directory.
func ExtractTarGz(gzipStream string, dest string) error {
	file, err := os.Open(gzipStream)
	if err != nil {
		return err
	}
	defer file.Close()

	uncompressedStream, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer uncompressedStream.Close()

	tarReader := tar.NewReader(uncompressedStream)
	return ExtractTarStream(tarReader, dest)
}

// ExtractTarStream extracts a tar stream from a reader into a destination directory.
func ExtractTarStream(tr *tar.Reader, dest string) error {
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target) // ignore error
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target) // ignore error
			linkTarget := filepath.Join(dest, header.Linkname)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// CopyDirTar performs a high-speed streaming copy of a directory using native tar.
// It accepts an optional ignore function to skip specific files or directories.
func CopyDirTar(src, dst string, ignore func(string) bool) error {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)

	// Writer goroutine: walks the source and Writes to the pipe
	go func() {
		tw := tar.NewWriter(pw)
		defer pw.Close()
		defer tw.Close()

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

			if path == src {
				return nil
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			header.Name = rel

			if info.Mode()&os.ModeSymlink != 0 {
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				header.Linkname = link
			}

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			if !info.Mode().IsRegular() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(tw, f)
			return err
		})
		errCh <- err
	}()

	// Main thread extracted from the reader end of the pipe
	tr := tar.NewReader(pr)
	extractErr := ExtractTarStream(tr, dst)

	walkErr := <-errCh
	if extractErr != nil {
		return extractErr
	}
	return walkErr
}

// CreateTarGz creates a gzip compressed tarball from a source directory.
func CreateTarGz(src string, writers ...io.Writer) error {
	mw := io.MultiWriter(writers...)
	
	// Add buffering to minimize syscalls during compression and hashing
	bw := bufio.NewWriterSize(mw, 128*1024) // 128KB buffer
	defer bw.Flush()

	gw, err := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	if err != nil {
		return err
	}
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if path == src {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		header.Name = relPath

		// Handle symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			header.Linkname = link
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(tw, file)
		return err
	})
}
