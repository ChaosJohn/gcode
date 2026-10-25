//go:build darwin

// Raw terminal mode for darwin via stdlib syscall (no cgo).
package tui

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	ioctlGetTermios = 0x40487413 // TIOCGETA
	ioctlSetTermios = 0x40487414 // TIOCSETA
	ioctlGetWinsize = 0x40087468 // TIOCGWINSZ
)

type termState struct {
	saved syscall.Termios
}

// enableRaw puts the terminal into raw mode.
func enableRaw(fd uintptr) (*termState, error) {
	var saved syscall.Termios
	if err := ioctl(fd, ioctlGetTermios, unsafe.Pointer(&saved)); err != nil {
		return nil, err
	}
	raw := saved
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK |
		syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, ioctlSetTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return &termState{saved: saved}, nil
}

// disableRaw restores cooked mode.
func disableRaw(fd uintptr, state *termState) error {
	if state == nil {
		return nil
	}
	saved := state.saved
	return ioctl(fd, ioctlSetTermios, unsafe.Pointer(&saved))
}

// windowSize returns the terminal columns and rows.
func windowSize(fd uintptr) (cols, rows int, err error) {
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	if err = ioctl(fd, ioctlGetWinsize, unsafe.Pointer(&ws)); err != nil {
		return 80, 24, err
	}
	return int(ws.Col), int(ws.Row), nil
}

func ioctl(fd uintptr, request uint, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request),
		uintptr(arg))
	if errno != 0 {
		return errno
	}
	_ = os.Stdin
	return nil
}
