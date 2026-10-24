// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package session

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func openPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	closeMaster := true
	defer func() {
		if closeMaster {
			_ = master.Close()
		}
	}()
	fd := int(master.Fd())
	if err = unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return nil, nil, err
	}
	if err = unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return nil, nil, err
	}
	var name [128]byte
	// TIOCPTYGNAME writes the slave path into name. x/sys exports no libc wrapper for that request.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))) //nolint:staticcheck,gosec // SA1019,G103: direct ioctl is the available call
	if errno != 0 {
		return nil, nil, errno
	}
	slave, err := os.OpenFile(cstring(name[:]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	closeMaster = false
	return master, slave, nil
}
