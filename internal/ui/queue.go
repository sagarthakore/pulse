package ui

import (
	"fmt"
	"math/rand/v2"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

type repeatMode int

const (
	repeatOff repeatMode = iota
	repeatAll
	repeatOne
)

func (r repeatMode) String() string {
	switch r {
	case repeatAll:
		return "all"
	case repeatOne:
		return "one"
	}
	return "off"
}

// queue is the play queue. cur is the index of the current track or -1.
type queue struct {
	songs  []subsonic.Song
	cur    int
	repeat repeatMode
	view   list
}

func newQueue() queue { return queue{cur: -1} }

func (q *queue) current() *subsonic.Song {
	if q.cur < 0 || q.cur >= len(q.songs) {
		return nil
	}
	return &q.songs[q.cur]
}

// nextIndex returns the track that should follow the current one when it
// finishes naturally, or -1.
func (q *queue) nextIndex() int {
	if len(q.songs) == 0 || q.cur < 0 {
		return -1
	}
	if q.repeat == repeatOne {
		return q.cur
	}
	return q.skipIndex(1)
}

// skipIndex returns the track delta steps from the current one, honouring
// repeat-all wrap-around (but not repeat-one: manual skips always move).
func (q *queue) skipIndex(delta int) int {
	if len(q.songs) == 0 {
		return -1
	}
	i := q.cur + delta
	if i >= 0 && i < len(q.songs) {
		return i
	}
	if q.repeat == repeatOff {
		return -1
	}
	return ((i % len(q.songs)) + len(q.songs)) % len(q.songs)
}

// indexOf finds id searching forward from start, wrapping around; -1 if absent.
func (q *queue) indexOf(id string, start int) int {
	n := len(q.songs)
	for k := 0; k < n; k++ {
		i := ((start+k)%n + n) % n
		if q.songs[i].ID == id {
			return i
		}
	}
	return -1
}

func (q *queue) replace(songs []subsonic.Song, start int) {
	q.songs = append([]subsonic.Song(nil), songs...)
	q.cur = start
	q.view.cursor = max(0, start)
	q.sync()
}

func (q *queue) appendSongs(songs []subsonic.Song) {
	q.songs = append(q.songs, songs...)
	q.sync()
}

// insertNext places songs right after the current track.
func (q *queue) insertNext(songs []subsonic.Song) {
	at := q.cur + 1
	if q.cur < 0 {
		at = 0
	}
	rest := append([]subsonic.Song(nil), q.songs[at:]...)
	q.songs = append(append(q.songs[:at], songs...), rest...)
	q.sync()
}

func (q *queue) remove(i int) {
	if i < 0 || i >= len(q.songs) {
		return
	}
	q.songs = append(q.songs[:i], q.songs[i+1:]...)
	switch {
	case i < q.cur:
		q.cur--
	case i == q.cur:
		// The playing track was removed; the caller decides what plays next.
		q.cur = -1
	}
	q.sync()
}

// moveItem swaps track i with its neighbour in direction delta (±1).
func (q *queue) moveItem(i, delta int) bool {
	j := i + delta
	if i < 0 || j < 0 || i >= len(q.songs) || j >= len(q.songs) {
		return false
	}
	q.songs[i], q.songs[j] = q.songs[j], q.songs[i]
	switch q.cur {
	case i:
		q.cur = j
	case j:
		q.cur = i
	}
	q.view.cursor = j
	q.sync()
	return true
}

// shuffle randomises the tracks after the current one (or all tracks when
// nothing is playing).
func (q *queue) shuffle() {
	start := q.cur + 1
	rest := q.songs[start:]
	rand.Shuffle(len(rest), func(a, b int) { rest[a], rest[b] = rest[b], rest[a] })
	q.sync()
}

func (q *queue) clear() {
	q.songs = nil
	q.cur = -1
	q.view = list{}
}

func (q *queue) cycleRepeat() {
	q.repeat = (q.repeat + 1) % 3
}

// sync rebuilds the visible rows from songs.
func (q *queue) sync() {
	items := make([]item, len(q.songs))
	w := len(fmt.Sprint(len(q.songs)))
	for i, s := range q.songs {
		it := songItem(s, false)
		it.prefix = fmt.Sprintf("%*d", w, i+1)
		items[i] = it
	}
	q.view.setItems(items)
}

func (q *queue) remaining() (n int, total int) {
	for i := max(0, q.cur); i < len(q.songs); i++ {
		n++
		total += q.songs[i].Duration
	}
	return n, total
}
