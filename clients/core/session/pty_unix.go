// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package session

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func startPTY(argv []string, rows, cols int) (*os.File, *exec.Cmd, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, nil, err
	}
	if err := resizePTY(master, rows, cols); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // G204: direct exec of an allowlisted peer, no shell string
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, nil, err
	}
	_ = slave.Close()
	return master, cmd, nil
}

func resizePTY(master *os.File, rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return nil
	}
	if rows > 1000 {
		rows = 1000
	}
	if cols > 1000 {
		cols = 1000
	}
	return unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})
}
