package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReadResumeConfirmation(t *testing.T) {
	for _, scenario := range []struct {
		input string
		want  bool
	}{
		{"RESUME\n", true},
		{"RESUME\r\n", true},
		{"", false},
		{"RESUME", false},
		{"resume\n", false},
		{"yes\n", false},
		{"RESUME extra\n", false},
		{strings.Repeat("R", 300) + "\n", false},
	} {
		if got := readResumeConfirmation(strings.NewReader(scenario.input)); got != scenario.want {
			t.Errorf("confirmation %q = %v, want %v", scenario.input, got, scenario.want)
		}
	}
}

func TestResumePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, scenario := range []struct{ input, want string }{
		{"~", home},
		{"~/sessions/saved.jsonl", filepath.Join(home, "sessions", "saved.jsonl")},
		{filepath.Join(home, "saved.jsonl"), filepath.Join(home, "saved.jsonl")},
	} {
		got, err := resumePath(scenario.input)
		if err != nil || got != scenario.want {
			t.Errorf("resumePath(%q) = %q, %v; want %q", scenario.input, got, err, scenario.want)
		}
	}
}
