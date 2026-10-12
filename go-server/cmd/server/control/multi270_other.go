//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func cleanupOwnedClient270(proc *os.Process) string {
	if proc != nil {
		_ = proc.Release()
	}
	return "unconfirmed"
}

func startClient270(_ context.Context, cmd *exec.Cmd, multi bool, bind func(int) error) (*os.Process, error) {
	if multi {
		return nil, errors.New("start client: multi270 UNSUPPORTED_PLATFORM")
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start client: %w", err)
	}
	if err := bind(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	return cmd.Process, nil
}
