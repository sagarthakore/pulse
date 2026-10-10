// Package ui implements the Bubble Tea terminal interface.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sagarthakore/pulse/internal/local"
	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
)

const (
	tickInterval  = 100 * time.Millisecond // also the animation frame rate
	seekStep      = 10 * time.Second
	seekStepLarge = 60 * time.Second
	volumeStep    = 5
	preloadWindow = 30 * time.Second
	restartAfter  = 3 * time.Second
)

type focusArea int

const (
	focusBrowser focusArea = iota
	focusQueue
)

// Options carries startup information for the UI.
type Options struct {
	// Navidrome is the server library; nil when not configured/unreachable.
	Navidrome Library
	// NavidromeLabel is shown in the header (e.g. "alice @ host · navidrome").
	NavidromeLabel string
	// NavidromeErr explains why a configured server is unavailable.
	NavidromeErr string

	// Local is the local folder library; nil until a folder is chosen.
	Local *local.Library
	// ChooseFolder opens (and remembers) a new local music folder.
	ChooseFolder func(root string) (*local.Library, error)

	// Start is the library shown first.
	Start SourceKind
	// Notice is flashed once at startup (e.g. a connection problem).
	Notice string

	// OpaqueBackground paints the Catppuccin Mocha base/mantle backgrounds.
	// By default the terminal's own (possibly translucent) background shows.
	OpaqueBackground bool
}

// Engine is the audio engine the UI drives; *player.Engine in production.
type Engine interface {
	Play(m player.Media)
	Preload(m player.Media)
	ClearPreload()
	Stop()
	TogglePause() bool
	Seek(d time.Duration)
	SetVolume(v int) int
	Status() player.Status
	Spectrum(bands int) []float64
	Events() <-chan player.Event
}

// Model is the root Bubble Tea model.
type Model struct {
	engine Engine
	opts   Options

	width, height int

	sources [numSources]*source
	active  SourceKind // library being viewed
	playing SourceKind // library whose queue drives playback
	seq     int
	focus   focusArea

	status player.Status

	search       textinput.Model
	searchActive bool
	showHelp     bool
	picker       *folderPicker

	flash      string
	flashIsErr bool
	flashUntil time.Time

	scrobbleID string
	scrobbled  bool

	frame   int       // animation frame, advanced every tick
	showViz bool      // spectrum visualizer in the now-playing card
	viz     []float64 // smoothed visualizer levels
}

// New builds the root model.
func New(engine Engine, opts Options) *Model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "artists, albums, songs…"
	ti.CharLimit = 200
	ti.PromptStyle = fg(cPeach).Bold(true)
	ti.TextStyle = sText
	ti.PlaceholderStyle = sFaint
	ti.Cursor.Style = fg(cRosewater)

	m := &Model{
		engine:  engine,
		opts:    opts,
		search:  ti,
		showViz: true,
	}
	nav := m.newSource(SourceNavidrome)
	nav.api, nav.label, nav.unavailable = opts.Navidrome, opts.NavidromeLabel, opts.NavidromeErr
	m.sources[SourceNavidrome] = nav
	m.sources[SourceLocal] = m.newSource(SourceLocal)
	if opts.Local != nil {
		m.setLocal(opts.Local)
	}

	m.active = opts.Start
	if m.active == SourceNavidrome && nav.api == nil {
		m.active = SourceLocal
	}
	m.playing = m.active
	if engine != nil {
		m.status = engine.Status()
	}
	return m
}

// Volume returns the current volume so it can be persisted on exit.
func (m *Model) Volume() int { return m.engine.Status().Volume }

// ActiveSource returns the library being viewed, to restore next time.
func (m *Model) ActiveSource() SourceKind { return m.active }

type tickMsg time.Time

type engineMsg player.Event

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) waitEngine() tea.Cmd {
	ch := m.engine.Events()
	return func() tea.Msg { return engineMsg(<-ch) }
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), m.waitEngine(), tea.SetWindowTitle("pulse")}
	if l := m.sources[SourceLocal].local; l != nil {
		cmds = append(cmds, scanCmd(l))
	}
	if m.active == SourceLocal && m.src().api == nil {
		cmds = append(cmds, m.openPicker())
	} else {
		cmds = append(cmds, m.ensureLoaded())
	}
	if m.opts.Notice != "" {
		m.flashError(fmt.Errorf("%s", m.opts.Notice))
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(tick(), m.onTick())

	case engineMsg:
		return m, tea.Batch(m.waitEngine(), m.onEngineEvent(player.Event(msg)))

	case pageLoadedMsg:
		return m, m.handlePageLoaded(msg)

	case songsMsg:
		return m, m.onSongs(msg)

	case scanDoneMsg:
		return m, m.onScanDone(msg)

	case randomSongsMsg:
		if msg.err != nil {
			m.flashError(msg.err)
			return m, nil
		}
		if len(msg.songs) == 0 {
			m.flashInfo("no songs to mix")
			return m, nil
		}
		s := m.sources[msg.src]
		s.queue.replace(msg.songs, 0)
		m.flashInfo(fmt.Sprintf("playing a random mix of %d songs", len(msg.songs)))
		return m, m.playIndex(s, 0)

	case starMsg:
		if msg.err != nil {
			m.flashError(msg.err)
			return m, nil
		}
		m.applyStar(msg)
		if msg.starred {
			m.flashInfo("★ starred " + msg.title)
		} else {
			m.flashInfo("unstarred " + msg.title)
		}
		return m, nil

	case scrobbleMsg:
		if msg.err != nil {
			m.flashError(fmt.Errorf("scrobble: %w", msg.err))
		}
		return m, nil

	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m, m.moveCursor(-3)
		case tea.MouseButtonWheelDown:
			return m, m.moveCursor(3)
		}
		return m, nil

	case tea.KeyMsg:
		return m, m.onKey(msg)
	}

	if m.searchActive {
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	if m.picker != nil && m.picker.typing {
		var cmd tea.Cmd
		m.picker.input, cmd = m.picker.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// --- playback ---

func (s *source) media(song subsonic.Song) player.Media {
	return player.Media{ID: song.ID, Suffix: song.Suffix, Duration: seconds(song.Duration), Opener: s.api}
}

// playIndex starts entry i of s's queue; s becomes the playing library.
func (m *Model) playIndex(s *source, i int) tea.Cmd {
	if i < 0 || i >= len(s.queue.songs) || s.api == nil {
		return nil
	}
	m.playing = s.kind
	s.queue.cur = i
	song := s.queue.songs[i]
	m.engine.Play(s.media(song))
	m.status = m.engine.Status()
	m.ensurePreload()
	return m.trackStarted(s, song.ID)
}

func (m *Model) trackStarted(s *source, id string) tea.Cmd {
	m.scrobbleID, m.scrobbled = id, false
	return m.scrobble(s, id, false)
}

func (m *Model) togglePause() tea.Cmd {
	if m.status.ID == "" {
		s := m.src()
		if len(s.queue.songs) == 0 {
			m.flashInfo("queue is empty — pick something to play")
			return nil
		}
		return m.playIndex(s, max(0, s.queue.cur))
	}
	m.engine.TogglePause()
	m.status = m.engine.Status()
	return nil
}

func (m *Model) next() tea.Cmd {
	s := m.controlSrc()
	i := s.queue.skipIndex(1)
	if i < 0 {
		m.flashInfo("end of queue")
		return nil
	}
	return m.playIndex(s, i)
}

func (m *Model) prev() tea.Cmd {
	if m.status.ID != "" && m.status.Position > restartAfter {
		m.engine.Seek(0)
		return nil
	}
	s := m.controlSrc()
	i := s.queue.skipIndex(-1)
	if i < 0 {
		if m.status.ID != "" {
			m.engine.Seek(0)
		}
		return nil
	}
	return m.playIndex(s, i)
}

func (m *Model) seekBy(delta time.Duration) {
	if m.status.ID == "" {
		return
	}
	target := m.status.Position + delta
	if d := m.status.Duration; d > 0 && target > d-time.Second {
		target = d - time.Second
	}
	target = max(0, target)
	m.engine.Seek(target)
	m.status.Position = target
}

// ensurePreload keeps the engine's preloaded track in sync with the playing
// queue. The next track is buffered once the current download finishes or
// the current track is nearly over, so the two don't compete for bandwidth.
func (m *Model) ensurePreload() {
	st := m.engine.Status()
	m.status = st
	if st.ID == "" {
		return
	}
	s := m.playSrc()
	ni := s.queue.nextIndex()
	if ni < 0 {
		if st.PreloadID != "" {
			m.engine.ClearPreload()
		}
		return
	}
	next := s.queue.songs[ni]
	if st.PreloadID == next.ID {
		return
	}
	nearEnd := st.Duration > 0 && st.Duration-st.Position < preloadWindow
	if st.Complete || nearEnd {
		m.engine.Preload(s.media(next))
	} else if st.PreloadID != "" {
		m.engine.ClearPreload()
	}
}

func (m *Model) onTick() tea.Cmd {
	m.frame++
	m.ensurePreload()
	m.updateViz()
	if !m.flashUntil.IsZero() && time.Now().After(m.flashUntil) {
		m.flash, m.flashUntil = "", time.Time{}
	}
	cmds := []tea.Cmd{m.refreshLocal()}

	// Scrobble once half the track (or 4 minutes) has played.
	st := m.status
	if st.ID != "" && st.ID == m.scrobbleID && !m.scrobbled && st.Duration >= 30*time.Second {
		threshold := min(st.Duration/2, 4*time.Minute)
		if st.Position >= threshold {
			m.scrobbled = true
			cmds = append(cmds, m.scrobble(m.playSrc(), st.ID, true))
		}
	}
	return tea.Batch(cmds...)
}

// updateViz pulls fresh spectrum levels and eases the bars toward them:
// quick attack, gravity-style fall.
func (m *Model) updateViz() {
	n := m.vizBands()
	if n == 0 {
		m.viz = nil
		return
	}
	var target []float64
	if st := m.status; st.ID != "" && !st.Paused && !st.Buffering {
		target = m.engine.Spectrum(n)
	} else {
		target = make([]float64, n)
	}
	if len(m.viz) != n {
		m.viz = make([]float64, n)
	}
	for i, t := range target {
		if t > m.viz[i] {
			m.viz[i] += (t - m.viz[i]) * 0.7
		} else {
			m.viz[i] = max(t, m.viz[i]-0.06)
		}
	}
}

func (m *Model) onEngineEvent(ev player.Event) tea.Cmd {
	s := m.playSrc()
	switch ev.Kind {
	case player.EventAdvanced:
		ni := s.queue.nextIndex()
		if ni < 0 || s.queue.songs[ni].ID != ev.ID {
			// The queue changed while the swap happened; find the track.
			ni = s.queue.indexOf(ev.ID, s.queue.cur+1)
		}
		if ni >= 0 {
			s.queue.cur = ni
		}
		m.ensurePreload()
		return m.trackStarted(s, ev.ID)
	case player.EventEnded:
		m.status = m.engine.Status()
		if ev.Err != nil {
			m.flashError(fmt.Errorf("playback stopped: %w", ev.Err))
			return nil
		}
		if ni := s.queue.nextIndex(); ni >= 0 {
			return m.playIndex(s, ni)
		}
		return nil
	case player.EventNotice:
		if ev.Err != nil {
			m.flashError(ev.Err)
		}
	}
	return nil
}

func (m *Model) onSongs(msg songsMsg) tea.Cmd {
	if msg.err != nil {
		m.flashError(msg.err)
		return nil
	}
	if len(msg.songs) == 0 {
		m.flashInfo("nothing to add")
		return nil
	}
	s := m.sources[msg.src]
	n := len(msg.songs)
	count := plural(n, "song", "songs")
	var at int // index of the first added song
	switch msg.action {
	case actReplace:
		s.queue.replace(msg.songs, msg.start)
		return m.playIndex(s, msg.start)
	case actPlayNext:
		at = max(0, s.queue.cur+1)
		s.queue.insertNext(msg.songs)
		m.flashInfo(fmt.Sprintf("playing next: %s (%s)", msg.label, count))
	case actAppend:
		at = len(s.queue.songs)
		s.queue.appendSongs(msg.songs)
		m.flashInfo(fmt.Sprintf("queued %s (%s)", msg.label, count))
	}
	if m.status.ID == "" {
		// Nothing was playing: start with what was just added.
		return m.playIndex(s, at)
	}
	m.ensurePreload()
	return nil
}

// --- flash messages ---

func (m *Model) flashInfo(s string) {
	m.flash, m.flashIsErr = s, false
	m.flashUntil = time.Now().Add(4 * time.Second)
}

func (m *Model) flashError(err error) {
	m.flash, m.flashIsErr = strings.TrimSpace(err.Error()), true
	m.flashUntil = time.Now().Add(8 * time.Second)
}
