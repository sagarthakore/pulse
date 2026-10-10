package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type helpEntry struct{ keys, desc string }

type helpSection struct {
	title   string
	entries []helpEntry
}

var helpSections = []helpSection{
	{"Playback", []helpEntry{
		{"space", "play / pause"},
		{"n / p", "next / previous track"},
		{"← / →", "seek -/+ 10s"},
		{"shift+← / →", "seek -/+ 60s"},
		{"+ / -", "volume up / down"},
		{"r", "cycle repeat (off / all / one)"},
		{"s", "shuffle upcoming tracks"},
		{"R", "play a random mix"},
		{"v", "toggle visualizer"},
	}},
	{"Browse", []helpEntry{
		{"1-4", "artists / albums / playlists / search"},
		{"/", "search"},
		{"enter", "open, or play from this song"},
		{"esc / bksp", "go back"},
		{"a", "add to end of queue"},
		{"A", "play next"},
		{"P", "play now (replace queue)"},
		{"f", "star / unstar"},
		{"o", "change album order (albums tab)"},
		{"ctrl+r", "reload"},
	}},
	{"Libraries", []helpEntry{
		{"L", "switch navidrome / local"},
		{"O", "choose local music folder"},
		{"u", "rescan local folder"},
	}},
	{"Queue", []helpEntry{
		{"tab", "switch between browser and queue"},
		{"enter", "play selected"},
		{"d / del", "remove from queue"},
		{"K / J", "move up / down"},
		{"X", "clear queue"},
	}},
	{"General", []helpEntry{
		{"j k ↑ ↓", "move"},
		{"g / G", "top / bottom"},
		{"pgup / pgdn", "page up / down"},
		{"?", "toggle this help"},
		{"q / ctrl+c", "quit"},
	}},
}

func (m *Model) onKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if key == "ctrl+c" {
		return tea.Quit
	}

	if m.picker != nil {
		return m.onPickerKey(msg)
	}

	if m.searchActive {
		switch key {
		case "esc":
			m.searchActive = false
			m.search.Blur()
			return nil
		case "enter":
			q := strings.TrimSpace(m.search.Value())
			m.searchActive = false
			m.search.Blur()
			if q == "" {
				return nil
			}
			s := m.src()
			p := m.newPage(pageSearch, q, "Search: "+q)
			s.stacks[tabSearch] = []*page{p}
			m.focus = focusBrowser
			return m.loadPage(s, p, 0)
		}
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return cmd
	}

	if m.showHelp {
		switch key {
		case "?", "esc", "q":
			m.showHelp = false
		}
		return nil
	}

	// Global keys.
	switch key {
	case "q":
		return tea.Quit
	case "?":
		m.showHelp = true
		return nil
	case " ":
		return m.togglePause()
	case "n":
		return m.next()
	case "p":
		return m.prev()
	case "left":
		m.seekBy(-seekStep)
		return nil
	case "right":
		m.seekBy(seekStep)
		return nil
	case "shift+left":
		m.seekBy(-seekStepLarge)
		return nil
	case "shift+right":
		m.seekBy(seekStepLarge)
		return nil
	case "+", "=":
		m.engine.SetVolume(m.status.Volume + volumeStep)
		m.status = m.engine.Status()
		return nil
	case "-", "_":
		m.engine.SetVolume(m.status.Volume - volumeStep)
		m.status = m.engine.Status()
		return nil
	case "r":
		q := &m.src().queue
		q.cycleRepeat()
		m.flashInfo("repeat: " + q.repeat.String())
		m.ensurePreload()
		return nil
	case "s":
		q := &m.src().queue
		if len(q.songs) < 2 {
			return nil
		}
		q.shuffle()
		m.flashInfo("shuffled upcoming tracks")
		m.ensurePreload()
		return nil
	case "R":
		if m.src().api == nil {
			return nil
		}
		m.flashInfo("fetching a random mix…")
		return m.fetchRandom(m.src())
	case "L":
		return m.switchSource()
	case "O":
		if m.active != SourceLocal {
			m.flashInfo("press L to switch to your local library first")
			return nil
		}
		return m.openPicker()
	case "u":
		l := m.sources[SourceLocal].local
		if m.active != SourceLocal || l == nil {
			return nil
		}
		if l.Status().Scanning {
			m.flashInfo("already scanning…")
			return nil
		}
		m.flashInfo("rescanning " + shortPath(l.Root()) + "…")
		return scanCmd(l)
	case "v":
		m.showViz = !m.showViz
		return nil
	case "tab", "shift+tab":
		if m.focus == focusBrowser {
			m.focus = focusQueue
		} else {
			m.focus = focusBrowser
		}
		return nil
	case "1", "2", "3", "4":
		return m.switchTab(tabID(key[0] - '1'))
	case "/":
		m.src().tab = tabSearch
		m.focus = focusBrowser
		m.searchActive = true
		m.search.SetValue("")
		return m.search.Focus()
	case "up", "k":
		return m.moveCursor(-1)
	case "down", "j":
		return m.moveCursor(1)
	case "pgup", "ctrl+u":
		return m.moveCursor(-m.listHeight())
	case "pgdown", "ctrl+d":
		return m.moveCursor(m.listHeight())
	case "home", "g":
		m.activeList().top()
		return nil
	case "end", "G":
		m.activeList().bottom()
		return m.afterMove()
	}

	if m.focus == focusQueue {
		return m.onQueueKey(key)
	}
	return m.onBrowserKey(key)
}

func (m *Model) switchTab(t tabID) tea.Cmd {
	m.src().tab = t
	m.focus = focusBrowser
	if t == tabSearch && m.currentPage().id == "" && m.src().api != nil {
		m.searchActive = true
		return m.search.Focus()
	}
	return m.ensureLoaded()
}

func (m *Model) activeList() *list {
	if m.focus == focusQueue {
		return &m.src().queue.view
	}
	if p := m.currentPage(); p != nil {
		return &p.list
	}
	return &list{}
}

func (m *Model) moveCursor(delta int) tea.Cmd {
	m.activeList().move(delta)
	return m.afterMove()
}

func (m *Model) afterMove() tea.Cmd {
	if m.focus == focusBrowser {
		if p := m.currentPage(); p != nil {
			return m.maybeLoadMore(p)
		}
	}
	return nil
}

func (m *Model) onBrowserKey(key string) tea.Cmd {
	s := m.src()
	p := m.currentPage()
	if p == nil {
		return nil
	}
	sel := p.list.selected()

	switch key {
	case "esc", "backspace":
		if stack := s.stacks[s.tab]; len(stack) > 1 {
			s.stacks[s.tab] = stack[:len(stack)-1]
		}
		return nil
	case "ctrl+r", "f5":
		p.list.items = nil
		p.exhausted = false
		return m.loadPage(s, p, 0)
	case "o":
		if p.kind != pageAlbums {
			return nil
		}
		for i, t := range albumTypes {
			if t == p.albumType {
				p.albumType = albumTypes[(i+1)%len(albumTypes)]
				break
			}
		}
		p.list = list{}
		p.exhausted = false
		return m.loadPage(s, p, 0)
	}

	if sel == nil {
		return nil
	}

	switch key {
	case "enter":
		switch sel.kind {
		case kindArtist:
			return m.pushPage(m.newPage(pageArtist, sel.id, sel.title))
		case kindAlbum:
			return m.pushPage(m.newPage(pageAlbum, sel.id, sel.title))
		case kindPlaylist:
			return m.pushPage(m.newPage(pagePlaylist, sel.id, sel.title))
		case kindSong:
			return m.playFromPage(p)
		}
	case "a":
		return m.fetchSongs(s, *sel, actAppend)
	case "A":
		return m.fetchSongs(s, *sel, actPlayNext)
	case "P":
		if sel.kind == kindSong {
			return m.playFromPage(p)
		}
		return m.fetchSongs(s, *sel, actReplace)
	case "f":
		return m.toggleStar(s, sel)
	}
	return nil
}

// playFromPage replaces the queue with every song on the page and starts at
// the selected one.
func (m *Model) playFromPage(p *page) tea.Cmd {
	msg := songsMsg{src: m.active, action: actReplace}
	for i, it := range p.list.items {
		if it.kind != kindSong || it.song == nil {
			continue
		}
		if i == p.list.cursor {
			msg.start = len(msg.songs)
		}
		msg.songs = append(msg.songs, *it.song)
	}
	return m.onSongs(msg)
}

func (m *Model) onQueueKey(key string) tea.Cmd {
	s := m.src()
	q := &s.queue
	i := q.view.cursor
	if i < 0 || i >= len(q.songs) {
		return nil
	}
	switch key {
	case "enter":
		return m.playIndex(s, i)
	case "d", "delete", "x":
		wasPlaying := i == q.cur && m.playingHere()
		q.remove(i)
		if wasPlaying {
			if i < len(q.songs) {
				return m.playIndex(s, i)
			}
			m.engine.Stop()
			m.status = m.engine.Status()
			return nil
		}
		m.ensurePreload()
	case "K", "shift+up":
		if q.moveItem(i, -1) {
			m.ensurePreload()
		}
	case "J", "shift+down":
		if q.moveItem(i, 1) {
			m.ensurePreload()
		}
	case "X":
		if m.playingHere() {
			m.engine.Stop()
			m.status = m.engine.Status()
		}
		q.clear()
		m.flashInfo("queue cleared")
	case "f":
		it := q.view.items[i]
		return m.toggleStar(s, &it)
	}
	return nil
}
