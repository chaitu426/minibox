package utils

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Allocate PTY (master/slave).
func StartPTY() (master *os.File, slave *os.File, err error) {
	masterfd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(masterfd), "/dev/ptmx")

	// Unlock slave.
	var n int
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(masterfd), unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&n))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCSPTLCK: %w", errno)
	}

	// Get slave name.
	var ptn uint32
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(masterfd), unix.TIOCGPTN, uintptr(unsafe.Pointer(&ptn))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("ioctl TIOCGPTN: %w", errno)
	}
	slaveName := fmt.Sprintf("/dev/pts/%d", ptn)

	// Open slave.
	slavefd, err := unix.Open(slaveName, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slaveName, err)
	}
	slave = os.NewFile(uintptr(slavefd), slaveName)

	return master, slave, nil
}

// Set terminal to raw mode.
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

// Restore terminal state.
func Restore(fd uintptr, old *unix.Termios) error {
	return unix.IoctlSetTermios(int(fd), unix.TCSETS, old)
}
