package pilaunch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	maxGoPickerFiles     = 500
	maxGoPickerFileBytes = 64 << 10
	goPickerRows         = 10
)

type savedSessionEntry struct {
	Path    string
	Name    string
	Preview string
	Cwd     string
	Updated time.Time
}

func listSavedSessions(dir string) []savedSessionEntry {
	return readSessionFiles(sessionFilesNewestFirst([]string{dir}))
}

func listAllSavedSessions(lookupDir, root string) []savedSessionEntry {
	dirs := []string{lookupDir}
	children, _ := os.ReadDir(root)
	for _, child := range children {
		if child.IsDir() {
			dirs = append(dirs, filepath.Join(root, child.Name()))
		}
	}
	return readSessionFiles(sessionFilesNewestFirst(dirs))
}

type sessionFile struct {
	path    string
	updated time.Time
}

func sessionFilesNewestFirst(dirs []string) []sessionFile {
	seen := map[string]bool{}
	var files []sessionFile
	for _, dir := range dirs {
		children, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, child := range children {
			path := filepath.Join(dir, child.Name())
			info, err := child.Info()
			if err != nil || !info.Mode().IsRegular() || !strings.HasSuffix(child.Name(), ".jsonl") || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, sessionFile{path, info.ModTime()})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].updated.After(files[j].updated) })
	if len(files) > maxGoPickerFiles {
		files = files[:maxGoPickerFiles]
	}
	return files
}

func readSessionFiles(files []sessionFile) []savedSessionEntry {
	var entries []savedSessionEntry
	for _, file := range files {
		if entry, ok := readSessionEntry(file); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func readSessionEntry(file sessionFile) (savedSessionEntry, bool) {
	handle, err := os.Open(file.path)
	if err != nil {
		return savedSessionEntry{}, false
	}
	defer handle.Close()
	body, err := io.ReadAll(io.LimitReader(handle, maxGoPickerFileBytes))
	if err != nil {
		return savedSessionEntry{}, false
	}
	entry := savedSessionEntry{Path: file.path, Updated: file.updated}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(nil, maxGoPickerFileBytes)
	for line := 0; scanner.Scan(); line++ {
		var record struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			Name    string `json:"name"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			if line == 0 {
				return savedSessionEntry{}, false
			}
			continue
		}
		switch {
		case line == 0:
			if record.Type != "session" {
				return savedSessionEntry{}, false
			}
			entry.Cwd = record.Cwd
		case record.Type == "session_info" && record.Name != "":
			entry.Name = record.Name
		case record.Type == "message" && record.Message.Role == "user" && entry.Preview == "":
			entry.Preview = messageText(record.Message.Content)
		}
	}
	return entry, true
}

func messageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	for _, part := range parts {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			return strings.TrimSpace(part.Text)
		}
	}
	return ""
}

type goPickerModel struct {
	lookupDir string
	root      string
	current   []savedSessionEntry
	all       []savedSessionEntry
	allLoaded bool
	showAll   bool
	query     string
	cursor    int
	done      bool
	selected  string
}

func newGoPickerModel(lookupDir, root string) goPickerModel {
	return goPickerModel{lookupDir: lookupDir, root: root, current: listSavedSessions(lookupDir)}
}

func (m goPickerModel) visible() []savedSessionEntry {
	source := m.current
	if m.showAll {
		source = m.all
	}
	query := strings.ToLower(m.query)
	var matches []savedSessionEntry
	for _, entry := range source {
		if strings.Contains(strings.ToLower(entry.Name+"\n"+entry.Preview+"\n"+entry.Cwd), query) {
			matches = append(matches, entry)
		}
	}
	return matches
}

func (m goPickerModel) Init() tea.Cmd { return nil }

func (m goPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	visible := m.visible()
	switch key.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.done = true
		return m, tea.Quit
	case tea.KeyEnter:
		if len(visible) == 0 {
			return m, nil
		}
		m.done = true
		m.selected = visible[m.cursor].Path
		return m, tea.Quit
	case tea.KeyTab:
		m.showAll = !m.showAll
		if m.showAll && !m.allLoaded {
			m.all = listAllSavedSessions(m.lookupDir, m.root)
			m.allLoaded = true
		}
		m.cursor = 0
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyDown:
		if m.cursor < len(visible)-1 {
			m.cursor++
		}
	case tea.KeyBackspace:
		if runes := []rune(m.query); len(runes) > 0 {
			m.query = string(runes[:len(runes)-1])
			m.cursor = 0
		}
	case tea.KeyRunes, tea.KeySpace:
		m.query += string(key.Runes)
		if key.Type == tea.KeySpace {
			m.query += " "
		}
		m.cursor = 0
	}
	return m, nil
}

func (m goPickerModel) View() string {
	scope := "current project"
	if m.showAll {
		scope = "all projects"
	}
	var view strings.Builder
	fmt.Fprintf(&view, "Resume session (%s) — Tab scope, Enter select, Esc cancel\nSearch: %s\n", scope, m.query)
	visible := m.visible()
	start := max(0, min(m.cursor-goPickerRows/2, len(visible)-goPickerRows))
	for i := start; i < len(visible) && i < start+goPickerRows; i++ {
		entry := visible[i]
		label := entry.Name
		if label == "" {
			label = entry.Preview
		}
		if label == "" {
			label = filepath.Base(entry.Path)
		}
		label = strings.Join(strings.Fields(label), " ")
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		line := fmt.Sprintf("%s%s  %s", marker, entry.Updated.Format("2006-01-02 15:04"), label)
		if m.showAll {
			line += "  [" + entry.Cwd + "]"
		}
		view.WriteString(line + "\n")
	}
	if len(visible) == 0 {
		view.WriteString("  no matching sessions\n")
	}
	return view.String()
}

func runGoPicker(ctx context.Context, in *os.File, out io.Writer, lookupDir, allSessionsDir, agentDir string) (string, error) {
	root := allSessionsDir
	if root == "" {
		root = filepath.Join(agentDir, "sessions")
	}
	final, err := tea.NewProgram(newGoPickerModel(lookupDir, root), tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx)).Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("session picker failed: %w", err)
	}
	model, ok := final.(goPickerModel)
	if !ok {
		return "", errors.New("session picker returned an unexpected state")
	}
	return model.selected, nil
}
