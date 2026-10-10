package ui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

func songs(ids ...string) []subsonic.Song {
	out := make([]subsonic.Song, len(ids))
	for i, id := range ids {
		out[i] = subsonic.Song{ID: id, Title: id}
	}
	return out
}

func ids(q *queue) string {
	s := ""
	for _, x := range q.songs {
		s += x.ID
	}
	return s
}

func TestQueueNextIndexRepeatModes(t *testing.T) {
	q := newQueue()
	q.replace(songs("a", "b", "c"), 2)

	if got := q.nextIndex(); got != -1 {
		t.Errorf("repeat off at end: next = %d, want -1", got)
	}
	q.repeat = repeatAll
	if got := q.nextIndex(); got != 0 {
		t.Errorf("repeat all at end: next = %d, want 0", got)
	}
	q.repeat = repeatOne
	if got := q.nextIndex(); got != 2 {
		t.Errorf("repeat one: next = %d, want 2", got)
	}
	// Manual skips ignore repeat-one but honour wrap-around.
	if got := q.skipIndex(1); got != 0 {
		t.Errorf("skip with repeat one: %d, want 0", got)
	}
	q.cur = 0
	if got := q.skipIndex(-1); got != 2 {
		t.Errorf("skip back with wrap: %d, want 2", got)
	}
}

func TestQueueInsertNextAndRemove(t *testing.T) {
	q := newQueue()
	q.replace(songs("a", "b", "c"), 1)
	q.insertNext(songs("x", "y"))
	if got := ids(&q); got != "abxyc" {
		t.Fatalf("after insertNext: %s", got)
	}
	if q.cur != 1 {
		t.Fatalf("cur = %d, want 1", q.cur)
	}

	q.remove(0) // before current: cur shifts down
	if got := ids(&q); got != "bxyc" || q.cur != 0 {
		t.Fatalf("after remove(0): %s cur=%d", got, q.cur)
	}
	q.remove(0) // the current track
	if q.cur != -1 {
		t.Fatalf("removing current should clear cur, got %d", q.cur)
	}
}

func TestQueueMoveTracksCurrent(t *testing.T) {
	q := newQueue()
	q.replace(songs("a", "b", "c"), 1)
	if !q.moveItem(1, -1) {
		t.Fatal("move failed")
	}
	if got := ids(&q); got != "bac" || q.cur != 0 {
		t.Fatalf("got %s cur=%d", got, q.cur)
	}
	if q.moveItem(0, -1) {
		t.Fatal("moving past the top should fail")
	}
}

func TestQueueShuffleKeepsPlayedPrefix(t *testing.T) {
	q := newQueue()
	q.replace(songs("a", "b", "c", "d", "e", "f", "g", "h"), 2)
	q.shuffle()
	if got := ids(&q)[:3]; got != "abc" {
		t.Fatalf("shuffle touched played/current tracks: %s", got)
	}
	if len(q.songs) != 8 {
		t.Fatalf("shuffle changed length: %d", len(q.songs))
	}
}

func TestListSkipsHeaders(t *testing.T) {
	l := list{}
	l.setItems([]item{headerItem("A", cBlue), {title: "1"}, headerItem("B", cBlue), {title: "2"}})
	if l.cursor != 1 {
		t.Fatalf("initial cursor = %d, want 1", l.cursor)
	}
	l.move(1)
	if l.cursor != 3 {
		t.Fatalf("cursor = %d, want 3", l.cursor)
	}
	l.move(1)
	if l.cursor != 3 {
		t.Fatalf("cursor moved past end: %d", l.cursor)
	}
	l.top()
	if l.cursor != 1 {
		t.Fatalf("top = %d, want 1", l.cursor)
	}
}

func TestFitHandlesWideAndStyledText(t *testing.T) {
	for _, tc := range []struct {
		in string
		w  int
	}{
		{"日本語のタイトル", 7}, // double-width runes
		{"short", 10},
		{fg(cMauve).Render("styled text that is long"), 8},
		{fg(cMauve).Render("hi"), 8},
	} {
		got := fit(tc.in, tc.w)
		if w := ansi.StringWidth(got); w != tc.w {
			t.Errorf("fit(%q, %d) has width %d", tc.in, tc.w, w)
		}
		if w := ansi.StringWidth(fitLeft(tc.in, tc.w)); w != tc.w {
			t.Errorf("fitLeft(%q, %d) has width %d", tc.in, tc.w, w)
		}
	}
}

func TestRenderRowWidth(t *testing.T) {
	s := subsonic.Song{ID: "1", Title: "Paranoid Android", Artist: "Radiohead", Album: "OK Computer", Duration: 383, Track: 2}
	it := trackSongItem(s, false)
	it.starred = true
	for _, w := range []int{20, 40, 80, 120} {
		for _, sel := range []bool{false, true} {
			for _, playing := range []bool{false, true} {
				row := renderRow(it, w, sel, playing, rowStyle{accent: cMauve, focused: true, marker: sPlaying.Render("▶")})
				if got := ansi.StringWidth(row); got != w {
					t.Errorf("width %d sel=%v playing=%v: rendered %d cells: %q", w, sel, playing, got, row)
				}
			}
		}
	}
}
