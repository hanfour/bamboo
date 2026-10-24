// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package session

import (
	"errors"
	"os"
	"os/exec"
)

func startPTY(_ []string, _, _ int) (*os.File, *exec.Cmd, error) {
	return nil, nil, errors.New("pty is not supported on this platform")
}

func resizePTY(_ *os.File, _, _ int) error {
	return errors.New("pty is not supported on this platform")
}
