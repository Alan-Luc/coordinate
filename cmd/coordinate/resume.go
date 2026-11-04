package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"coordinate/internal/coordinator"
	"coordinate/internal/pilaunch"
	"coordinate/internal/savedpi"
)

func resumePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Abs(path)
}

func readResumeConfirmation(input io.Reader) bool {
	answer, err := bufio.NewReader(io.LimitReader(input, 256)).ReadString('\n')
	return err == nil && strings.TrimSuffix(strings.TrimSuffix(answer, "\n"), "\r") == "RESUME"
}

func confirmSavedResume(snapshot savedpi.Snapshot) (bool, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("resume requires controlling-terminal confirmation")
	}
	defer terminal.Close()
	if _, _, err := coordinator.TerminalSize(int(terminal.Fd())); err != nil {
		return false, fmt.Errorf("resume requires controlling-terminal confirmation")
	}
	if _, err := fmt.Fprintf(terminal, "Resume saved Pi conversation?\nFile: %q\nNative ID: %q\nProject: %q\nKnown live writers are refused. Other Pi instances and state roots cannot be excluded.\nClose every other writer of this conversation. Type RESUME to confirm: ", snapshot.CanonicalFile, snapshot.NativeID, snapshot.Cwd); err != nil {
		return false, err
	}
	confirmed := readResumeConfirmation(terminal)
	if !confirmed {
		_, err = fmt.Fprintln(terminal, "Resume cancelled.")
	}
	return confirmed, err
}

func prepareSavedResume(ctx context.Context, route pilaunch.Classification, managed pilaunch.Managed, cwd string, in *os.File, out, errOut io.Writer) (savedpi.Snapshot, string, bool, error) {
	var snapshot savedpi.Snapshot
	sessionDir := route.SessionDir
	if sessionDir != "" {
		var err error
		sessionDir, err = resumePath(sessionDir)
		if err != nil {
			return snapshot, "", false, err
		}
	}
	path := route.SessionFile
	if path == "" {
		var err error
		path, err = pilaunch.SelectSavedSession(ctx, managed, cwd, sessionDir, in, out, errOut)
		if err != nil {
			return snapshot, "", false, err
		}
		if path == "" {
			_, err = fmt.Fprintln(errOut, "No session selected")
			return snapshot, "", false, err
		}
	} else {
		var err error
		path, err = resumePath(path)
		if err != nil {
			return snapshot, "", false, err
		}
	}
	if err := pilaunch.Recheck(managed); err != nil {
		return snapshot, "", false, err
	}
	var err error
	snapshot, err = savedpi.Validate(path)
	if err != nil {
		return snapshot, "", false, fmt.Errorf("saved session refused: %w", err)
	}
	confirmed, err := confirmSavedResume(snapshot)
	return snapshot, sessionDir, confirmed, err
}
