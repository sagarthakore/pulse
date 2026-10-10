package ui

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

// TestScreenshot renders a sample frame to SVG. It always checks the SVG is
// well-formed; with PULSE_SCREENSHOT=1 it also writes docs/screenshot.svg
// (the README image):
//
//	PULSE_SCREENSHOT=1 go test ./internal/ui -run TestScreenshot
func TestScreenshot(t *testing.T) {
	withTrueColor(t)
	svg := frameToSVG(sampleFrame(), 108, 16)

	d := xml.NewDecoder(strings.NewReader(svg))
	for {
		if _, err := d.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid SVG: %v", err)
		}
	}

	if os.Getenv("PULSE_SCREENSHOT") != "" {
		out := filepath.Join("..", "..", "docs", "screenshot.svg")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte(svg), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", out)
	}
}

// sampleFrame renders the UI with a representative library and queue.
func sampleFrame() string {
	m := newTestModel(Options{})

	albums := []subsonic.Album{
		{ID: "1", Name: "OK Computer", Artist: "Radiohead", Year: 1997},
		{ID: "2", Name: "In Rainbows", Artist: "Radiohead", Year: 2007},
		{ID: "3", Name: "Kid A", Artist: "Radiohead", Year: 2000},
		{ID: "4", Name: "Blue Lines", Artist: "Massive Attack", Year: 1991},
		{ID: "5", Name: "Mezzanine", Artist: "Massive Attack", Year: 1998, Starred: "2024-01-01T00:00:00Z"},
		{ID: "6", Name: "Dummy", Artist: "Portishead", Year: 1994},
		{ID: "7", Name: "Selected Ambient Works 85-92", Artist: "Aphex Twin", Year: 1992},
	}
	var items []item
	for _, a := range albums {
		items = append(items, albumItem(a, true))
	}
	p := m.currentPage()
	p.loaded = true
	p.list.setItems(items)
	p.list.cursor = 1

	song := func(id, title string, secs int) subsonic.Song {
		return subsonic.Song{ID: id, Title: title, Artist: "Radiohead", Album: "OK Computer", Year: 1997, Duration: secs}
	}
	m.src().queue.replace([]subsonic.Song{
		song("s1", "Airbag", 284),
		song("s2", "Paranoid Android", 383),
		song("s3", "Subterranean Homesick Alien", 267),
		song("s4", "Exit Music (For a Film)", 264),
		song("s5", "Let Down", 299),
		song("s6", "Karma Police", 261),
		song("s7", "Fitter Happier", 117),
	}, 1)
	m.src().queue.repeat = repeatAll
	m.status.ID = "s2"
	m.status.Position = 61 * time.Second
	m.status.Duration = 383 * time.Second
	m.status.Downloaded, m.status.Size = 6, 10
	m.status.Codec, m.status.SampleRate, m.status.Volume = "flac", 44100, 80

	m.Update(tea.WindowSizeMsg{Width: 108, Height: 16})
	m.viz = make([]float64, m.vizBands())
	shape := []float64{.55, .9, .75, .95, .7, .8, .6, .72, .5, .62, .45, .5, .38, .42, .3, .35, .25}
	for i := range m.viz {
		m.viz[i] = shape[i%len(shape)]
	}
	return m.View()
}

// --- ANSI → SVG ---

const (
	cellW    = 8.4
	cellH    = 18.0
	fontSize = 14
	padX     = 18.0
	padY     = 14.0
	titleH   = 30.0
)

type cellStyle struct {
	fg, bg          string
	bold, underline bool
}

type cell struct {
	r  rune
	st cellStyle
}

// parseANSI turns styled terminal output into a grid of cells.
func parseANSI(s string, cols, rows int) [][]cell {
	def := cellStyle{fg: string(cText)}
	grid := make([][]cell, rows)
	for y, line := range strings.Split(s, "\n") {
		if y >= rows {
			break
		}
		grid[y] = make([]cell, cols)
		for x := range grid[y] {
			grid[y][x] = cell{r: ' ', st: def}
		}
		st, x := def, 0
		for i := 0; i < len(line); {
			if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
				j := i + 2
				for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
					j++
				}
				if j < len(line) && line[j] == 'm' {
					st = applySGR(st, def, line[i+2:j])
				}
				i = j + 1
				continue
			}
			r, size := []rune(line[i:])[0], len(string([]rune(line[i:])[0]))
			i += size
			w := ansi.StringWidth(string(r))
			if w == 0 || x >= cols {
				continue
			}
			grid[y][x] = cell{r: r, st: st}
			x += w
		}
	}
	return grid
}

func applySGR(st, def cellStyle, params string) cellStyle {
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		n, _ := strconv.Atoi(ps[i])
		switch {
		case ps[i] == "" || n == 0:
			st = def
		case n == 1:
			st.bold = true
		case n == 22:
			st.bold = false
		case n == 4:
			st.underline = true
		case n == 24:
			st.underline = false
		case n == 39:
			st.fg = def.fg
		case n == 49:
			st.bg = ""
		case (n == 38 || n == 48) && i+4 < len(ps) && ps[i+1] == "2":
			r, _ := strconv.Atoi(ps[i+2])
			g, _ := strconv.Atoi(ps[i+3])
			b, _ := strconv.Atoi(ps[i+4])
			c := fmt.Sprintf("#%02x%02x%02x", r, g, b)
			if n == 38 {
				st.fg = c
			} else {
				st.bg = c
			}
			i += 4
		}
	}
	return st
}

// frameToSVG draws the frame in a terminal window. Box-drawing and block
// characters are drawn as shapes and every other glyph is placed in its own
// cell, so the layout is identical whatever fonts the viewer has.
func frameToSVG(frame string, cols, rows int) string {
	grid := parseANSI(frame, cols, rows)
	width := padX*2 + float64(cols)*cellW
	height := titleH + padY*2 + float64(rows)*cellH
	ox, oy := padX, titleH+padY

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img" aria-label="pulse terminal music player">`+"\n", width, height, width, height)
	fmt.Fprintf(&b, `<style>text{font-family:ui-monospace,"Cascadia Mono","SF Mono",Menlo,Consolas,"DejaVu Sans Mono","Liberation Mono",monospace;font-size:%dpx;text-anchor:middle;dominant-baseline:central}.b{font-weight:700}</style>`+"\n", fontSize)

	// Window chrome.
	fmt.Fprintf(&b, `<rect width="%.0f" height="%.0f" rx="10" fill="%s" stroke="%s"/>`+"\n", width, height, cBase, cSurface0)
	fmt.Fprintf(&b, `<path d="M0 10a10 10 0 0 1 10-10h%.0fa10 10 0 0 1 10 10v%.0fh-%.0fz" fill="%s"/>`+"\n", width-20, titleH-10, width, cMantle)
	for i, c := range []string{string(cRed), string(cYellow), string(cGreen)} {
		fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="6" fill="%s"/>`+"\n", 20+float64(i)*20, titleH/2, c)
	}
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s">pulse</text>`+"\n", width/2, titleH/2, cOverlay1)

	for y, row := range grid {
		top := oy + float64(y)*cellH
		// Background runs.
		for x := 0; x < len(row); {
			bg := row[x].st.bg
			end := x + 1
			for end < len(row) && row[end].st.bg == bg {
				end++
			}
			if bg != "" {
				fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`+"\n",
					ox+float64(x)*cellW, top, float64(end-x)*cellW+0.3, cellH+0.3, bg)
			}
			x = end
		}
		// Glyphs.
		for x, c := range row {
			left := ox + float64(x)*cellW
			if shape := cellShape(c.r, left, top, c.st.fg); shape != "" {
				b.WriteString(shape)
				continue
			}
			if c.r != ' ' {
				class := ""
				if c.st.bold {
					class = ` class="b"`
				}
				fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" fill="%s"%s>%s</text>`+"\n",
					left+cellW/2, top+cellH/2, c.st.fg, class, xmlEscape(string(c.r)))
			}
			if c.st.underline {
				fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="1" fill="%s"/>`+"\n",
					left, top+cellH-3, cellW+0.3, c.st.fg)
			}
		}
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// cellShape returns SVG geometry for box-drawing and block characters, or ""
// for characters drawn as text.
func cellShape(r rune, x, y float64, color string) string {
	cx, cy := x+cellW/2, y+cellH/2
	right, bottom := x+cellW, y+cellH
	rad := cellW / 2
	line := func(d string, w float64) string {
		return fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="%.1f"/>`+"\n", d, color, w)
	}
	rect := func(rx, ry, rw, rh float64) string {
		return fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`+"\n", rx, ry, rw, rh, color)
	}
	switch r {
	case '─':
		return line(fmt.Sprintf("M%.1f %.1fH%.1f", x, cy, right+0.3), 1.2)
	case '━':
		return line(fmt.Sprintf("M%.1f %.1fH%.1f", x, cy, right+0.3), 3)
	case '│':
		return line(fmt.Sprintf("M%.1f %.1fV%.1f", cx, y, bottom+0.3), 1.2)
	case '┃':
		return line(fmt.Sprintf("M%.1f %.1fV%.1f", cx, y, bottom+0.3), 3)
	case '╭':
		return line(fmt.Sprintf("M%.1f %.1fV%.1fQ%.1f %.1f %.1f %.1fH%.1f", cx, bottom+0.3, cy+rad, cx, cy, cx+rad, cy, right+0.3), 1.2)
	case '╮':
		return line(fmt.Sprintf("M%.1f %.1fH%.1fQ%.1f %.1f %.1f %.1fV%.1f", x, cy, cx-rad, cx, cy, cx, cy+rad, bottom+0.3), 1.2)
	case '╰':
		return line(fmt.Sprintf("M%.1f %.1fV%.1fQ%.1f %.1f %.1f %.1fH%.1f", cx, y, cy-rad, cx, cy, cx+rad, cy, right+0.3), 1.2)
	case '╯':
		return line(fmt.Sprintf("M%.1f %.1fH%.1fQ%.1f %.1f %.1f %.1fV%.1f", x, cy, cx-rad, cx, cy, cx, cy-rad, y), 1.2)
	case '▌':
		return rect(x, y, cellW/2, cellH+0.3)
	case '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█':
		h := float64(r-'▁'+1) / 8 * cellH
		return rect(x+0.6, bottom-h, cellW-1.2, h+0.3)
	}
	return ""
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
