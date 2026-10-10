package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/sagarthakore/pulse/internal/local"
)

const (
	headerHeight     = 1
	nowPlayingHeight = 5
	footerHeight     = 1
	twoPaneMinWidth  = 90
	vizRows          = 3
	vizMinCardWidth  = 70
)

var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	blockLevels   = []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
)

// View implements tea.Model.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting pulse…"
	}
	if m.width < 40 || m.height < 14 {
		return "terminal too small for pulse"
	}
	bodyH := m.height - headerHeight - nowPlayingHeight - footerHeight
	out := strings.Join([]string{
		m.viewHeader(),
		clampLines(m.viewBody(bodyH), bodyH),
		m.viewNowPlaying(),
		m.viewFooter(),
	}, "\n")
	if !m.opts.OpaqueBackground {
		return out
	}
	return paint(out, cBase, m.width)
}

// panel gives a block a solid background in opaque mode only; in the default
// transparent mode the terminal's own (possibly translucent) background shows.
func (m *Model) panel(s string, bg lipgloss.Color, width int) string {
	if !m.opts.OpaqueBackground {
		return s
	}
	return paint(s, bg, width)
}

func (m *Model) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

// listHeight is the number of list rows visible in the browser pane.
func (m *Model) listHeight() int {
	h := m.height - headerHeight - nowPlayingHeight - footerHeight - 2
	if m.src().tab == tabSearch {
		h--
	}
	return max(1, h)
}

// --- header ---

func (m *Model) viewHeader() string {
	s := m.src()
	tabs := make([]string, numTabs)
	for i, name := range tabNames {
		num := fmt.Sprint(i + 1)
		accent := tabAccents[i]
		if tabID(i) == s.tab {
			tabs[i] = lipgloss.NewStyle().Background(cSurface0).Foreground(accent).Bold(true).
				Render(" " + num + " " + name + " ")
		} else {
			tabs[i] = sFaint.Render(" "+num+" ") + fg(cOverlay2).Render(name+" ")
		}
	}
	left := " " + pill("♫ pulse", cMauve, cCrust) + " " + m.sourceSwitch() + "  " + strings.Join(tabs, " ")
	return m.panel(spreadFirstFit(left, m.headerInfo(), m.width), cMantle, m.width)
}

// sourceSwitch shows both libraries with the active one highlighted.
func (m *Model) sourceSwitch() string {
	var b strings.Builder
	for k := SourceKind(0); k < numSources; k++ {
		label := " " + k.String() + " "
		switch {
		case k == m.active:
			b.WriteString(lipgloss.NewStyle().Background(cSurface0).Foreground(sourceAccents[k]).Bold(true).Render(label))
		case m.sources[k].api == nil && k == SourceNavidrome:
			b.WriteString(fg(cSurface2).Render(label))
		default:
			b.WriteString(fg(cOverlay1).Render(label))
		}
	}
	return b.String()
}

// headerInfo describes the active library, most detailed first; the header
// uses the first that fits.
func (m *Model) headerInfo() []string {
	s := m.src()
	dot := sSuccess.Render("●") + " "
	switch {
	case s.kind == SourceNavidrome && s.api == nil:
		return []string{fg(cRed).Render("○") + sSub.Render(" navidrome unavailable ")}
	case s.kind == SourceNavidrome:
		short, _, _ := strings.Cut(s.label, " · ")
		return []string{dot + sSub.Render(s.label) + " ", dot + sSub.Render(short) + " "}
	case s.local == nil:
		return []string{sFaint.Render("○ no folder chosen ")}
	}
	st := s.local.Status()
	if st.Scanning {
		progress := fmt.Sprintf(" scanning %s/%s", commas(st.Done), commas(st.Total))
		spin := fg(cTeal).Render(m.spinner())
		return []string{
			spin + sSub.Render(progress+" · "+s.label) + " ",
			spin + sSub.Render(progress) + " ",
		}
	}
	tracks := plural(st.Tracks, "track", "tracks")
	return []string{dot + sSub.Render(s.label+" · "+tracks) + " ", dot + sSub.Render(tracks) + " "}
}

// --- panes ---

// pane draws a rounded box with the title (and optional info) set into the
// top border: ╭─ Title ───────── info ─╮
func pane(width, height int, title, info, content string, accent lipgloss.Color, focused bool) string {
	border, titleColor := cSurface1, cOverlay1
	if focused {
		border, titleColor = accent, accent
	}
	bs := fg(border)
	inner := width - 2

	infoPart := ""
	if info != "" {
		infoPart = " " + sDim.Render(truncate(info, max(0, inner/2))) + " "
	}
	titleW := width - 4 - lipgloss.Width(infoPart) - 2
	titlePart := " " + fg(titleColor).Bold(true).Render(truncate(title, max(0, titleW))) + " "
	fill := max(0, width-4-lipgloss.Width(titlePart)-lipgloss.Width(infoPart))

	var b strings.Builder
	b.WriteString(bs.Render("╭─") + titlePart + bs.Render(strings.Repeat("─", fill)) + infoPart + bs.Render("─╮"))
	for _, l := range strings.Split(clampLines(content, max(0, height-2)), "\n") {
		b.WriteString("\n" + bs.Render("│") + fit(l, inner) + bs.Render("│"))
	}
	b.WriteString("\n" + bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return b.String()
}

func (m *Model) viewBody(h int) string {
	if m.picker != nil {
		return m.viewPicker(h)
	}
	if m.showHelp {
		return m.viewHelp(h)
	}
	if m.width < twoPaneMinWidth {
		if m.focus == focusQueue {
			return m.viewQueue(m.width, h)
		}
		return m.viewBrowser(m.width, h)
	}
	qw := max(34, min(70, m.width*2/5))
	bw := m.width - qw
	return lipgloss.JoinHorizontal(lipgloss.Top, m.viewBrowser(bw, h), m.viewQueue(qw, h))
}

func (m *Model) breadcrumb(width int) string {
	stack := m.src().stacks[m.src().tab]
	parts := make([]string, len(stack))
	for i, p := range stack {
		parts[i] = p.title
	}
	p := stack[len(stack)-1]
	if p.kind == pageAlbums {
		parts[len(parts)-1] += " · " + albumTypeNames[p.albumType]
	}
	crumb := strings.Join(parts, " › ")
	// Keep the most specific end of the path visible.
	r := []rune(crumb)
	for len(r) > 0 && lipgloss.Width(string(r)) > width-1 {
		r = r[1:]
	}
	if len(r) < len([]rune(crumb)) {
		return "…" + string(r)
	}
	return crumb
}

// playingMarker is the animated one-cell marker beside the playing track:
// a live level meter while playing.
func (m *Model) playingMarker() string {
	switch {
	case m.status.Paused:
		return fg(cYellow).Render("‖")
	case m.status.Buffering:
		return fg(cPeach).Render(m.spinner())
	}
	level := 0.0
	for _, v := range m.viz {
		level = max(level, v)
	}
	if len(m.viz) == 0 {
		// Visualizer hidden: a simple bouncing meter.
		level = []float64{.3, .6, .9, .6}[m.frame/2%4]
	}
	idx := max(1, min(8, int(level*8+0.5)))
	return sPlaying.Render(blockLevels[idx])
}

func (m *Model) viewBrowser(width, height int) string {
	focused := m.focus == focusBrowser && !m.searchActive
	s := m.src()
	accent := tabAccents[s.tab]
	inner := width - 2
	p := m.currentPage()

	info := ""
	switch {
	case p.loading && len(p.list.items) > 0:
		info = m.spinner()
	case len(p.list.items) > 0:
		info = fmt.Sprint(len(p.list.items))
	}
	title := m.breadcrumb(width - 10 - len(info))

	var b strings.Builder
	rows := height - 2
	if s.tab == tabSearch && s.api != nil {
		switch {
		case m.searchActive:
			m.search.Width = inner - 5
			b.WriteString(" " + m.search.View())
		case p.id != "":
			b.WriteString(fg(cPeach).Render(" / ") + sText.Render(truncate(p.id, inner-4)))
		default:
			b.WriteString(sDim.Render(" press ") + keyBadge("/") + sDim.Render(" to search"))
		}
		b.WriteByte('\n')
		rows--
	}

	var scan local.Status
	if s.local != nil {
		scan = s.local.Status()
	}
	switch {
	case s.kind == SourceLocal && s.api == nil:
		b.WriteString("\n " + fg(cTeal).Render("♫") + sSub.Render("  play music from a folder on this computer") +
			"\n\n " + keyBadge("O") + sDim.Render(" choose your music folder"))
	case s.api == nil:
		reason := s.unavailable
		if reason == "" {
			reason = "no server configured — run pulse --setup"
		}
		b.WriteString(sError.Render(" ✗ Navidrome unavailable") + "\n " + sDim.Render(truncate(reason, inner-2)) +
			"\n\n " + keyBadge("L") + sDim.Render(" switch to local music"))
	case p.err != nil && len(p.list.items) == 0:
		b.WriteString(sError.Render(" ✗ " + truncate(p.err.Error(), inner-4)))
		b.WriteString("\n\n " + keyBadge("ctrl+r") + sDim.Render(" retry"))
	case p.loading && len(p.list.items) == 0:
		b.WriteString(" " + fg(accent).Render(m.spinner()) + sDim.Render(" loading…"))
	case len(p.list.items) == 0 && scan.Scanning:
		b.WriteString(" " + fg(cTeal).Render(m.spinner()) + sDim.Render(fmt.Sprintf(" scanning your music… %s / %s files",
			commas(scan.Done), commas(scan.Total))))
	case len(p.list.items) == 0 && p.loaded:
		if p.kind == pageSearch && p.id != "" {
			b.WriteString(sDim.Render(" no results for ") + sText.Render(p.id))
		} else if p.kind != pageSearch {
			b.WriteString(sDim.Render(" nothing here"))
		}
	default:
		playingID := ""
		if m.playingHere() {
			playingID = m.status.ID
		}
		rs := rowStyle{accent: accent, focused: focused, marker: m.playingMarker()}
		b.WriteString(p.list.render(inner, rows, rs, playingID, -1))
	}
	return pane(width, height, title, info, b.String(), accent, focused)
}

func (m *Model) viewQueue(width, height int) string {
	focused := m.focus == focusQueue
	inner := width - 2
	q := &m.src().queue

	info := ""
	if len(q.songs) > 0 {
		n, secs := q.remaining()
		info = fmt.Sprintf("%d · %s left", n, formatDuration(seconds(secs)))
	}

	var content string
	if len(q.songs) == 0 {
		content = "\n " + fg(queueAccent).Render("♫") + sSub.Render("  the queue is empty") + "\n\n " +
			keyBadge("a") + sDim.Render(" add  ") + keyBadge("enter") + sDim.Render(" play  ") +
			keyBadge("R") + sDim.Render(" mix")
	} else {
		playing := q.cur
		if !m.playingHere() {
			playing = -2 // stopped, or the other library is playing: no marker
		}
		rs := rowStyle{accent: queueAccent, focused: focused, marker: m.playingMarker()}
		content = q.view.render(inner, height-2, rs, "", playing)
	}
	return pane(width, height, "Queue", info, content, queueAccent, focused)
}

// --- now playing ---

func badge(text string, fgc lipgloss.Color) string {
	return lipgloss.NewStyle().Background(cSurface0).Foreground(fgc).Render(" " + text + " ")
}

func keyBadge(k string) string { return badge(k, cLavender) }

func (m *Model) statePill() (string, lipgloss.Color) {
	st := m.status
	switch {
	case st.ID == "":
		return pill("■ STOPPED", cSurface1, cSubtext1), cSurface2
	case st.Paused:
		return pill("‖ PAUSED", cYellow, cCrust), cYellow
	case st.Buffering:
		return pill(m.spinner()+" LOADING", cPeach, cCrust), cPeach
	}
	return pill("▶ PLAYING", cGreen, cCrust), cMauve
}

// vizBands is how many visualizer bars fit in the now-playing card (0 when
// hidden); the tick handler requests exactly this many from the engine.
func (m *Model) vizBands() int {
	cw := m.width - 4
	if !m.showViz || cw < vizMinCardWidth {
		return 0
	}
	return min(48, cw/3) / 2
}

func (m *Model) viewNowPlaying() string {
	w := m.width
	st := m.status
	ps := m.playSrc()
	song := ps.queue.current()
	if song == nil {
		st.ID = ""
	}
	statePill, border := m.statePill()

	cw := w - 4 // inside border and one cell of padding each side
	bars := m.vizBands()
	leftW := cw
	if bars > 0 {
		leftW = cw - bars*2 - 2
	}
	indent := strings.Repeat(" ", lipgloss.Width(statePill)+2)

	var l1, l2 string
	right1 := ""
	// Right-hand extras for line 2, most complete first; the first that
	// fits is used so narrow terminals drop the repeat badge before volume.
	right2 := []string{m.volumeMeter()}
	if ps.queue.repeat != repeatOff {
		right2 = append([]string{badge("↻ "+ps.queue.repeat.String(), cTeal) + "  " + right2[0]}, right2...)
	}
	if st.ID == "" {
		l1 = statePill + "  " + sDim.Render("nothing playing")
		if len(m.src().queue.songs) > 0 {
			l2 = indent + keyBadge("space") + sDim.Render(" resume the queue")
		} else {
			l2 = indent + sDim.Render("pick something and press ") + keyBadge("enter")
		}
	} else {
		l1 = statePill + "  " + fg(cPink).Bold(true).Render(song.Title)
		if st.Codec != "" {
			c := strings.ToUpper(st.Codec)
			if st.SampleRate > 0 {
				c += fmt.Sprintf(" %gkHz", float64(st.SampleRate)/1000)
			}
			right1 = badge(c, cSapphire)
		}
		meta := fg(cBlue).Render(song.Artist)
		if song.Album != "" {
			meta += sFaint.Render(" · ") + fg(cLavender).Render(song.Album)
		}
		if song.Year > 0 {
			meta += sFaint.Render(" · ") + fg(cPeach).Render(fmt.Sprint(song.Year))
		}
		l2 = indent + meta
	}
	left := []string{
		spread(l1, right1, leftW),
		spreadFirstFit(l2, right2, leftW),
		m.progressBar(leftW),
	}

	viz := m.renderViz(bars)
	lines := make([]string, vizRows)
	for i := range lines {
		lines[i] = " " + fit(left[i], leftW)
		if bars > 0 {
			lines[i] += "  " + viz[i]
		}
	}

	info := ""
	if n := len(ps.queue.songs); st.ID != "" && n > 0 && ps.queue.cur >= 0 {
		info = fmt.Sprintf("%d / %d", ps.queue.cur+1, n)
		if m.active != m.playing {
			info += " · " + ps.kind.String()
		}
	}
	card := pane(w, nowPlayingHeight, "Now Playing", info, strings.Join(lines, "\n"), border, true)
	return m.panel(card, cMantle, w)
}

func (m *Model) volumeMeter() string {
	const cells = 8
	vol := m.status.Volume
	filled := (vol*cells + 50) / 100
	cols := gradient(volumeStops, cells)
	var b strings.Builder
	b.WriteString(sFaint.Render("vol "))
	for i := 0; i < cells; i++ {
		if i < filled {
			b.WriteString(fg(cols[i]).Render("━"))
		} else {
			b.WriteString(fg(cSurface1).Render("─"))
		}
	}
	b.WriteString(sSub.Render(fmt.Sprintf(" %3d%%", vol)))
	return b.String()
}

func (m *Model) progressBar(w int) string {
	st := m.status
	pos := formatDuration(st.Position)
	dur := "--:--"
	if st.Duration > 0 {
		dur = formatDuration(st.Duration)
	}
	if st.ID == "" {
		pos, dur = "0:00", "0:00"
	}
	barW := w - len(pos) - len(dur) - 2
	if barW < 5 {
		return sText.Render(pos) + sFaint.Render(" / ") + sDim.Render(dur)
	}

	played := 0
	if st.Duration > 0 {
		played = int(float64(barW) * float64(st.Position) / float64(st.Duration))
	}
	loaded := 0
	switch {
	case st.Complete:
		loaded = barW
	case st.Size > 0:
		loaded = int(float64(barW) * float64(st.Downloaded) / float64(st.Size))
	}
	played = max(0, min(barW, played))
	loaded = max(played, min(barW, loaded))
	knob := st.ID != "" && st.Duration > 0
	if knob && played == barW {
		played-- // keep room for the playhead
	}

	cols := gradient(progressStops, barW)
	var b strings.Builder
	for i := 0; i < played; i++ {
		b.WriteString(fg(cols[i]).Render("━"))
	}
	rest := barW - played
	if knob {
		b.WriteString(fg(cRosewater).Render("●"))
		rest--
	}
	ahead := max(0, min(rest, loaded-played-1))
	b.WriteString(fg(cSurface2).Render(strings.Repeat("━", ahead)))
	b.WriteString(fg(cSurface1).Render(strings.Repeat("─", rest-ahead)))

	return sText.Render(pos) + " " + b.String() + " " + sDim.Render(dur)
}

// renderViz draws the spectrum as vizRows rows of bars with gaps.
func (m *Model) renderViz(bars int) []string {
	out := make([]string, vizRows)
	if bars == 0 {
		return out
	}
	cols := gradient(visualizerStops, bars)
	steps := vizRows * 8
	for r := 0; r < vizRows; r++ {
		var b strings.Builder
		floor := (vizRows - 1 - r) * 8
		for i := 0; i < bars; i++ {
			level := 0.0
			if i < len(m.viz) {
				level = m.viz[i]
			}
			fill := max(0, min(8, int(level*float64(steps)+0.5)-floor))
			switch {
			case fill > 0:
				b.WriteString(fg(cols[i]).Render(blockLevels[fill]))
			case r == vizRows-1:
				b.WriteString(fg(cSurface1).Render("▁")) // baseline
			default:
				b.WriteString(" ")
			}
			b.WriteString(" ")
		}
		out[r] = b.String()
	}
	return out
}

// --- footer ---

func (m *Model) viewFooter() string {
	w := m.width
	var line string
	if m.flash != "" {
		if m.flashIsErr {
			line = " " + pill("✗", cRed, cCrust) + " " + sError.Render(m.flash)
		} else {
			line = " " + pill("✓", cGreen, cCrust) + " " + sSuccess.Render(m.flash)
		}
		return m.panel(fit(line, w), cMantle, w)
	}

	hints := []helpEntry{
		{"space", "play/pause"},
		{"n/p", "next/prev"},
		{"←/→", "seek"},
		{"enter", "open/play"},
		{"a", "queue"},
		{"/", "search"},
		{"L", "library"},
		{"?", "help"},
		{"tab", "focus"},
		{"v", "visualizer"},
		{"q", "quit"},
	}
	switch {
	case m.picker != nil && m.picker.typing:
		hints = []helpEntry{{"enter", "go to path"}, {"esc", "cancel"}}
	case m.picker != nil:
		hints = []helpEntry{{"↑/↓", "move"}, {"enter", "open / use"}, {"⌫", "up"}, {"/", "type a path"}, {"~", "home"}, {"esc", "cancel"}}
	case m.searchActive:
		hints = []helpEntry{{"enter", "search"}, {"esc", "cancel"}}
	}
	var b strings.Builder
	b.WriteString(" ")
	used := 1
	for _, h := range hints {
		seg := keyBadge(h.keys) + " " + sDim.Render(h.desc) + "  "
		sw := lipgloss.Width(seg)
		if used+sw > w {
			break
		}
		b.WriteString(seg)
		used += sw
	}
	return m.panel(b.String(), cMantle, w)
}

// --- help ---

// helpColors follow helpSections: Playback, Browse, Libraries, Queue, General.
var helpColors = []lipgloss.Color{cGreen, cBlue, cTeal, cPink, cPeach}

// helpColumns splits helpSections into two balanced columns.
var helpColumns = [2][]int{{0, 2, 3}, {1, 4}}

func (m *Model) viewHelp(h int) string {
	var blocks []string
	for i, sec := range helpSections {
		c := helpColors[i%len(helpColors)]
		var b strings.Builder
		b.WriteString(fg(c).Bold(true).Render("◆ "+sec.title) + "\n")
		for _, e := range sec.entries {
			b.WriteString(fg(c).Render(fit("  "+e.keys, 16)) + sText.Render(e.desc) + "\n")
		}
		blocks = append(blocks, b.String())
	}

	card := func(content string) string {
		lines := strings.Split(strings.TrimRight(content, "\n "), "\n")
		for i, l := range lines {
			lines[i] = "  " + l
		}
		body := "\n" + strings.Join(lines, "\n") + "\n"
		w := lipgloss.Width(body) + 4
		box := pane(w, len(lines)+4, "Keys", "? or esc to close", body, cMauve, true)
		return m.panel(box, cMantle, w)
	}
	column := func(idx []int) string {
		var parts []string
		for _, i := range idx {
			parts = append(parts, blocks[i])
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	twoCol := card(lipgloss.JoinHorizontal(lipgloss.Top, column(helpColumns[0]), "    ", column(helpColumns[1])))
	oneCol := card(lipgloss.JoinVertical(lipgloss.Left, blocks...))

	box := oneCol
	if lipgloss.Width(twoCol) <= m.width &&
		(lipgloss.Height(oneCol) > h || lipgloss.Width(oneCol) > m.width) {
		box = twoCol
	}
	if lipgloss.Width(box) > m.width || lipgloss.Height(box) > h {
		// Tiny terminal: show what fits rather than wrapping.
		lines := strings.Split(box, "\n")
		for i, l := range lines {
			lines[i] = truncate(l, m.width)
		}
		return clampLines(strings.Join(lines, "\n"), h)
	}
	return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, box)
}

// --- folder picker ---

func (m *Model) viewPicker(h int) string {
	p := m.picker
	w := max(40, min(m.width-4, 96))
	ph := min(h, 24)
	inner := w - 2

	loc := p.dir
	if loc == "" {
		loc = "drives"
	}
	var b strings.Builder
	b.WriteString(" " + fg(cPeach).Bold(true).Render(truncate(loc, inner-2)) + "\n")
	if p.typing {
		p.input.Width = inner - 10
		b.WriteString(" " + p.input.View())
	} else {
		b.WriteString(" " + keyBadge("/") + sDim.Render(" type or paste a path   ") + keyBadge("enter") + sDim.Render(" on ✓ to use a folder"))
	}
	b.WriteString("\n")
	rows := ph - 2 - 3
	if p.err != nil {
		b.WriteString(sError.Render(" ✗ "+truncate(p.err.Error(), inner-4)) + "\n")
		rows--
	}
	b.WriteString("\n")
	rs := rowStyle{accent: cTeal, focused: !p.typing}
	b.WriteString(p.view.render(inner, max(1, rows), rs, "", -1))

	card := m.panel(pane(w, ph, "Choose your music folder", "", b.String(), cTeal, true), cMantle, w)
	return lipgloss.Place(m.width, h, lipgloss.Center, lipgloss.Center, card)
}
