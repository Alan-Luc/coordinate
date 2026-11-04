package coordinator

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"coordinate/internal/savedpi"
	"golang.org/x/sys/unix"
)

type NativeFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type NativeSessionObservation struct {
	Schema      int                 `json:"schema"`
	JobID       string              `json:"job_id"`
	Owner       string              `json:"owner"`
	SessionID   string              `json:"session_id"`
	Generation  string              `json:"generation"`
	Process     ProcessIdentity     `json:"process"`
	UpdatedAt   time.Time           `json:"updated_at"`
	SessionFile string              `json:"session_file"`
	Cwd         string              `json:"cwd"`
	State       string              `json:"state"`
	File        *NativeFileIdentity `json:"file,omitempty"`
	HeaderID    string              `json:"header_id,omitempty"`
}

func (c *Coordinator) validateJobSessionStatus(manifest Manifest, launch Launch, status SessionStatus, now time.Time) error {
	if launch.Pane == nil || validateSessionStatus(status, manifest.ID, manifest.Owner, launch.Pane.PID, now) != nil || !sessionLaunchProtocolMatches(status, manifest.Protocol) {
		return errors.New("session status does not match its launch")
	}
	if manifest.Protocol == InteractiveProtocolVersion {
		_, err := readNativeSessionForTarget(filepath.Join(c.store.root, "jobs", manifest.ID), manifest, launch, status, now)
		return err
	}
	return nil
}

func readNativeSessionForTarget(directory string, manifest Manifest, launch Launch, status SessionStatus, now time.Time) (NativeSessionObservation, error) {
	var observation NativeSessionObservation
	if err := readJSON(filepath.Join(directory, "native-session.json"), &observation); err != nil {
		return observation, err
	}
	if manifest.Protocol != InteractiveProtocolVersion || status.LaunchProtocol != InteractiveProtocolVersion || launch.Pane == nil || validateSessionStatus(status, manifest.ID, manifest.Owner, launch.Pane.PID, now) != nil || observation.Schema != SchemaVersion || observation.JobID != manifest.ID || observation.Owner != manifest.Owner || observation.SessionID != status.SessionID || observation.Generation != status.Generation || !sameIdentity(observation.Process, *launch.Pane) || observation.UpdatedAt.IsZero() || now.Sub(observation.UpdatedAt) > controlStatusMaxAge || observation.UpdatedAt.After(now.Add(time.Second)) {
		return observation, errors.New("native observation identity or freshness mismatch")
	}
	cwd, err := canonicalCwd(observation.Cwd)
	if err != nil || cwd != observation.Cwd || !filepath.IsAbs(observation.SessionFile) || filepath.Clean(observation.SessionFile) != observation.SessionFile {
		return observation, errors.New("native observation paths are not canonical")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(observation.SessionFile))
	if err != nil || parent != filepath.Dir(observation.SessionFile) {
		return observation, errors.New("native session parent is not canonical")
	}
	if err := validateNativeResumeTarget(directory, manifest, launch, observation); err != nil {
		return observation, err
	}
	if observation.State == "unpersisted" {
		if observation.File != nil || observation.HeaderID != "" {
			return observation, errors.New("unpersisted observation has file identity")
		}
		if _, err := os.Lstat(observation.SessionFile); !errors.Is(err, os.ErrNotExist) {
			return observation, errors.New("unpersisted session path is not absent")
		}
		return observation, nil
	}
	if observation.State != "persisted" || observation.File == nil || observation.HeaderID != observation.SessionID {
		return observation, errors.New("invalid persisted native observation")
	}
	fd, err := unix.Open(observation.SessionFile, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return observation, err
	}
	file := os.NewFile(uintptr(fd), observation.SessionFile)
	defer file.Close()
	var identity unix.Stat_t
	if err := unix.Fstat(fd, &identity); err != nil || identity.Mode&unix.S_IFMT != unix.S_IFREG || identity.Mode&0022 != 0 || identity.Uid != uint32(os.Getuid()) || identity.Nlink != 1 || uint64(identity.Dev) != observation.File.Device || identity.Ino != observation.File.Inode {
		return observation, errors.New("native session file identity mismatch")
	}
	line, err := bufio.NewReader(io.LimitReader(file, 4097)).ReadBytes('\n')
	if err != nil || len(line) > 4096 {
		return observation, errors.New("native session header unavailable")
	}
	var header struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		ID      string `json:"id"`
		Cwd     string `json:"cwd"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session" || header.Version != 3 || header.ID != observation.SessionID || header.Cwd != observation.Cwd {
		return observation, errors.New("native session header mismatch")
	}
	var fresh unix.Stat_t
	if err := unix.Lstat(observation.SessionFile, &fresh); err != nil || fresh.Mode&unix.S_IFMT != unix.S_IFREG || fresh.Dev != identity.Dev || fresh.Ino != identity.Ino {
		return observation, errors.New("native session path changed during validation")
	}
	return observation, nil
}

func validateNativeResumeTarget(directory string, manifest Manifest, launch Launch, observation NativeSessionObservation) error {
	intent, err := readProtocol2Intent(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if intent.Kind == "interactive" {
		return nil
	}
	intent, err = readResumeIntent(directory)
	if err != nil {
		return errors.New("unsupported native session intent")
	}
	reservation, err := readResumeReservationAt(filepath.Dir(filepath.Dir(directory)), intent.Resume.OperationID)
	if err != nil || reservation.Phase != "launched" || reservation.Initial.JobID != manifest.ID || reservation.Initial.Name != manifest.Name || reservation.Initial.Process == nil || launch.Pane == nil || !sameIdentity(*reservation.Initial.Process, *launch.Pane) || !snapshotMatches(reservation.Target, intent.Resume.Target) {
		return errors.New("native session does not match its saved reservation")
	}
	target := intent.Resume.Target
	if validateResumeNativeObservation(target, observation) == nil {
		return nil
	}
	if observation.SessionID == target.NativeID || observation.SessionFile == target.CanonicalFile {
		return errors.New("native session does not match the saved target")
	}
	history, err := readNativeHistory(filepath.Join(directory, "native-session-history.json"))
	if err != nil {
		return err
	}
	for _, previous := range history.Observations {
		if previous.JobID == manifest.ID && previous.Owner == manifest.Owner && sameIdentity(previous.Process, *launch.Pane) && validateResumeNativeObservation(target, previous) == nil {
			return nil
		}
	}
	return errors.New("initial saved session was not observed by this runtime")
}

func validateResumeNativeObservation(target savedpi.Snapshot, observation NativeSessionObservation) error {
	if validateSnapshot(target) != nil || observation.State != "persisted" || observation.File == nil || observation.SessionFile != target.CanonicalFile || observation.SessionID != target.NativeID || observation.Cwd != target.Cwd || observation.HeaderID != target.NativeID || observation.File.Device != target.Device || observation.File.Inode != target.Inode {
		return errors.New("native session does not match the saved target")
	}
	return nil
}
