package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

// testModel builds a model with populated pages and queue but no engine;
// View only reads m.status, so no audio device is needed.
func testModel() *Model {
	m := newTestModel(Options{})
	var albums []item
	for i := 0; i < 60; i++ {
		albums = append(albums, albumItem(subsonic.Album{
			ID: fmt.Sprint(i), Name: fmt.Sprintf("Album with a fairly long name number %d", i),
			Artist: "Some Artist 日本", Year: 1990 + i%30, SongCount: 10,
		}, true))
	}
	p := m.currentPage()
	p.loaded = true
	p.list.setItems(albums)

	var songs []subsonic.Song
	for i := 0; i < 25; i++ {
		songs = append(songs, subsonic.Song{
			ID: fmt.Sprint("s", i), Title: fmt.Sprintf("Track %d — a title that goes on", i),
			Artist: "Artist", Album: "Album", Duration: 200 + i, Year: 1997,
		})
	}
	m.src().queue.replace(songs, 3)
	m.status.ID = "s3"
	m.status.Position = 61e9
	m.status.Duration = 203e9
	m.status.Downloaded, m.status.Size = 5, 10
	m.status.Codec, m.status.SampleRate, m.status.Volume = "flac", 44100, 80
	return m
}

func checkFrame(t *testing.T, m *Model, w, h int) {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	out := m.View()
	lines := strings.Split(out, "\n")
	if w >= 40 && h >= 14 && len(lines) != h {
		t.Errorf("%dx%d: rendered %d lines", w, h, len(lines))
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("%dx%d: line %d is %d cells wide: %q", w, h, i, lw, ansi.Strip(l))
		}
	}
}

func TestViewFitsTerminal(t *testing.T) {
	sizes := [][2]int{{120, 40}, {100, 30}, {89, 24}, {80, 24}, {72, 20}, {60, 20}, {40, 14}, {40, 12}, {30, 8}, {200, 60}}
	for _, sz := range sizes {
		m := testModel()
		checkFrame(t, m, sz[0], sz[1])

		m.focus = focusQueue
		checkFrame(t, m, sz[0], sz[1])

		m.showHelp = true
		checkFrame(t, m, sz[0], sz[1])
		m.showHelp = false

		m.src().tab = tabSearch
		m.searchActive = true
		m.search.Focus()
		checkFrame(t, m, sz[0], sz[1])

		m.status.ID = "" // stopped
		m.flashError(fmt.Errorf("a long error message that should be truncated to the terminal width without wrapping"))
		checkFrame(t, m, sz[0], sz[1])
	}
}

// withTrueColor renders with 24-bit colour, as Windows Terminal does.
func withTrueColor(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestMochaBackgroundCoversEveryCell(t *testing.T) {
	withTrueColor(t)
	const w, h = 120, 40
	m := testModel()
	m.opts.OpaqueBackground = true
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.viz = make([]float64, m.vizBands())
	for i := range m.viz {
		m.viz[i] = float64(i%7) / 6
	}
	out := m.View()

	for _, c := range []lipgloss.Color{cBase, cMantle, cSurface0} {
		if !strings.Contains(out, bgSeq(c)) {
			t.Errorf("no %s background in frame", c)
		}
	}
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%d lines, want %d", len(lines), h)
	}
	base := bgSeq(cBase)
	for i, l := range lines {
		if !strings.HasPrefix(l, base) {
			t.Fatalf("line %d doesn't start on the Mocha base: %q", i, l[:min(len(l), 40)])
		}
		if lw := ansi.StringWidth(l); lw != w {
			t.Fatalf("line %d is %d cells, want %d", i, lw, w)
		}
		// Every reset must immediately re-assert a background, except the
		// final one closing the line; otherwise the terminal's own
		// background would show through.
		body := strings.TrimSuffix(l, sgrReset)
		for j := strings.Index(body, sgrReset); j >= 0; {
			rest := body[j+len(sgrReset):]
			if !strings.HasPrefix(rest, "\x1b[48;2;") {
				t.Fatalf("line %d: reset not followed by a background at byte %d: %q", i, j, ansi.Strip(rest))
			}
			k := strings.Index(rest, sgrReset)
			if k < 0 {
				break
			}
			j += len(sgrReset) + k
		}
	}
}

func TestDefaultBackgroundIsTransparent(t *testing.T) {
	withTrueColor(t)
	m := testModel() // OpaqueBackground defaults to false
	for _, help := range []bool{false, true} {
		m.showHelp = help
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		out := m.View()
		for _, c := range []lipgloss.Color{cBase, cMantle} {
			if strings.Contains(out, bgSeq(c)) {
				t.Fatalf("help=%v: %s background painted in transparent mode", help, c)
			}
		}
		// Small accents (selection bar, pills) keep their own backgrounds.
		if !help && !strings.Contains(out, bgSeq(cSurface0)) {
			t.Fatal("selection highlight missing")
		}
	}
}

func TestVisualizerBars(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	n := m.vizBands()
	if n == 0 {
		t.Fatal("visualizer hidden at 120 columns")
	}
	m.viz = make([]float64, n)
	m.viz[0] = 1 // full height
	rows := m.renderViz(n)
	for r, row := range rows {
		if got := ansi.StringWidth(row); got != n*2 {
			t.Fatalf("row %d width %d, want %d", r, got, n*2)
		}
		if !strings.HasPrefix(ansi.Strip(row), "█") {
			t.Fatalf("row %d of a full bar = %q", r, ansi.Strip(row))
		}
	}
	m.showViz = false
	if m.vizBands() != 0 {
		t.Fatal("visualizer still allocated bars when hidden")
	}
}

func TestBreadcrumbTruncatesFromLeft(t *testing.T) {
	m := testModel()
	s := m.src()
	s.stacks[s.tab] = append(s.stacks[s.tab],
		m.newPage(pageArtist, "1", "A Very Long Artist Name Indeed"),
		m.newPage(pageAlbum, "2", "日本語のアルバム"))
	got := m.breadcrumb(20)
	if w := ansi.StringWidth(got); w > 20 {
		t.Fatalf("breadcrumb width %d > 20: %q", w, got)
	}
	if !strings.HasSuffix(got, "日本語のアルバム") || !strings.HasPrefix(got, "…") {
		t.Fatalf("breadcrumb = %q", got)
	}
}
