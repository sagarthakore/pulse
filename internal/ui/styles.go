package ui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

// Catppuccin Mocha — https://catppuccin.com/palette
const (
	cRosewater = lipgloss.Color("#f5e0dc")
	cFlamingo  = lipgloss.Color("#f2cdcd")
	cPink      = lipgloss.Color("#f5c2e7")
	cMauve     = lipgloss.Color("#cba6f7")
	cRed       = lipgloss.Color("#f38ba8")
	cMaroon    = lipgloss.Color("#eba0ac")
	cPeach     = lipgloss.Color("#fab387")
	cYellow    = lipgloss.Color("#f9e2af")
	cGreen     = lipgloss.Color("#a6e3a1")
	cTeal      = lipgloss.Color("#94e2d5")
	cSky       = lipgloss.Color("#89dceb")
	cSapphire  = lipgloss.Color("#74c7ec")
	cBlue      = lipgloss.Color("#89b4fa")
	cLavender  = lipgloss.Color("#b4befe")
	cText      = lipgloss.Color("#cdd6f4")
	cSubtext1  = lipgloss.Color("#bac2de")
	cSubtext0  = lipgloss.Color("#a6adc8")
	cOverlay2  = lipgloss.Color("#9399b2")
	cOverlay1  = lipgloss.Color("#7f849c")
	cOverlay0  = lipgloss.Color("#6c7086")
	cSurface2  = lipgloss.Color("#585b70")
	cSurface1  = lipgloss.Color("#45475a")
	cSurface0  = lipgloss.Color("#313244")
	cBase      = lipgloss.Color("#1e1e2e")
	cMantle    = lipgloss.Color("#181825")
	cCrust     = lipgloss.Color("#11111b")
)

// Each browser tab has its own accent; the queue is pink.
var (
	tabAccents  = [numTabs]lipgloss.Color{cBlue, cMauve, cGreen, cPeach}
	queueAccent = cPink
)

// Gradients used for the progress bar and the visualizer.
var (
	progressStops   = []lipgloss.Color{cMauve, cPink, cFlamingo}
	visualizerStops = []lipgloss.Color{cMauve, cPink, cPeach, cYellow, cGreen, cTeal, cSky, cBlue}
	volumeStops     = []lipgloss.Color{cTeal, cGreen}
)

func fg(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

var (
	sText    = fg(cText)
	sSub     = fg(cSubtext0)
	sDim     = fg(cOverlay1)
	sFaint   = fg(cOverlay0)
	sStar    = fg(cYellow)
	sError   = fg(cRed)
	sSuccess = fg(cGreen)
	sPlaying = fg(cGreen).Bold(true)
)

// pill renders text as a rounded-looking chip with a solid background.
func pill(text string, bg, fgc lipgloss.Color) string {
	return lipgloss.NewStyle().Background(bg).Foreground(fgc).Bold(true).Render(" " + text + " ")
}

// --- background painting ---
//
// lipgloss backgrounds don't survive nested styled segments (every segment
// ends with a full reset), so backgrounds are applied by re-asserting the
// colour after each reset. Painting is composable: an inner region painted
// mantle inside a base-painted screen keeps its mantle, because the inner
// colour is re-asserted after the outer one.

const sgrReset = "\x1b[0m"

var (
	bgSeqMu    sync.Mutex
	bgSeqCache = map[string]string{}
)

func bgSeq(c lipgloss.Color) string {
	profile := lipgloss.ColorProfile()
	key := fmt.Sprintf("%d/%s", profile, c)
	bgSeqMu.Lock()
	defer bgSeqMu.Unlock()
	if s, ok := bgSeqCache[key]; ok {
		return s
	}
	s := ""
	if col := profile.Color(string(c)); col != nil {
		if seq := col.Sequence(true); seq != "" {
			s = termenv.CSI + seq + "m"
		}
	}
	bgSeqCache[key] = s
	return s
}

// paint gives every line of s the background bg, padded to width cells.
func paint(s string, bg lipgloss.Color, width int) string {
	seq := bgSeq(bg)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		pad := strings.Repeat(" ", max(0, width-ansi.StringWidth(l)))
		if seq == "" {
			lines[i] = l + pad
			continue
		}
		lines[i] = seq + strings.ReplaceAll(l, sgrReset, sgrReset+seq) + pad + sgrReset
	}
	return strings.Join(lines, "\n")
}

// --- gradients ---

var (
	gradientMu    sync.Mutex
	gradientCache = map[string][]lipgloss.Color{}
)

// gradient returns n colours blended evenly through stops.
func gradient(stops []lipgloss.Color, n int) []lipgloss.Color {
	if n <= 0 {
		return nil
	}
	key := fmt.Sprintf("%v/%d", stops, n)
	gradientMu.Lock()
	defer gradientMu.Unlock()
	if g, ok := gradientCache[key]; ok {
		return g
	}
	cols := make([]colorful.Color, len(stops))
	for i, s := range stops {
		cols[i], _ = colorful.Hex(string(s))
	}
	out := make([]lipgloss.Color, n)
	for i := range out {
		if len(cols) == 1 {
			out[i] = stops[0]
			continue
		}
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		seg := t * float64(len(cols)-1)
		j := min(int(seg), len(cols)-2)
		out[i] = lipgloss.Color(cols[j].BlendLab(cols[j+1], seg-float64(j)).Clamped().Hex())
	}
	gradientCache[key] = out
	return out
}

// --- text fitting ---

// truncate shortens s (which may contain ANSI styling) to at most w terminal
// cells, adding an ellipsis.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// fit truncates or pads s to exactly w terminal cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	t := truncate(s, w)
	return t + strings.Repeat(" ", max(0, w-ansi.StringWidth(t)))
}

// fitLeft right-aligns s in w terminal cells.
func fitLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	t := truncate(s, w)
	return strings.Repeat(" ", max(0, w-ansi.StringWidth(t))) + t
}

// spread places left and right at the edges of a w-cell line.
func spread(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// spreadFirstFit is spread with the first of rights that fits beside left.
func spreadFirstFit(left string, rights []string, w int) string {
	for _, r := range rights {
		if ansi.StringWidth(left)+1+ansi.StringWidth(r) <= w {
			return spread(left, r, w)
		}
	}
	return fit(left, w)
}

// clampLines pads or cuts a block of text to exactly h lines.
func clampLines(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
