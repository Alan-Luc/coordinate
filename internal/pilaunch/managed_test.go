package pilaunch

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func managedFixture(t *testing.T) Managed {
	t.Helper()
	root := t.TempDir()
	agent := filepath.Join(root, "agent")
	launcher := filepath.Join(agent, "bin", "pi")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	launcherBytes, err := os.ReadFile("testdata/pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, launcherBytes, 0700); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(agent, "install")
	release := filepath.Join(install, "releases", "1.4.0")
	packageDir := filepath.Join(release, "node_modules", "@earendil-works", "pi-coding-agent")
	entry := filepath.Join(packageDir, "dist", "bundle", "cli.js")
	binDir := filepath.Join(release, "node_modules", ".bin")
	for _, dir := range []string{packageDir, filepath.Dir(entry), binDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(install, "current-version"), []byte("1.4.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","version":"1.4.0","bin":{"pi":"dist/bundle/cli.js"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("#!/usr/bin/env node\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../@earendil-works/pi-coding-agent/dist/bundle/cli.js", filepath.Join(binDir, "pi")); err != nil {
		t.Fatal(err)
	}
	managed, err := ObserveManaged(launcher)
	if err != nil {
		t.Fatalf("observe managed layout: %v", err)
	}
	return managed
}

func compiledFixture(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	body := "#!/bin/sh\necho '" + version + "'\n"
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestObserveManagedExecutableAliasPreservesOriginalExecPath(t *testing.T) {
	executable := compiledFixture(t, "1.5.2")
	alias := filepath.Join(t.TempDir(), "pi")
	if err := os.Symlink(executable, alias); err != nil {
		t.Fatal(err)
	}
	aliased, err := ObserveManaged(alias)
	if err != nil {
		t.Fatalf("alias rejected: %v", err)
	}
	if aliased.Executable != alias {
		t.Fatalf("native executable was replaced: %s", aliased.Executable)
	}
	canonical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if aliased.Observation.Executable.Path != canonical {
		t.Fatalf("executable observation is not canonical: %s", aliased.Observation.Executable.Path)
	}
	if err := Recheck(aliased); err != nil {
		t.Fatalf("stable alias failed recheck: %v", err)
	}
}

func TestObserveManagedAcceptsAnyPiVersion(t *testing.T) {
	for _, version := range []string{"1.5.2", "1.5.2 (Pi-Bolt 1.2.1, darwin-arm64-jit, JIT on)", "1.3.0"} {
		managed, err := ObserveManaged(compiledFixture(t, version))
		if err != nil {
			t.Fatalf("%s rejected: %v", version, err)
		}
		if managed.Observation.Version != version {
			t.Fatalf("version %q recorded as %q", version, managed.Observation.Version)
		}
		if err := Recheck(managed); err != nil {
			t.Fatalf("stable observation failed recheck: %v", err)
		}
	}
}

func TestObserveManagedRecognizesNodeLayout(t *testing.T) {
	managed := managedFixture(t)
	if err := Recheck(managed); err != nil {
		t.Fatalf("stable observation failed recheck: %v", err)
	}
}

func TestObserveRejectsUnsafeExecutable(t *testing.T) {
	t.Run("group writable", func(t *testing.T) {
		path := compiledFixture(t, "1.5.2")
		if err := os.Chmod(path, 0775); err != nil {
			t.Fatal(err)
		}
		if _, err := ObserveManaged(path); err == nil {
			t.Fatal("group-writable executable accepted")
		}
	})
	t.Run("not executing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pi")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 3\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := ObserveManaged(path); err == nil {
			t.Fatal("executable failing --version accepted")
		}
	})
}

func TestRecheckRejectsExecutableDrift(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		managed, err := ObserveManaged(compiledFixture(t, "1.5.2"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(managed.Executable, []byte("#!/bin/sh\necho 1.5.2\n# changed\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := Recheck(managed); err == nil {
			t.Fatal("content drift accepted")
		}
	})
	t.Run("group writable", func(t *testing.T) {
		managed, err := ObserveManaged(compiledFixture(t, "1.5.2"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(managed.Executable, 0775); err != nil {
			t.Fatal(err)
		}
		if err := Recheck(managed); err == nil {
			t.Fatal("group-writable executable accepted")
		}
	})
	t.Run("swapped inode with same bytes", func(t *testing.T) {
		managed, err := ObserveManaged(compiledFixture(t, "1.5.2"))
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(managed.Executable)
		if err != nil {
			t.Fatal(err)
		}
		replacement := managed.Executable + ".new"
		if err := os.WriteFile(replacement, body, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, managed.Executable); err != nil {
			t.Fatal(err)
		}
		if err := Recheck(managed); err == nil {
			t.Fatal("swapped executable accepted")
		}
	})
}

func TestResolveRejectsCoordinatorIdentity(t *testing.T) {
	coordinator := filepath.Join(t.TempDir(), "agent-coordinator")
	if err := os.WriteFile(coordinator, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(coordinator, "", coordinator)
	if err == nil || got != "" {
		t.Fatalf("coordinator recursion accepted: %q, %v", got, err)
	}
}

func TestMetadataReadRejectsSymlinkAndFIFO(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFile(filepath.Join(root, "link"), 10); err == nil {
		t.Fatal("metadata symlink accepted")
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFile(fifo, 10); err == nil {
		t.Fatal("metadata FIFO accepted")
	}
}

func TestObservationIsImmutableAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata")
	body := []byte("safe")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := observeFile(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if got.Hash != hex.EncodeToString(sum[:]) || got.Size != 4 {
		t.Fatalf("unexpected observation: %+v", got)
	}
	if _, err := observeFile(path, 3); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	if strings.Contains(got.Hash, " ") {
		t.Fatal("invalid digest")
	}
}
