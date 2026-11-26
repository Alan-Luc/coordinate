package pilaunch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/term"
)

func TestDecodePickerResult(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
		fail bool
	}{
		{name: "selected", body: `{"path":"/sessions/a.jsonl"}`, want: "/sessions/a.jsonl"},
		{name: "cancel", body: `{"path":null}`},
		{name: "missing path", body: `{}`, fail: true},
		{name: "extra field", body: `{"path":"/sessions/a.jsonl","other":true}`, fail: true},
		{name: "malformed", body: `{`, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodePickerResult(strings.NewReader(test.body))
			if (err != nil) != test.fail || got != test.want {
				t.Fatalf("decodePickerResult() = %q, %v; want %q, failure %v", got, err, test.want, test.fail)
			}
		})
	}
}

func TestSupportedNodeVersion(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "v20.0.0"},
		{version: "v22.18.0"},
		{version: "v22.19.0", want: true},
		{version: "v24.4.1", want: true},
		{version: "v19.9.9"},
		{version: "v20oops.0.0"},
		{version: "v20.0"},
	} {
		if got := supportedNodeVersion(test.version); got != test.want {
			t.Errorf("supportedNodeVersion(%q) = %v, want %v", test.version, got, test.want)
		}
	}
}

func TestResolveSessionDirectoryUsesNativePrecedenceWithoutCreatingDirectories(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	for _, dir := range []string{cwd, agentDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	settingsDir := filepath.Join(cwd, ".pi")
	if err := os.Mkdir(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"sessionDir":"saved/sessions"}`), 0600); err != nil {
		t.Fatal(err)
	}
	configured, err := configuredSessionDir(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, cli, environment, want string
	}{
		{name: "cli", cli: "cli-dir", environment: "env-dir", want: filepath.Join(cwd, "cli-dir")},
		{name: "environment", environment: "env-dir", want: filepath.Join(cwd, "env-dir")},
		{name: "settings", want: filepath.Join(cwd, "saved", "sessions")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resolveSessionDirectory(cwd, test.cli, test.environment, agentDir, configured)
			if got != test.want {
				t.Fatalf("resolveSessionDirectory() = %q, want %q", got, test.want)
			}
			if _, err := os.Stat(got); !os.IsNotExist(err) {
				t.Fatalf("lookup created or unexpectedly found %q: %v", got, err)
			}
		})
	}
}

func TestSelectSavedSessionRejectsManagedDriftBeforeStartingRuntime(t *testing.T) {
	managed := managedFixture(t)
	if err := os.WriteFile(managed.Executable, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output, errOutput strings.Builder
	if _, err := SelectSavedSession(context.Background(), managed, t.TempDir(), "", input, &output, &errOutput); err == nil {
		t.Fatal("managed runtime drift accepted")
	}
	if output.Len() != 0 {
		t.Fatalf("runtime started before drift refusal: %q", output.String())
	}
}

func TestSelectSavedSessionDisposableTerminal(t *testing.T) {
	launcher := os.Getenv("PI_PICKER_MANAGED_EXECUTABLE")
	if launcher == "" {
		t.Skip("set PI_PICKER_MANAGED_EXECUTABLE for disposable terminal verification")
	}
	managed, err := ObserveManaged(launcher)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.Mkdir(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "fixture.jsonl")
	original := []byte(`{"type":"session","version":3,"id":"fixture-id","timestamp":"2026-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" + `{"type":"message","id":"entry-id","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"picker-fixture","timestamp":1767225601000}}` + "\n")
	if err := os.WriteFile(sessionPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(t.TempDir(), "agent")
	if err := os.Mkdir(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	keybindings := []byte(`{"app.session.delete":"ctrl+x"}`)
	if err := os.WriteFile(filepath.Join(agentDir, "keybindings.json"), keybindings, 0600); err != nil {
		t.Fatal(err)
	}
	terminalBefore, err := term.GetState(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectSavedSession(context.Background(), managed, cwd, sessionDir, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if selected != sessionPath {
		t.Fatalf("selection returned %q, want %q", selected, sessionPath)
	}
	terminalAfterSelection, err := term.GetState(os.Stdin.Fd())
	if err != nil || !reflect.DeepEqual(terminalBefore, terminalAfterSelection) {
		t.Fatalf("selection did not restore terminal: %v", err)
	}
	cancelled, err := SelectSavedSession(context.Background(), managed, cwd, sessionDir, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != "" {
		t.Fatalf("cancel returned selected path %q", cancelled)
	}
	terminalAfterCancel, err := term.GetState(os.Stdin.Fd())
	if err != nil || !reflect.DeepEqual(terminalBefore, terminalAfterCancel) {
		t.Fatalf("cancellation did not restore terminal: %v", err)
	}
	got, err := os.ReadFile(sessionPath)
	if err != nil || string(got) != string(original) {
		t.Fatalf("selection-only actions changed the session: %v", err)
	}
	entries, err := os.ReadDir(os.Getenv("PI_CODING_AGENT_DIR"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "keybindings.json" {
		t.Fatalf("picker created agent state: %v, %v", entries, err)
	}
	bindingsAfter, err := os.ReadFile(filepath.Join(agentDir, "keybindings.json"))
	if err != nil || string(bindingsAfter) != string(keybindings) {
		t.Fatalf("picker changed keybindings: %v", err)
	}
}

func TestDecodePickerResultRejectsOversizedInput(t *testing.T) {
	body, err := json.Marshal(map[string]string{"path": strings.Repeat("a", maxPickerResultBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePickerResult(strings.NewReader(string(body))); err == nil {
		t.Fatal("oversized picker result accepted")
	}
}
