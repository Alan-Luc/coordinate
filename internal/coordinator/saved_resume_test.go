package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"coordinate/internal/pilaunch"
	"coordinate/internal/savedpi"
)

func TestResumeIntentDerivesOnlySavedSessionArguments(t *testing.T) {
	directory := t.TempDir()
	native := interactiveManagedFixture(t)
	snapshot := savedpi.Snapshot{
		CanonicalFile: "/tmp/saved-session.jsonl",
		NativeID:      "native-session",
		Cwd:           "/tmp",
		Device:        1,
		Inode:         2,
		Bytes:         3,
		SHA256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	intent := InteractiveIntent{
		Schema:          SchemaVersion,
		Kind:            "resume",
		TerminalProfile: TransparentTerminalProfile,
		NativePi:        &native,
		Resume:          &SavedResume{OperationID: "123e4567-e89b-42d3-a456-426614174001", Target: snapshot, SessionDir: directory},
	}
	if err := writeJSON(directory, "intent.json", intent); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(directory, "role.json", JobRole{Schema: SchemaVersion, Role: "standalone"}); err != nil {
		t.Fatal(err)
	}
	if err := validateLaunchKind(directory, InteractiveProtocolVersion); err != nil {
		t.Fatalf("resume launch intent rejected: %v", err)
	}
	if err := validateLaunchKind(directory, ProtocolVersion); err == nil {
		t.Fatal("legacy bootstrap accepted a resume intent")
	}
	got, err := readResumeIntent(directory)
	if err != nil {
		t.Fatalf("resume intent rejected: %v", err)
	}
	if got.Kind != "resume" || got.Resume == nil || got.Resume.Target != snapshot {
		t.Fatalf("resume intent = %+v", got)
	}
	arguments, err := readResumeArguments(directory)
	if err != nil {
		t.Fatalf("readResumeArguments: %v", err)
	}
	want := []string{"--session", snapshot.CanonicalFile, "--session-dir", directory}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("resume arguments = %#v, want %#v", arguments, want)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["argv_sha256"]; exists {
		t.Fatal("resume intent must not carry an arbitrary argv hash")
	}
}

func TestStartResumedRejectsChangedSnapshotBeforeLaunch(t *testing.T) {
	snapshot := resumeFixture(t, t.TempDir(), "native-changed")
	if err := os.WriteFile(snapshot.CanonicalFile, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{}
	_, err := coordinator.StartResumed(context.Background(), snapshot, pilaunch.Managed{}, "")
	var action *ActionError
	if !errors.As(err, &action) || action.Diagnostic.Code != "saved_session_changed" {
		t.Fatalf("StartResumed error = %v; want saved_session_changed", err)
	}
}

func TestStartResumedUsesReservedExactNativeArguments(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("coordinator launch requires macOS")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable for managed-layout fixture")
	}
	root := t.TempDir()
	capturePath := filepath.Join(root, "argv.json")
	t.Setenv("ARGV_CAPTURE", capturePath)
	managed := interactiveManagedFixture(t)
	snapshot := resumeFixture(t, filepath.Join(root, "saved"), "native-resumed")
	store, err := openStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	coordinatorPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{store: store, options: Options{TmuxPath: tmuxPath, Executable: coordinatorPath, Width: 80, Height: 24}}
	var jobID string
	t.Cleanup(func() {
		if idPattern.MatchString(jobID) {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = coordinator.Forget(cleanupCtx, jobID, true)
		}
	})
	facts, err := coordinator.StartResumed(context.Background(), snapshot, managed, root)
	if err != nil {
		t.Fatalf("StartResumed: %v", err)
	}
	jobID = facts.ID
	manifest, launch, err := store.load(facts.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Cwd != snapshot.Cwd || launch.Phase != "exec-intent" {
		t.Fatalf("resumed manifest/launch = %+v / %+v", manifest, launch)
	}
	job := filepath.Join(store.root, "jobs", facts.ID)
	intent, err := readResumeIntent(job)
	if err != nil || intent.Resume.Target != snapshot || intent.Resume.SessionDir != root {
		t.Fatalf("resume intent = %+v, error = %v", intent.Resume, err)
	}
	if _, err := os.Stat(filepath.Join(job, "launch-argv.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume created an argv payload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(job, "initial-task.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume created a task input: %v", err)
	}
	reservation, err := coordinator.readResumeReservation(intent.Resume.OperationID)
	if err != nil || reservation.Initial.JobID != facts.ID || reservation.Initial.Name != facts.Name || reservation.Initial.Process == nil || launch.Pane == nil || !sameIdentity(*reservation.Initial.Process, *launch.Pane) {
		t.Fatalf("resume reservation = %+v, error = %v", reservation, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(capturePath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	encoded, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("fake Pi did not receive arguments: %v", err)
	}
	var arguments []string
	if err := json.Unmarshal(encoded, &arguments); err != nil {
		t.Fatal(err)
	}
	want := []string{"--session", snapshot.CanonicalFile, "--session-dir", root}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("native argv = %#v, want %#v", arguments, want)
	}
}

func TestResumeNativeObservationRequiresExactSavedIdentity(t *testing.T) {
	target := resumeFixture(t, t.TempDir(), "native-mismatch")
	observation := NativeSessionObservation{
		SessionID:   target.NativeID,
		SessionFile: target.CanonicalFile,
		Cwd:         target.Cwd,
		State:       "persisted",
		File:        &NativeFileIdentity{Device: target.Device, Inode: target.Inode},
		HeaderID:    target.NativeID,
	}
	if err := validateResumeNativeObservation(target, observation); err != nil {
		t.Fatalf("matching native observation rejected: %v", err)
	}
	for _, mutate := range []func(*NativeSessionObservation){
		func(value *NativeSessionObservation) { value.SessionFile += ".other" },
		func(value *NativeSessionObservation) { value.SessionID += "-other" },
		func(value *NativeSessionObservation) { value.Cwd += "/other" },
		func(value *NativeSessionObservation) { value.File.Inode++ },
	} {
		changed := observation
		changed.File = &NativeFileIdentity{Device: observation.File.Device, Inode: observation.File.Inode}
		mutate(&changed)
		if err := validateResumeNativeObservation(target, changed); err == nil {
			t.Fatal("mismatched native identity was accepted")
		}
	}
}

func TestReadNativeSessionForTargetBindsResumeReservation(t *testing.T) {
	root := t.TempDir()
	snapshot := resumeFixture(t, filepath.Join(root, "saved"), "native-readiness")
	store, err := openStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "123e4567-e89b-42d3-a456-426614174011"
	const operationID = "123e4567-e89b-42d3-a456-426614174012"
	const generation = "123e4567-e89b-42d3-a456-426614174013"
	name := "resume-native"
	job := filepath.Join(store.root, "jobs", jobID)
	if err := os.Mkdir(job, 0700); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{store: store}
	if err := coordinator.ensureReservationRoot(); err != nil {
		t.Fatal(err)
	}
	process, err := identifyProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	managed := interactiveManagedFixture(t)
	manifest := Manifest{Schema: SchemaVersion, ID: jobID, Name: name, Owner: store.token, Protocol: InteractiveProtocolVersion}
	launch := Launch{Schema: SchemaVersion, Phase: "exec-intent", Pane: &process}
	now := time.Now().UTC()
	status := SessionStatus{Protocol: ControlProtocol, LaunchProtocol: InteractiveProtocolVersion, JobID: jobID, Owner: store.token, SessionID: snapshot.NativeID, Generation: generation, PID: process.PID, UpdatedAt: now.UnixMilli(), Activity: "idle"}
	observation := NativeSessionObservation{Schema: SchemaVersion, JobID: jobID, Owner: store.token, SessionID: snapshot.NativeID, Generation: generation, Process: process, UpdatedAt: now, SessionFile: snapshot.CanonicalFile, Cwd: snapshot.Cwd, State: "persisted", File: &NativeFileIdentity{Device: snapshot.Device, Inode: snapshot.Inode}, HeaderID: snapshot.NativeID}
	intent := InteractiveIntent{Schema: SchemaVersion, Kind: "resume", TerminalProfile: TransparentTerminalProfile, NativePi: &managed, Resume: &SavedResume{OperationID: operationID, Target: snapshot}}
	reservation := ResumeReservation{Schema: 1, OperationID: operationID, Target: snapshot, Phase: "launched", Initial: &ResumeLaunchActor{JobID: jobID, Name: name, Launcher: process, Process: &process}}
	for file, value := range map[string]interface{}{"manifest.json": manifest, "launch.json": launch, "session.json": status, "native-session.json": observation, "intent.json": intent, "role.json": JobRole{Schema: SchemaVersion, Role: "standalone"}} {
		if err := writeJSON(job, file, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(coordinator.reservationRoot(), operationID+".json", reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeSessionForTarget(job, manifest, launch, status, now); err != nil {
		t.Fatalf("matching resumed native session rejected: %v", err)
	}
	fresh := observation
	fresh.SessionID = "new-after-resume"
	fresh.SessionFile = filepath.Join(snapshot.Cwd, "new-after-resume.jsonl")
	fresh.State, fresh.HeaderID, fresh.File = "unpersisted", "", nil
	freshStatus := status
	freshStatus.SessionID = fresh.SessionID
	if err := writeJSON(job, "native-session.json", fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeSessionForTarget(job, manifest, launch, freshStatus, now); err == nil {
		t.Fatal("unverified initial resume allowed a new identity")
	}
	history := nativeHistory{Schema: 1, Observations: []NativeSessionObservation{observation}}
	if err := writeJSON(job, "native-session-history.json", history); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeSessionForTarget(job, manifest, launch, freshStatus, now); err != nil {
		t.Fatalf("native /new after verified resume rejected: %v", err)
	}
	history.Observations[0].Process.StartSec++
	if err := writeJSON(job, "native-session-history.json", history); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeSessionForTarget(job, manifest, launch, freshStatus, now); err == nil {
		t.Fatal("another process birth authorized new-session control")
	}
	observation.Cwd += "/different"
	if err := writeJSON(job, "native-session.json", observation); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeSessionForTarget(job, manifest, launch, status, now); err == nil {
		t.Fatal("native session with a different cwd was accepted")
	}
}

func TestResumeIntentRejectsInteractivePayload(t *testing.T) {
	directory := t.TempDir()
	native := interactiveManagedFixture(t)
	intent := InteractiveIntent{
		Schema:          SchemaVersion,
		Kind:            "resume",
		TerminalProfile: TransparentTerminalProfile,
		NativePi:        &native,
		Resume: &SavedResume{OperationID: "123e4567-e89b-42d3-a456-426614174001", Target: savedpi.Snapshot{
			CanonicalFile: "/tmp/saved-session.jsonl",
			NativeID:      "native-session",
			Cwd:           "/tmp",
			Device:        1,
			Inode:         2,
			Bytes:         3,
			SHA256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	}
	if err := writeJSON(directory, "intent.json", intent); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(directory, "role.json", JobRole{Schema: SchemaVersion, Role: "standalone"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "launch-argv.json"), []byte(`{"schema":1,"argv":["--help"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateLaunchKind(directory, InteractiveProtocolVersion); err == nil {
		t.Fatal("resume launch accepted an arbitrary interactive payload")
	}
}
