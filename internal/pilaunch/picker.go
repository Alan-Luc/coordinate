package pilaunch

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxPickerResultBytes = 4096
const maxSettingsBytes = 1 << 20

//go:embed picker.mjs
var pickerScript []byte

func SelectSavedSession(ctx context.Context, managed Managed, cwd string, sessionDir string, in *os.File, out io.Writer, errOut io.Writer) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if in == nil || out == nil || errOut == nil {
		return "", errors.New("session picker requires terminal input and output")
	}
	if err := Recheck(managed); err != nil {
		return "", fmt.Errorf("managed Pi observation changed: %w", err)
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", errors.New("session picker working directory is invalid")
	}
	if sessionDir != "" && !filepath.IsAbs(sessionDir) {
		sessionDir = filepath.Join(cwd, sessionDir)
	}
	agentDir := agentDirectory()
	environmentSessionDir := os.Getenv("PI_CODING_AGENT_SESSION_DIR")
	configuredDir := ""
	if sessionDir == "" && environmentSessionDir == "" {
		configuredDir, err = configuredSessionDir(cwd, agentDir)
		if err != nil {
			return "", err
		}
	}
	lookupDir := resolveSessionDirectory(cwd, sessionDir, environmentSessionDir, agentDir, configuredDir)
	allSessionsDir := ""
	if sessionDir != "" || environmentSessionDir != "" || configuredDir != "" {
		allSessionsDir = lookupDir
	}
	nodePath, nodeObservation, err := observeNode(ctx)
	if err != nil {
		return "", err
	}
	nodeModules := filepath.Dir(filepath.Dir(filepath.Dir(managed.Observation.Package.Path)))
	codingAgentModule := filepath.Join(filepath.Dir(managed.Observation.Package.Path), "dist", "index.js")
	tuiModule := filepath.Join(nodeModules, "@earendil-works", "pi-tui", "dist", "index.js")
	moduleObservations := make(map[string]FileObservation, 2)
	for _, module := range []string{codingAgentModule, tuiModule} {
		observation, observeErr := observeFile(module, 16<<20)
		if observeErr != nil {
			return "", errors.New("managed Pi picker modules are unavailable or unsafe")
		}
		moduleObservations[module] = observation
	}
	resultRead, resultWrite, err := os.Pipe()
	if err != nil {
		return "", errors.New("session picker result channel unavailable")
	}
	defer resultRead.Close()

	cmd := exec.Command(nodePath, "--input-type=module", "-e", string(pickerScript), codingAgentModule, tuiModule, cwd, lookupDir, allSessionsDir, agentDir)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.ExtraFiles = []*os.File{resultWrite}
	if err := cmd.Start(); err != nil {
		resultWrite.Close()
		return "", fmt.Errorf("start native session picker: %w", err)
	}
	resultWrite.Close()
	resultChannel := make(chan struct {
		path string
		err  error
	}, 1)
	go func() {
		path, decodeErr := decodePickerResult(resultRead)
		resultChannel <- struct {
			path string
			err  error
		}{path, decodeErr}
	}()
	waitChannel := make(chan error, 1)
	go func() { waitChannel <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitChannel:
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case waitErr = <-waitChannel:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			waitErr = <-waitChannel
		}
		return "", ctx.Err()
	}
	result := <-resultChannel
	if waitErr != nil {
		return "", fmt.Errorf("native session picker failed: %w", waitErr)
	}
	if result.err != nil {
		return "", result.err
	}
	if err := recheckNode(nodePath, nodeObservation); err != nil {
		return "", err
	}
	if err := Recheck(managed); err != nil {
		return "", fmt.Errorf("managed Pi changed during session selection: %w", err)
	}
	for module, before := range moduleObservations {
		after, err := observeFile(module, 16<<20)
		if err != nil || after != before {
			return "", errors.New("managed Pi picker modules changed during session selection")
		}
	}
	return result.path, nil
}

func decodePickerResult(reader io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxPickerResultBytes+1))
	if err != nil {
		return "", errors.New("cannot read native session picker result")
	}
	if len(body) == 0 || len(body) > maxPickerResultBytes {
		return "", errors.New("native session picker result is empty or oversized")
	}
	var result struct {
		Path json.RawMessage `json:"path"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || result.Path == nil {
		return "", errors.New("native session picker result is malformed")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("native session picker result has trailing data")
	}
	if string(result.Path) == "null" {
		return "", nil
	}
	var path string
	if err := json.Unmarshal(result.Path, &path); err != nil || path == "" || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("native session picker returned an invalid path")
	}
	return path, nil
}

func observeNode(ctx context.Context) (string, FileObservation, error) {
	path, err := lookPath("node", os.Getenv("PATH"))
	if err != nil {
		return "", FileObservation{}, errors.New("safe Node runtime unavailable for native session picker")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", FileObservation{}, errors.New("Node runtime path is invalid")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", FileObservation{}, errors.New("Node runtime cannot be resolved")
	}
	observation, err := observeFile(real, 128<<20)
	if err != nil || observation.UID != uint32(os.Getuid()) || observation.Mode&0022 != 0 || observation.Mode&0111 == 0 {
		return "", FileObservation{}, errors.New("Node runtime is not a safe current-owner executable")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, real, "--version")
	command.WaitDelay = time.Second
	command.Stdin = nil
	version, err := command.Output()
	if err != nil {
		return "", FileObservation{}, errors.New("Node runtime version could not be checked")
	}
	if !supportedNodeVersion(strings.TrimSpace(string(version))) {
		return "", FileObservation{}, errors.New("native session picker requires Node.js 22.19 or newer")
	}
	return real, observation, nil
}

func recheckNode(path string, before FileObservation) error {
	after, err := observeFile(path, 128<<20)
	if err != nil || after != before {
		return errors.New("Node runtime changed during session selection")
	}
	return nil
}

func supportedNodeVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 || len(parts[0]) < 2 || parts[0][0] != 'v' {
		return false
	}
	major, err := strconv.Atoi(parts[0][1:])
	minor, minorErr := strconv.Atoi(parts[1])
	return err == nil && minorErr == nil && digits(parts[1]) && digits(parts[2]) && (major > 22 || major == 22 && minor >= 19)
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func agentDirectory() string {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(string(filepath.Separator), "nonexistent-agent-dir")
		}
		dir = filepath.Join(home, ".pi", "agent")
	} else if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Join(string(filepath.Separator), "nonexistent-agent-dir")
	}
	return absolute
}

func resolveSessionDirectory(cwd, sessionDir, environment, agentDir, configuredDir string) string {
	path := sessionDir
	if path == "" {
		path = environment
	}
	if path == "" {
		path = configuredDir
	}
	if path == "" {
		path = filepath.Join(agentDir, "sessions", encodedCwd(cwd))
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Join(string(filepath.Separator), "nonexistent-session-dir")
	}
	return absolute
}

func configuredSessionDir(cwd, agentDir string) (string, error) {
	for _, path := range []string{filepath.Join(cwd, ".pi", "settings.json"), filepath.Join(agentDir, "settings.json")} {
		body, err := readSettings(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("settings file is unsafe: %s", path)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(body, &values); err != nil || values == nil {
			return "", fmt.Errorf("settings file is malformed: %s", path)
		}
		raw, exists := values["sessionDir"]
		if !exists {
			continue
		}
		var sessionDir string
		if err := json.Unmarshal(raw, &sessionDir); err != nil {
			return "", fmt.Errorf("sessionDir setting is invalid: %s", path)
		}
		if sessionDir == "" {
			continue
		}
		if strings.HasPrefix(sessionDir, "~/") {
			home, _ := os.UserHomeDir()
			sessionDir = filepath.Join(home, sessionDir[2:])
		}
		if !filepath.IsAbs(sessionDir) {
			sessionDir = filepath.Join(cwd, sessionDir)
		}
		return sessionDir, nil
	}
	return "", nil
}

func readSettings(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSettingsBytes {
		return nil, errors.New("settings file is unsafe")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0022 != 0 || stat.Size > maxSettingsBytes {
		return nil, errors.New("settings file is unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil || len(body) > maxSettingsBytes {
		return nil, errors.New("settings file exceeds limit")
	}
	return body, nil
}

func encodedCwd(cwd string) string {
	return "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimLeft(cwd, "/\\")) + "--"
}
