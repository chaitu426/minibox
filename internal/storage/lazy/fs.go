package lazy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/chaitu426/minibox/internal/config"
	"github.com/chaitu426/minibox/internal/storage"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func StartLazyMount(blobPath, mountDir, cacheDir string, index *storage.LayerIndex) error {
	os.MkdirAll(mountDir, 0755)
	os.MkdirAll(cacheDir, 0755)

	// Check for stale FUSE mount
	if _, err := os.Stat(mountDir); err != nil {
		errStr := err.Error()
		if strings.Contains(strings.ToLower(errStr), "not connected") || strings.Contains(errStr, "Permission denied") {
			exec.Command("fusermount3", "-uz", mountDir).Run()
			syscall.Unmount(mountDir, syscall.MNT_FORCE|syscall.MNT_DETACH)
		}
	} else {
		// ALready mounted?
		if _, err := os.Stat(filepath.Join(mountDir, ".minibox_fuse")); err == nil {
			return nil
		}
		// If stat succeeds but it's empty, we should unmount just in case it's broken
		exec.Command("fusermount3", "-uz", mountDir).Run()
		syscall.Unmount(mountDir, syscall.MNT_FORCE|syscall.MNT_DETACH)
	}

	root := NewLazyRoot(blobPath, cacheDir, index)
	server, err := fs.Mount(mountDir, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: true,
			Debug:      true,
		},
	})
	if err != nil {
		fmt.Printf("FUSE Mount Error: %v\n", err)
		return err
	}

	go server.Wait()
	return nil
}

type LazyRoot struct {
	fs.Inode
	BlobPath  string
	CacheDir  string
	Index     *storage.LayerIndex
	extracted map[string]bool
	mu        sync.Mutex
}

func NewLazyRoot(blobPath, cacheDir string, index *storage.LayerIndex) *LazyRoot {
	root := &LazyRoot{
		BlobPath:  blobPath,
		CacheDir:  cacheDir,
		Index:     index,
		extracted: make(map[string]bool),
	}
	return root
}

func (r *LazyRoot) OnAdd(ctx context.Context) {
	// Virtual verification node
	fuseFile := r.NewPersistentInode(ctx, &fs.MemRegularFile{Data: []byte("active")}, fs.StableAttr{Mode: fuse.S_IFREG | 0644})
	r.AddChild(".minibox_fuse", fuseFile, true)

	// Build the tree from the index
	for _, f := range r.Index.Files {
		dir, name := filepath.Split(f.Name)
		parent := r.getOrCreateDir(ctx, dir)

		stable := r.getStableAttr(f)
		id := parent.NewPersistentInode(ctx, &LazyFile{
			root: r,
			info: f,
		}, stable)
		parent.AddChild(name, id, false)
	}
}

func (r *LazyRoot) getStableAttr(f storage.FileIndex) fs.StableAttr {
	mode := uint32(f.Mode)
	if f.Type == tar.TypeDir {
		mode |= syscall.S_IFDIR
	} else if f.Type == tar.TypeReg {
		mode |= syscall.S_IFREG
	} else if f.Type == tar.TypeSymlink {
		mode |= syscall.S_IFLNK
	}
	return fs.StableAttr{
		Mode: mode,
	}
}

func (r *LazyRoot) getOrCreateDir(ctx context.Context, path string) *fs.Inode {
	if path == "" || path == "." || path == "/" {
		return &r.Inode
	}

	dir, name := filepath.Split(filepath.Clean(path))
	parent := r.getOrCreateDir(ctx, dir)

	child := parent.GetChild(name)
	if child == nil {
		id := parent.NewPersistentInode(ctx, &fs.Inode{}, fs.StableAttr{Mode: syscall.S_IFDIR | 0755})
		parent.AddChild(name, id, false)
		return id
	}
	return child
}

type LazyFile struct {
	fs.Inode
	root *LazyRoot
	info storage.FileIndex
}

func (f *LazyFile) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	cachePath := filepath.Join(f.root.CacheDir, f.info.Name)

	// Check if already cached (on disk)
	if _, err := os.Stat(cachePath); err != nil {
		f.root.mu.Lock()
		// Double check under lock
		if !f.root.extracted[f.info.Name] {
			if err := f.root.extractFile(f.info.Name); err != nil {
				f.root.mu.Unlock()
				fmt.Printf("[lazy] extract failed name=%s err=%v\n", f.info.Name, err)
				return nil, 0, syscall.EIO
			}
		}
		f.root.mu.Unlock()
	}

	fd, err := syscall.Open(cachePath, int(flags), 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return fs.NewLoopbackFile(fd), 0, 0
}

func (r *LazyRoot) extractFile(targetName string) error {
	file, err := os.Open(r.BlobPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	found := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(r.CacheDir, header.Name)

		// Opportunistic extraction: if we encounter a file that isn't cached yet,
		// and it's small or it's the target, extract it.
		if header.Name == targetName || !r.extracted[header.Name] {
			if header.Typeflag == tar.TypeDir {
				os.MkdirAll(target, 0755)
			} else if header.Typeflag == tar.TypeSymlink {
				os.MkdirAll(filepath.Dir(target), 0755)
				os.Remove(target)
				os.Symlink(header.Linkname, target)
			} else if header.Typeflag == tar.TypeReg {
				os.MkdirAll(filepath.Dir(target), 0755)
				outFile, err := os.Create(target)
				if err == nil {
					io.Copy(outFile, tr)
					outFile.Close()
					os.Chmod(target, os.FileMode(header.Mode))
				}
			}
			r.extracted[header.Name] = true
		}

		if header.Name == targetName {
			found = true
			// We can stop if we want, but continuing a bit might help other files.
			// For now, let's stop to minimize latency for this specific Open call.
			break
		}
	}

	if !found {
		return fmt.Errorf("file %s not found in layer", targetName)
	}
	return nil
}

func (f *LazyFile) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return []byte(f.info.Linkname), 0
}

func (f *LazyFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Uid = uint32(config.SubUIDBase)
	out.Gid = uint32(config.SubUIDBase)
	out.Size = uint64(f.info.Size)
	out.Mode = uint32(f.info.Mode)
	if f.info.Type == tar.TypeDir {
		out.Mode |= syscall.S_IFDIR
	} else if f.info.Type == tar.TypeSymlink {
		out.Mode |= syscall.S_IFLNK
	} else {
		out.Mode |= syscall.S_IFREG
	}
	return 0
}

func (r *LazyRoot) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Uid = uint32(config.SubUIDBase)
	out.Gid = uint32(config.SubUIDBase)
	out.Mode = syscall.S_IFDIR | 0755
	return 0
}
