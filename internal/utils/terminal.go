package utils

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// StartPTY allocates a new PTY (master/slave pair) for interactive sessions.
func StartPTY() (master *os.File, slave *os.File, err error) {
	masterfd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(masterfd), "/dev/ptmx")

	// unlockpt: clear the lock on the slave side
	var n int
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(masterfd), unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&n))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCSPTLCK: %w", errno)
	}

	// getpts: find the slave name
	var ptn uint32
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(masterfd), unix.TIOCGPTN, uintptr(unsafe.Pointer(&ptn))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCGPTN: %w", errno)
	}
	slaveName := fmt.Sprintf("/dev/pts/%d", ptn)

	// Open the slave side
	slavefd, err := unix.Open(slaveName, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slaveName, err)
	}
	slave = os.NewFile(uintptr(slavefd), slaveName)

	return master, slave, nil
}

// SetRaw puts a terminal into raw mode (useful for the CLI side).
func SetRaw(fd uintptr) (*unix.Termios, error) {
	termios, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	if err != nil {
		return nil, err
	}

	old := *termios
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB
	termios.Cflag |= unix.CS8
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(int(fd), unix.TCSETS, termios); err != nil {
		return nil, err
	}

	return &old, nil
}

// Restore resets the terminal state.
func Restore(fd uintptr, old *unix.Termios) error {
	return unix.IoctlSetTermios(int(fd), unix.TCSETS, old)
}
