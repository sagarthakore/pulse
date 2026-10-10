package ui

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sagarthakore/pulse/internal/local"
	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
)

// fakeEngine records what the UI asks it to play.
type fakeEngine struct {
	played []player.Media
	status player.Status
	events chan player.Event
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{events: make(chan player.Event, 8), status: player.Status{Volume: 80}}
}

func (e *fakeEngine) Play(m player.Media) {
	e.played = append(e.played, m)
	e.status.ID, e.status.Paused = m.ID, false
}
func (e *fakeEngine) Preload(m player.Media)      { e.status.PreloadID = m.ID }
func (e *fakeEngine) ClearPreload()               { e.status.PreloadID = "" }
func (e *fakeEngine) Stop()                       { e.status.ID, e.status.PreloadID = "", "" }
func (e *fakeEngine) TogglePause() bool           { e.status.Paused = !e.status.Paused; return e.status.Paused }
func (e *fakeEngine) Seek(time.Duration)          {}
func (e *fakeEngine) SetVolume(v int) int         { e.status.Volume = v; return v }
func (e *fakeEngine) Status() player.Status       { return e.status }
func (e *fakeEngine) Spectrum(n int) []float64    { return make([]float64, n) }
func (e *fakeEngine) Events() <-chan player.Event { return e.events }
func (e *fakeEngine) last() player.Media          { return e.played[len(e.played)-1] }

// run executes cmd and feeds pulse's own resulting messages back into the
// model, the way Bubble Tea would. Commands that block (the engine-event
// wait) are abandoned, and timer-driven messages (ticks, cursor blinks) are
// dropped so the loop terminates.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(time.Second):
		return
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	case pageLoadedMsg, songsMsg, scanDoneMsg, randomSongsMsg, starMsg, scrobbleMsg:
		_, next := m.Update(msg)
		run(m, next)
	}
}

func key(m *Model, k string) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	_, cmd := m.Update(msg)
	run(m, cmd)
}

// writeWAV writes a short 16-bit stereo WAV file.
func writeWAV(t *testing.T, path string) {
	t.Helper()
	const n = 22050
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+n*4))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(2), uint32(44100), uint32(44100 * 4), uint16(4), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(n*4))
	b.Write(make([]byte, n*4))
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// localLib makes and scans a folder: Band/First Album/{01 Intro,02 Song}.wav
func localLib(t *testing.T) *local.Library {
	t.Helper()
	root, state := t.TempDir(), t.TempDir()
	writeWAV(t, filepath.Join(root, "Band", "First Album", "01 Intro.wav"))
	writeWAV(t, filepath.Join(root, "Band", "First Album", "02 Song.wav"))
	lib, err := local.Open(root, local.Options{CacheDir: state, DataDir: state})
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return lib
}

func songsOf(q *queue) string {
	var out []string
	for _, s := range q.songs {
		out = append(out, s.Title)
	}
	return strings.Join(out, ",")
}

func TestLocalLibraryBrowseAndPlay(t *testing.T) {
	eng := newFakeEngine()
	m := New(eng, Options{Local: localLib(t), Start: SourceLocal})
	run(m, m.ensureLoaded())

	p := m.currentPage()
	if len(p.list.items) != 1 || p.list.items[0].title != "First Album" {
		t.Fatalf("albums page = %+v", p.list.items)
	}
	key(m, "enter") // open the album
	if titles := m.currentPage().list.items; len(titles) != 2 || titles[0].title != "01 Intro" {
		t.Fatalf("album page = %+v", titles)
	}
	key(m, "enter") // play from the first track

	got := eng.last()
	if got.ID != "t:Band/First Album/01 Intro.wav" || m.playing != SourceLocal {
		t.Fatalf("played %+v (playing=%v)", got, m.playing)
	}
	src, err := got.Opener.Open(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4)
	io.ReadFull(src.Body, head)
	src.Body.Close()
	if string(head) != "RIFF" || src.Format != "wav" {
		t.Fatalf("local opener returned %q / %q", head, src.Format)
	}
}

func TestQueuesAreSeparatePerLibrary(t *testing.T) {
	eng := newFakeEngine()
	nav := &fakeLib{albums: []subsonic.Album{{ID: "al1", Name: "Server Album"}},
		songs: map[string][]subsonic.Song{"al1": {{ID: "n1", Title: "Server Song"}}}}
	m := New(eng, Options{Navidrome: nav, NavidromeLabel: "alice @ nas", Local: localLib(t)})
	run(m, m.ensureLoaded())

	key(m, "a") // queue the server album
	if m.active != SourceNavidrome || songsOf(&m.src().queue) != "Server Song" {
		t.Fatalf("navidrome queue = %q", songsOf(&m.src().queue))
	}
	if eng.last().ID != "n1" {
		t.Fatalf("expected the server song to start, got %+v", eng.played)
	}

	key(m, "L") // switch to local
	if m.active != SourceLocal || len(m.src().queue.songs) != 0 {
		t.Fatalf("local queue should start empty, active=%v queue=%q", m.active, songsOf(&m.src().queue))
	}
	// The server track keeps playing; the card says where it's from.
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Server Song") || !strings.Contains(view, "1 / 1 · navidrome") {
		t.Fatalf("now playing should show the navidrome track:\n%s", view)
	}

	run(m, m.ensureLoaded())
	key(m, "a") // queue the local album: doesn't touch the server queue
	if songsOf(&m.src().queue) != "01 Intro,02 Song" || songsOf(&m.sources[SourceNavidrome].queue) != "Server Song" {
		t.Fatalf("queues mixed: local=%q nav=%q", songsOf(&m.src().queue), songsOf(&m.sources[SourceNavidrome].queue))
	}
	if m.playing != SourceNavidrome {
		t.Fatal("queuing locally must not take over playback")
	}

	// Transport keys follow what's playing; enter in the local queue switches.
	key(m, "tab")
	key(m, "enter")
	if m.playing != SourceLocal || eng.last().ID != "t:Band/First Album/01 Intro.wav" {
		t.Fatalf("playing=%v last=%+v", m.playing, eng.last())
	}
	key(m, "n")
	if eng.last().ID != "t:Band/First Album/02 Song.wav" {
		t.Fatalf("next should advance the local queue, got %+v", eng.last())
	}
}

func TestNavidromeUnavailableStartsLocal(t *testing.T) {
	m := New(newFakeEngine(), Options{NavidromeErr: "connection refused", Start: SourceNavidrome})
	if m.active != SourceLocal {
		t.Fatal("should fall back to local when the server is unavailable")
	}
	run(m, m.Init())
	if m.picker == nil {
		t.Fatal("no local folder yet: the picker should open")
	}
	key(m, "esc")
	key(m, "L")
	if m.active != SourceLocal || !strings.Contains(m.flash, "connection refused") {
		t.Fatalf("active=%v flash=%q", m.active, m.flash)
	}
}

func TestPickerChoosesFolder(t *testing.T) {
	root := t.TempDir()
	music := filepath.Join(root, "Music")
	writeWAV(t, filepath.Join(music, "Artist", "Album", "track.wav"))
	os.MkdirAll(filepath.Join(root, ".hidden"), 0o755)
	state := t.TempDir()

	var chosen string
	m := newTestModel(Options{ChooseFolder: func(dir string) (*local.Library, error) {
		chosen = dir
		return local.Open(dir, local.Options{CacheDir: state, DataDir: state})
	}})
	m.engine = newFakeEngine()

	key(m, "L") // no folder yet: picker opens
	if m.picker == nil {
		t.Fatal("picker didn't open")
	}
	m.picker.open(root)
	rows := m.picker.view.items
	if len(rows) != 3 || rows[0].tag != pickUse || rows[2].title != "▸ Music" {
		t.Fatalf("picker rows = %+v", rows)
	}
	key(m, "down")
	key(m, "down")
	key(m, "enter") // open Music
	if m.picker.dir != music {
		t.Fatalf("picker dir = %q", m.picker.dir)
	}
	key(m, "backspace")
	if m.picker.dir != filepath.Clean(root) {
		t.Fatalf("back went to %q", m.picker.dir)
	}

	// Typed path, then use it.
	key(m, "/")
	m.picker.input.SetValue(music)
	key(m, "enter")
	key(m, "enter") // cursor is on "✓ use this folder"
	if chosen != music || m.picker != nil || m.active != SourceLocal {
		t.Fatalf("chosen=%q picker=%v active=%v", chosen, m.picker, m.active)
	}
	if st := m.src().local.Status(); st.Tracks != 1 {
		t.Fatalf("scan after choosing found %d tracks", st.Tracks)
	}
	if !strings.Contains(m.headerInfo()[0], "1 track") {
		t.Fatalf("header = %q", ansi.Strip(m.headerInfo()[0]))
	}
}

func TestViewFitsWithLibraries(t *testing.T) {
	m := New(newFakeEngine(), Options{Navidrome: &fakeLib{}, NavidromeLabel: "alice @ music.local:4533 · navidrome 0.53.3", Local: localLib(t), Start: SourceLocal})
	run(m, m.ensureLoaded())
	for _, sz := range [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 14}} {
		checkFrame(t, m, sz[0], sz[1])
		m.openPicker()
		checkFrame(t, m, sz[0], sz[1])
		m.picker.typing = true
		checkFrame(t, m, sz[0], sz[1])
		m.picker = nil
	}
	empty := New(newFakeEngine(), Options{Start: SourceLocal, NavidromeErr: "offline"})
	checkFrame(t, empty, 100, 30)
	if v := ansi.Strip(empty.View()); !strings.Contains(v, "choose your music folder") {
		t.Fatalf("empty local view:\n%s", v)
	}
}
