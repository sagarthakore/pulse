package ui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sagarthakore/pulse/internal/local"
	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
)

// SourceKind identifies a music library.
type SourceKind int

const (
	SourceNavidrome SourceKind = iota
	SourceLocal
	numSources
)

func (k SourceKind) String() string {
	if k == SourceLocal {
		return "local"
	}
	return "navidrome"
}

// sourceAccents color each library's name in the header switch.
var sourceAccents = [numSources]lipgloss.Color{cSapphire, cTeal}

// Library is what the UI needs from a music source: the Subsonic-style
// browse API plus a way to open tracks for playback. The Navidrome adapter
// and local.Library both satisfy it.
type Library interface {
	Artists(ctx context.Context) ([]subsonic.Artist, error)
	Artist(ctx context.Context, id string) (*subsonic.Artist, error)
	Album(ctx context.Context, id string) (*subsonic.Album, error)
	AlbumList(ctx context.Context, t subsonic.AlbumListType, size, offset int) ([]subsonic.Album, error)
	Playlists(ctx context.Context) ([]subsonic.Playlist, error)
	Playlist(ctx context.Context, id string) (*subsonic.Playlist, error)
	Search(ctx context.Context, query string) (*subsonic.SearchResult, error)
	RandomSongs(ctx context.Context, n int) ([]subsonic.Song, error)
	Star(ctx context.Context, id string) error
	Unstar(ctx context.Context, id string) error
	Scrobble(ctx context.Context, id string, submission bool) error
	player.Opener
}

// source is the UI state for one library: its navigation, tabs and its own
// play queue.
type source struct {
	kind  SourceKind
	api   Library        // nil when unavailable
	local *local.Library // the local library (local source only)
	label string         // shown in the header
	// unavailable explains why api is nil (e.g. server unreachable).
	unavailable string

	tab    tabID
	stacks [numTabs][]*page
	queue  queue
	gen    int // local library generation the pages were loaded from
}

func (m *Model) newSource(kind SourceKind) *source {
	s := &source{kind: kind, tab: tabAlbums, queue: newQueue()}
	m.resetPages(s)
	return s
}

// resetPages creates fresh root pages for every tab.
func (m *Model) resetPages(s *source) {
	s.stacks[tabArtists] = []*page{m.newPage(pageArtists, "", "Artists")}
	albums := m.newPage(pageAlbums, "", "Albums")
	albums.albumType = subsonic.AlbumsNewest
	s.stacks[tabAlbums] = []*page{albums}
	s.stacks[tabPlaylists] = []*page{m.newPage(pagePlaylists, "", "Playlists")}
	s.stacks[tabSearch] = []*page{m.newPage(pageSearch, "", "Search")}
}

// setLocal installs a (new) local library.
func (m *Model) setLocal(lib *local.Library) {
	s := m.sources[SourceLocal]
	s.api, s.local, s.unavailable = lib, lib, ""
	s.label = shortPath(lib.Root())
	s.gen = lib.Status().Generation
	m.resetPages(s)
}

// src is the library being viewed.
func (m *Model) src() *source { return m.sources[m.active] }

// playSrc is the library whose queue drives playback.
func (m *Model) playSrc() *source { return m.sources[m.playing] }

// controlSrc is what transport keys act on: the playing library while
// something plays, otherwise the one being viewed.
func (m *Model) controlSrc() *source {
	if m.status.ID != "" {
		return m.playSrc()
	}
	return m.src()
}

// playingHere reports whether the viewed library is the one playing.
func (m *Model) playingHere() bool {
	return m.active == m.playing && m.status.ID != ""
}

// switchSource flips between Navidrome and the local folder.
func (m *Model) switchSource() tea.Cmd {
	next := SourceLocal
	if m.active == SourceLocal {
		next = SourceNavidrome
	}
	s := m.sources[next]
	if next == SourceNavidrome && s.api == nil {
		msg := "no Navidrome server configured — run pulse --setup"
		if s.unavailable != "" {
			msg = "Navidrome unavailable: " + s.unavailable
		}
		m.flashInfo(msg)
		return nil
	}
	m.active = next
	m.searchActive = false
	m.search.Blur()
	if next == SourceLocal && s.api == nil {
		return m.openPicker()
	}
	return m.ensureLoaded()
}

// ensureLoaded loads the current page if it hasn't been yet.
func (m *Model) ensureLoaded() tea.Cmd {
	if p := m.currentPage(); p != nil && !p.loaded && !p.loading {
		return m.loadPage(m.src(), p, 0)
	}
	return nil
}

// --- local scanning ---

type scanDoneMsg struct {
	lib *local.Library
	err error
}

func scanCmd(lib *local.Library) tea.Cmd {
	return func() tea.Msg {
		return scanDoneMsg{lib: lib, err: lib.Scan(context.Background())}
	}
}

// refreshLocal reloads the local root pages after the library changes.
func (m *Model) refreshLocal() tea.Cmd {
	s := m.sources[SourceLocal]
	if s.local == nil {
		return nil
	}
	gen := s.local.Status().Generation
	if gen == s.gen {
		return nil
	}
	s.gen = gen
	var cmds []tea.Cmd
	for t := range s.stacks {
		if root := s.stacks[t][0]; root.loaded || root.loading {
			cmds = append(cmds, m.loadPage(s, root, 0))
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) onScanDone(msg scanDoneMsg) tea.Cmd {
	s := m.sources[SourceLocal]
	if s.local != msg.lib {
		return nil // the folder was changed during the scan
	}
	if msg.err != nil {
		m.flashError(msg.err)
	} else if st := msg.lib.Status(); st.Skipped > 0 {
		m.flashInfo(plural(st.Skipped, "file needs", "files need") + " ffmpeg to play — install it to include them")
	}
	return m.refreshLocal()
}

// shortPath abbreviates the home directory as ~.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return "~" + string(filepath.Separator) + rel
		}
	}
	return p
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return commas(n) + " " + many
}

// commas formats n with thousands separators.
func commas(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commas(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
