package utils

import (
	"os"
	"path/filepath"
	"golang.org/x/sys/unix"
)

// CopyReflink attempts to perform an instant clone (reflink) of src to dst.
// This only works on Linux filesystems that support FICLONE (Btrfs, XFS, etc.).
func CopyReflink(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	// We need to preserve the original file mode
	info, err := s.Stat()
	if err != nil {
		return err
	}

	d, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer d.Close()

	// FICLONE is the ioctl that performs the reflink
	// If the filesystem doesn't support it, this returns ENOTSUP or EXDEV
	return unix.IoctlSetInt(int(d.Fd()), unix.FICLONE, int(s.Fd()))
}
