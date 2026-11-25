package pilaunch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxExecutableBytes = 256 << 20
	versionTimeout     = 15 * time.Second
	maxVersionBytes    = 512
)

type FileObservation struct {
	Path string
	Hash string
	Size int64
	Mode uint32
	UID  uint32
	Dev  uint64
	Ino  uint64
}

type Observation struct {
	Executable FileObservation
	Version    string
}

type Managed struct {
	Executable  string
	Observation Observation
}

func Resolve(argv0, path, coordinator string) (string, error) {
	resolved, err := lookPath(argv0, path)
	if err != nil {
		return "", errors.New("native Pi executable is unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("native Pi executable path is invalid")
	}
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", errors.New("native Pi executable cannot be resolved")
	}
	if coordinator != "" {
		self, selfErr := filepath.EvalSymlinks(coordinator)
		if selfErr == nil {
			if a, e := os.Stat(real); e == nil {
				if b, e2 := os.Stat(self); e2 == nil && os.SameFile(a, b) {
					return "", errors.New("native Pi resolves to coordinator")
				}
			}
		}
		if real == coordinator {
			return "", errors.New("native Pi resolves to coordinator")
		}
	}
	return resolved, nil
}

func lookPath(name, path string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("native Pi executable is unavailable")
}

func Observe(executable string) (FileObservation, error) {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return FileObservation{}, errors.New("native Pi executable cannot be resolved")
	}
	observed, err := observeFile(real, maxExecutableBytes)
	if err != nil {
		return FileObservation{}, errors.New("native Pi executable is unsafe")
	}
	return observed, nil
}

func readVersion(executable string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "--version").Output()
	if err != nil {
		return "", errors.New("native Pi --version failed")
	}
	if len(output) > maxVersionBytes {
		return "", errors.New("native Pi --version output is too long")
	}
	return strings.TrimSpace(string(output)), nil
}

func ObserveManaged(executable string) (Managed, error) {
	observed, err := Observe(executable)
	if err != nil {
		return Managed{}, err
	}
	version, err := readVersion(executable)
	if err != nil {
		return Managed{}, err
	}
	return Managed{Executable: executable, Observation: Observation{Executable: observed, Version: version}}, nil
}

func Recheck(managed Managed) error {
	current, err := Observe(managed.Executable)
	if err != nil {
		return err
	}
	if current != managed.Observation.Executable {
		return errors.New("managed Pi changed since observation")
	}
	return nil
}

func observeFile(path string, limit int64) (FileObservation, error) {
	var result FileObservation
	result, _, err := readObservedFile(path, limit)
	return result, err
}

func readObservedFile(path string, limit int64) (FileObservation, []byte, error) {
	var result FileObservation
	abs, err := filepath.Abs(path)
	if err != nil {
		return result, nil, err
	}
	before, err := os.Lstat(abs)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return result, nil, errors.New("unsafe metadata file")
	}
	fd, err := syscall.Open(abs, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, nil, err
	}
	f := os.NewFile(uintptr(fd), abs)
	defer f.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Mode&0022 != 0 || stat.Size > limit {
		return result, nil, errors.New("unsafe metadata ownership, type, or permissions")
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return result, nil, errors.New("metadata changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return result, nil, errors.New("metadata exceeds limit")
	}
	sum := sha256.Sum256(b)
	return FileObservation{Path: abs, Hash: hex.EncodeToString(sum[:]), Size: int64(len(b)), Mode: uint32(stat.Mode), UID: stat.Uid, Dev: uint64(stat.Dev), Ino: uint64(stat.Ino)}, b, nil
}
