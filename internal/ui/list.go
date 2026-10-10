package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

type itemKind int

const (
	kindSong itemKind = iota
	kindAlbum
	kindArtist
	kindPlaylist
	kindFolder // folder picker entry
	kindHeader // non-selectable section heading
)

// item is one row in a list pane.
type item struct {
	kind    itemKind
	id      string
	title   string
	sub     string // secondary column (artist, album, counts, ...)
	right   string // right-aligned column (duration, year)
	prefix  string // short leading column (track number)
	starred bool
	accent  lipgloss.Color // header color
	tag     string         // internal marker (folder picker actions)
	song    *subsonic.Song
}

// list is a scrollable cursor over items.
type list struct {
	items  []item
	cursor int
	offset int
}

func (l *list) selected() *item {
	if l.cursor < 0 || l.cursor >= len(l.items) || l.items[l.cursor].kind == kindHeader {
		return nil
	}
	return &l.items[l.cursor]
}

// move shifts the cursor by delta, skipping headers.
func (l *list) move(delta int) {
	if len(l.items) == 0 {
		return
	}
	c := max(0, min(len(l.items)-1, l.cursor+delta))
	step := 1
	if delta < 0 {
		step = -1
	}
	for c >= 0 && c < len(l.items) && l.items[c].kind == kindHeader {
		c += step
	}
	if c < 0 || c >= len(l.items) {
		// Ran past the end looking for a selectable row: search the other way.
		c = max(0, min(len(l.items)-1, l.cursor+delta))
		for c >= 0 && c < len(l.items) && l.items[c].kind == kindHeader {
			c -= step
		}
		if c < 0 || c >= len(l.items) {
			return
		}
	}
	l.cursor = c
}

func (l *list) top()    { l.cursor = 0; l.move(0) }
func (l *list) bottom() { l.cursor = len(l.items) - 1; l.move(0) }

// setItems replaces the items, keeping the cursor in range.
func (l *list) setItems(items []item) {
	l.items = items
	if l.cursor >= len(items) {
		l.cursor = len(items) - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	l.move(0)
}

// scroll keeps the cursor visible within height rows.
func (l *list) scroll(height int) {
	if height <= 0 {
		return
	}
	margin := min(3, height/4)
	if l.cursor < l.offset+margin {
		l.offset = l.cursor - margin
	}
	if l.cursor >= l.offset+height-margin {
		l.offset = l.cursor - height + margin + 1
	}
	l.offset = max(0, min(l.offset, len(l.items)-height))
}

// rowStyle carries per-pane rendering options.
type rowStyle struct {
	accent  lipgloss.Color // selection bar color
	focused bool
	marker  string // styled one-cell "now playing" marker (animated)
}

// render draws the visible rows. playingID marks the song currently playing;
// playingIndex (>=0) marks a row by position instead (used by the queue,
// which may contain the same song twice).
func (l *list) render(width, height int, rs rowStyle, playingID string, playingIndex int) string {
	l.scroll(height)
	var b strings.Builder
	end := min(len(l.items), l.offset+height)
	for i := l.offset; i < end; i++ {
		it := l.items[i]
		playing := false
		if playingIndex >= 0 {
			playing = i == playingIndex
		} else if playingID != "" && it.kind == kindSong && it.id == playingID {
			playing = true
		}
		b.WriteString(renderRow(it, width, i == l.cursor, playing, rs))
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Column colors by item kind.
func subColor(k itemKind) lipgloss.Color {
	switch k {
	case kindAlbum:
		return cLavender
	case kindPlaylist, kindFolder:
		return cOverlay2
	}
	return cSubtext0
}

func rightColor(k itemKind) lipgloss.Color {
	if k == kindAlbum {
		return cPeach // year
	}
	return cOverlay1
}

// renderRow lays out: bar | marker | prefix | title ★ | sub | right.
func renderRow(it item, width int, selected, playing bool, rs rowStyle) string {
	if it.kind == kindHeader {
		return renderHeader(it, width)
	}

	bar := " "
	if selected {
		c := cSurface2
		if rs.focused {
			c = rs.accent
		}
		bar = fg(c).Render("▌")
	}
	marker := " "
	if playing {
		marker = rs.marker
		if marker == "" {
			marker = sPlaying.Render("▶")
		}
	}

	prefixW := 0
	if it.prefix != "" {
		prefixW = len([]rune(it.prefix)) + 1
	}
	rightW := 0
	if it.right != "" {
		rightW = len([]rune(it.right)) + 1
	}
	rest := width - 3 - prefixW - rightW - 1
	titleW, subW := rest, 0
	if it.sub != "" && rest >= 30 {
		subW = rest * 2 / 5
		titleW = rest - subW - 1
	}
	starW := 0
	if it.starred && titleW > 4 {
		starW = 2
	}

	titleStyle := sText
	switch {
	case playing:
		titleStyle = sPlaying
	case selected && rs.focused:
		titleStyle = fg(rs.accent).Bold(true)
	case selected:
		titleStyle = fg(cText).Bold(true)
	}

	var row strings.Builder
	row.WriteString(bar + marker + " ")
	if prefixW > 0 {
		row.WriteString(sFaint.Render(fit(it.prefix, prefixW)))
	}
	row.WriteString(titleStyle.Render(fit(it.title, titleW-starW)))
	if starW > 0 {
		row.WriteString(sStar.Render(" ★"))
	}
	if subW > 0 {
		row.WriteString(" " + fg(subColor(it.kind)).Render(fit(it.sub, subW)))
	}
	if rightW > 0 {
		row.WriteString(fg(rightColor(it.kind)).Render(fitLeft(it.right, rightW)))
	}
	row.WriteString(" ")

	if selected {
		return paint(row.String(), cSurface0, width)
	}
	return row.String()
}

// renderHeader draws a section heading: " ◆ Albums (12) ─────────".
func renderHeader(it item, width int) string {
	c := it.accent
	if c == "" {
		c = cPink
	}
	label := " ◆ " + it.title + " "
	ruleW := width - lipgloss.Width(label) - 1
	if ruleW < 1 {
		return fg(c).Bold(true).Render(fit(label, width))
	}
	return fg(c).Bold(true).Render(label) + fg(cSurface1).Render(strings.Repeat("─", ruleW)) + " "
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

// --- item constructors ---

func songItem(s subsonic.Song, withAlbum bool) item {
	sub := s.Artist
	if withAlbum && s.Album != "" {
		sub = s.Artist + " · " + s.Album
	}
	song := s
	return item{
		kind:    kindSong,
		id:      s.ID,
		title:   s.Title,
		sub:     sub,
		right:   formatDuration(seconds(s.Duration)),
		starred: s.Starred != "",
		song:    &song,
	}
}

func trackSongItem(s subsonic.Song, multiDisc bool) item {
	it := songItem(s, false)
	switch {
	case s.Track > 0 && multiDisc && s.DiscNumber > 0:
		it.prefix = fmt.Sprintf("%d-%02d", s.DiscNumber, s.Track)
	case s.Track > 0:
		it.prefix = fmt.Sprintf("%02d", s.Track)
	}
	return it
}

func albumItem(a subsonic.Album, withArtist bool) item {
	sub := fmt.Sprintf("%d tracks", a.SongCount)
	if withArtist && a.Artist != "" {
		sub = a.Artist
	}
	right := ""
	if a.Year > 0 {
		right = fmt.Sprint(a.Year)
	}
	return item{kind: kindAlbum, id: a.ID, title: a.Name, sub: sub, right: right, starred: a.Starred != ""}
}

func artistItem(a subsonic.Artist) item {
	return item{
		kind:    kindArtist,
		id:      a.ID,
		title:   a.Name,
		right:   fmt.Sprintf("%d", a.AlbumCount),
		starred: a.Starred != "",
	}
}

func playlistItem(p subsonic.Playlist) item {
	return item{
		kind:  kindPlaylist,
		id:    p.ID,
		title: p.Name,
		sub:   fmt.Sprintf("%d tracks · %s", p.SongCount, p.Owner),
		right: formatDuration(seconds(p.Duration)),
	}
}

func headerItem(title string, accent lipgloss.Color) item {
	return item{kind: kindHeader, title: title, accent: accent}
}
