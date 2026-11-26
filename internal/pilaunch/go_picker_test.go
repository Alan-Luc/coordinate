package pilaunch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func writeSession(t *testing.T, dir, file, cwd, prompt string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":\"2026-01-01T00:00:00Z\",\"cwd\":%q}\n", file, cwd) +
		fmt.Sprintf("{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", prompt)
	path := filepath.Join(dir, file+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func pickerFixture(t *testing.T) (root, current, older, other string) {
	root = t.TempDir()
	currentDir := filepath.Join(root, "--proj--")
	current = writeSession(t, currentDir, "new", "/proj", "fix the parser", time.Minute)
	older = writeSession(t, currentDir, "old", "/proj", "write docs", time.Hour)
	other = writeSession(t, filepath.Join(root, "--elsewhere--"), "far", "/elsewhere", "deploy service", 2*time.Hour)
	if err := os.WriteFile(filepath.Join(currentDir, "bad.jsonl"), []byte("not json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return
}

func press(m goPickerModel, msgs ...tea.Msg) goPickerModel {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(goPickerModel)
	}
	return m
}

func runes(s string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestListSavedSessionsNewestFirstSkippingMalformed(t *testing.T) {
	root, current, older, _ := pickerFixture(t)
	got := listSavedSessions(filepath.Join(root, "--proj--"))
	if len(got) != 2 || got[0].Path != current || got[1].Path != older {
		t.Fatalf("unexpected listing: %+v", got)
	}
	if got[0].Preview != "fix the parser" || got[0].Cwd != "/proj" {
		t.Fatalf("header or preview not read: %+v", got[0])
	}
}

func TestListSavedSessionsCapsFileCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxGoPickerFiles+5; i++ {
		writeSession(t, dir, fmt.Sprintf("s%d", i), "/p", "x", time.Duration(i)*time.Second)
	}
	if got := listSavedSessions(dir); len(got) != maxGoPickerFiles {
		t.Fatalf("listed %d sessions", len(got))
	}
}

func TestListSavedSessionsReadsBoundedPreview(t *testing.T) {
	dir := t.TempDir()
	path := writeSession(t, dir, "big", "/p", strings.Repeat("a", 2*maxGoPickerFileBytes), time.Minute)
	got := listSavedSessions(dir)
	if len(got) != 1 || got[0].Path != path || got[0].Preview != "" || got[0].Cwd != "/p" {
		t.Fatalf("oversized preview line not skipped: %+v", got)
	}
}

func TestGoPickerScopeFilterAndSelection(t *testing.T) {
	root, current, older, other := pickerFixture(t)
	m := newGoPickerModel(filepath.Join(root, "--proj--"), root)
	if len(m.visible()) != 2 {
		t.Fatalf("current scope shows %d", len(m.visible()))
	}
	m = press(m, runes("docs"))
	if v := m.visible(); len(v) != 1 || v[0].Path != older {
		t.Fatalf("filter failed: %+v", v)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.done || m.selected != "" {
		t.Fatal("escape did not cancel")
	}
	m = press(newGoPickerModel(filepath.Join(root, "--proj--"), root), tea.KeyMsg{Type: tea.KeyTab})
	if v := m.visible(); len(v) != 3 || v[0].Path != current || v[2].Path != other {
		t.Fatalf("all scope wrong: %+v", v)
	}
	m = press(m, runes("elsewhere"))
	if v := m.visible(); len(v) != 1 || v[0].Path != other {
		t.Fatalf("cwd filter failed: %+v", v)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.done || m.selected != other {
		t.Fatalf("enter selected %q", m.selected)
	}
	m = press(newGoPickerModel(filepath.Join(root, "--proj--"), root), tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.done || m.selected != "" {
		t.Fatal("ctrl+c did not cancel")
	}
}

func TestSelectSavedSessionUsesGoPickerWithoutNodeLayout(t *testing.T) {
	managed, err := ObserveManaged(compiledFixture(t, "1.5.2 (Pi-Bolt 1.2.1, darwin-arm64-jit, JIT on)"))
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	sessionDir := t.TempDir()
	want := writeSession(t, sessionDir, "s", cwd, "hello", time.Minute)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := writer.WriteString("\r"); err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var output, errOutput strings.Builder
	got, err := SelectSavedSession(context.Background(), managed, cwd, sessionDir, reader, &output, &errOutput)
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestGoPickerSpaceAddsOneSpace(t *testing.T) {
	root, _, _, _ := pickerFixture(t)
	m := press(newGoPickerModel(filepath.Join(root, "--proj--"), root), runes("fix"), tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}, runes("the"))
	if m.query != "fix the" || len(m.visible()) != 1 {
		t.Fatalf("query %q matched %d sessions", m.query, len(m.visible()))
	}
}

func TestGoPickerViewStripsTerminalControlSequences(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "--proj--")
	prompt, err := json.Marshal("hello \x1b]52;c;cGF5bG9hZA==\x07 \x1b[2Jworld \u009b31m")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"session\",\"version\":3,\"id\":\"evil\",\"cwd\":\"/proj\"}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":" + string(prompt) + "}}\n"
	if err := os.WriteFile(filepath.Join(dir, "evil.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	view := newGoPickerModel(dir, root).View()
	for _, bad := range []string{"\x1b", "\x07", "\u009b"} {
		if strings.Contains(view, bad) {
			t.Fatalf("view contains control character %q: %q", bad, view)
		}
	}
	if !strings.Contains(view, "hello") || !strings.Contains(view, "world") {
		t.Fatalf("view lost printable text: %q", view)
	}
}
