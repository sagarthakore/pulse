package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// folderPicker is the overlay for choosing the local music folder.
type folderPicker struct {
	dir   string // "" lists drives (Windows)
	view  list
	err   error
	input textinput.Model
	// typing: the path input has focus.
	typing bool
}

const (
	pickUse    = "use"
	pickParent = "parent"
	pickDir    = "dir"
)

// openPicker shows the folder picker, starting at the current music folder
// or the user's Music folder.
func (m *Model) openPicker() tea.Cmd {
	start := ""
	if l := m.sources[SourceLocal].local; l != nil {
		start = l.Root()
	} else if home, err := os.UserHomeDir(); err == nil {
		start = home
		if fi, err := os.Stat(filepath.Join(home, "Music")); err == nil && fi.IsDir() {
			start = filepath.Join(home, "Music")
		}
	}
	ti := textinput.New()
	ti.Prompt = "path: "
	ti.Placeholder = `e.g. D:\Music or ~/Music`
	ti.PromptStyle = fg(cPeach).Bold(true)
	ti.TextStyle = sText
	ti.PlaceholderStyle = sFaint
	ti.Cursor.Style = fg(cRosewater)
	m.picker = &folderPicker{input: ti}
	m.picker.open(start)
	m.searchActive = false
	return nil
}

// open lists dir's subfolders.
func (p *folderPicker) open(dir string) {
	p.err = nil
	var items []item
	if dir == "" {
		for _, d := range drives() {
			items = append(items, item{kind: kindFolder, id: d, title: d, tag: pickDir})
		}
		p.dir = ""
		p.view = list{}
		p.view.setItems(items)
		return
	}

	dir = filepath.Clean(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		p.err = err
		return
	}
	p.dir = dir
	items = append(items, item{kind: kindFolder, id: dir, title: "✓ use this folder", tag: pickUse})
	if parent, ok := parentDir(dir); ok {
		items = append(items, item{kind: kindFolder, id: parent, title: "↑ ..", tag: pickParent})
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "$") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	for _, name := range dirs {
		items = append(items, item{kind: kindFolder, id: filepath.Join(dir, name), title: "▸ " + name, tag: pickDir})
	}
	p.view = list{}
	p.view.setItems(items)
}

// parentDir returns dir's parent; at a Windows drive root the parent is the
// drive list ("").
func parentDir(dir string) (string, bool) {
	parent := filepath.Dir(dir)
	if parent != dir {
		return parent, true
	}
	if runtime.GOOS == "windows" {
		return "", true
	}
	return "", false
}

// drives lists the Windows drive roots that exist.
func drives() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func (m *Model) onPickerKey(msg tea.KeyMsg) tea.Cmd {
	p := m.picker
	key := msg.String()

	if p.typing {
		switch key {
		case "esc":
			p.typing = false
			p.input.Blur()
			return nil
		case "enter":
			path := strings.Trim(strings.TrimSpace(p.input.Value()), `"'`)
			p.typing = false
			p.input.Blur()
			if path != "" {
				p.open(expandHome(path))
			}
			return nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return cmd
	}

	switch key {
	case "esc", "q":
		m.picker = nil
		if m.active == SourceLocal && m.src().api == nil && m.sources[SourceNavidrome].api != nil {
			m.active = SourceNavidrome // nothing to show locally yet
		}
		return nil
	case "up", "k":
		p.view.move(-1)
	case "down", "j":
		p.view.move(1)
	case "pgup", "ctrl+u":
		p.view.move(-10)
	case "pgdown", "ctrl+d":
		p.view.move(10)
	case "home", "g":
		p.view.top()
	case "end", "G":
		p.view.bottom()
	case "backspace", "left", "h":
		if parent, ok := parentDir(p.dir); ok && p.dir != "" {
			p.open(parent)
		}
	case "~":
		if home, err := os.UserHomeDir(); err == nil {
			p.open(home)
		}
	case "/":
		p.typing = true
		p.input.SetValue("")
		return p.input.Focus()
	case "enter", "right", "l":
		sel := p.view.selected()
		if sel == nil {
			return nil
		}
		if sel.tag == pickUse {
			return m.chooseFolder(sel.id)
		}
		p.open(sel.id)
	}
	return nil
}

// chooseFolder makes dir the local library and starts scanning it.
func (m *Model) chooseFolder(dir string) tea.Cmd {
	if m.opts.ChooseFolder == nil {
		m.flashInfo("choosing a folder isn't available")
		return nil
	}
	lib, err := m.opts.ChooseFolder(dir)
	if err != nil {
		m.flashError(err)
		return nil
	}
	s := m.sources[SourceLocal]
	if m.playing == SourceLocal && m.status.ID != "" {
		m.engine.Stop() // tracks from the old folder
		m.status = m.engine.Status()
	}
	s.queue.clear()
	m.setLocal(lib)
	m.picker = nil
	m.active = SourceLocal
	m.focus = focusBrowser
	m.flashInfo("scanning " + shortPath(lib.Root()) + "…")
	return tea.Batch(scanCmd(lib), m.ensureLoaded())
}
