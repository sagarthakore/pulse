// Package local is a music library backed by a folder on disk. It exposes
// the same browse API as the Navidrome client so the UI can treat both alike.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
)

// Options configures a Library.
type Options struct {
	// FFmpeg is the path to ffmpeg, used for formats pulse can't decode
	// itself; "" disables those files.
	FFmpeg string
	// CacheDir holds the scan cache.
	CacheDir string
	// DataDir holds favourites and play counts.
	DataDir string
}

// Status describes the library and any scan in progress.
type Status struct {
	Scanning bool
	Done     int // files processed by the current scan
	Total    int // files found by the current scan
	Tracks   int // playable tracks in the library
	// Skipped counts files that need ffmpeg, which isn't available.
	Skipped int
	// Generation increases whenever the library contents change.
	Generation int
	Err        error
}

// Library is a scanned music folder.
type Library struct {
	root string
	opts Options

	cachePath string
	stats     *stats

	scanMu sync.Mutex // one scan at a time

	mu        sync.RWMutex
	records   []record
	playlists []string // slash-separated paths relative to root
	idx       *index
	status    Status
	randOrder []*album // stable order for paging the random album list
}

// Open loads the library for root from its cache (if any). It doesn't scan;
// call Scan to pick up changes.
func Open(root string, opts Options) (*Library, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", abs)
	}
	key := rootKey(abs)
	l := &Library{
		root:      abs,
		opts:      opts,
		cachePath: filepath.Join(opts.CacheDir, "library-"+key+".json"),
		stats:     loadStats(filepath.Join(opts.DataDir, "local-"+key+".json")),
		idx:       &index{},
	}
	if c, err := loadCache(l.cachePath); err == nil && c.Root == abs {
		l.records, l.playlists = c.Records, c.Playlists
		l.rebuild()
	}
	return l, nil
}

// FindFFmpeg returns configured if it exists, else ffmpeg from PATH, else "".
func FindFFmpeg(configured string) string {
	if configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
		return ""
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// Root returns the library folder.
func (l *Library) Root() string { return l.root }

// HasFFmpeg reports whether formats needing conversion are playable.
func (l *Library) HasFFmpeg() bool { return l.opts.FFmpeg != "" }

// Status returns the current library and scan state.
func (l *Library) Status() Status {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.status
}

// rebuild recomputes the index from records. Callers must not hold mu.
func (l *Library) rebuild() {
	idx := buildIndex(l.root, l.records, l.playlists, l.opts.FFmpeg != "")
	l.mu.Lock()
	l.idx = idx
	l.randOrder = nil
	l.status.Tracks = len(idx.tracks)
	l.status.Skipped = idx.skipped
	l.status.Generation++
	l.mu.Unlock()
}

func (l *Library) index() *index {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.idx
}

// rootKey is a short stable name for per-folder cache and stats files.
func rootKey(abs string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		abs = strings.ToLower(abs)
	}
	h := fnv.New64a()
	h.Write([]byte(abs))
	return fmt.Sprintf("%016x", h.Sum64())
}

// --- browse API (mirrors subsonic.Client) ---

// Artists returns every album artist, alphabetically.
func (l *Library) Artists(context.Context) ([]subsonic.Artist, error) {
	idx := l.index()
	out := make([]subsonic.Artist, len(idx.artists))
	for i, a := range idx.artists {
		out[i] = l.artistOut(a)
	}
	return out, nil
}

// Artist returns an artist with their albums.
func (l *Library) Artist(_ context.Context, id string) (*subsonic.Artist, error) {
	a, ok := l.index().artistByID[id]
	if !ok {
		return nil, fmt.Errorf("artist not found")
	}
	out := l.artistOut(a)
	for _, al := range a.albums {
		out.Album = append(out.Album, l.albumOut(al))
	}
	return &out, nil
}

// Album returns an album with its tracks.
func (l *Library) Album(_ context.Context, id string) (*subsonic.Album, error) {
	al, ok := l.index().albumByID[id]
	if !ok {
		return nil, fmt.Errorf("album not found")
	}
	out := l.albumOut(al)
	out.Song = l.songsOut(al.tracks)
	return &out, nil
}

// AlbumList returns a page of albums in the given order.
func (l *Library) AlbumList(_ context.Context, t subsonic.AlbumListType, size, offset int) ([]subsonic.Album, error) {
	idx := l.index()
	var list []*album
	switch t {
	case subsonic.AlbumsRandom:
		l.mu.Lock()
		if offset == 0 || l.randOrder == nil {
			l.randOrder = append([]*album(nil), idx.albums...)
			rand.Shuffle(len(l.randOrder), func(i, j int) { l.randOrder[i], l.randOrder[j] = l.randOrder[j], l.randOrder[i] })
		}
		list = l.randOrder
		l.mu.Unlock()
	case subsonic.AlbumsAlphabetical:
		list = append([]*album(nil), idx.albums...)
		sort.SliceStable(list, func(i, j int) bool { return sortKey(list[i].name) < sortKey(list[j].name) })
	case subsonic.AlbumsNewest:
		list = append([]*album(nil), idx.albums...)
		sort.SliceStable(list, func(i, j int) bool { return list[i].added > list[j].added })
	case subsonic.AlbumsRecent, subsonic.AlbumsFrequent:
		type ranked struct {
			al    *album
			last  time.Time
			count int
		}
		var rs []ranked
		for _, al := range idx.albums {
			r := ranked{al: al}
			for _, t := range al.tracks {
				p := l.stats.plays(t.song.ID)
				r.count += p.Count
				if p.Last.After(r.last) {
					r.last = p.Last
				}
			}
			if r.count > 0 {
				rs = append(rs, r)
			}
		}
		sort.SliceStable(rs, func(i, j int) bool {
			if t == subsonic.AlbumsRecent {
				return rs[i].last.After(rs[j].last)
			}
			return rs[i].count > rs[j].count
		})
		for _, r := range rs {
			list = append(list, r.al)
		}
	case subsonic.AlbumsStarred:
		for _, al := range idx.albums {
			if l.stats.starred(al.id) != "" {
				list = append(list, al)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported album list %q", t)
	}

	if offset >= len(list) {
		return nil, nil
	}
	list = list[offset:min(len(list), offset+size)]
	out := make([]subsonic.Album, len(list))
	for i, al := range list {
		out[i] = l.albumOut(al)
	}
	return out, nil
}

// Playlists returns the .m3u/.m3u8 playlists found in the folder.
func (l *Library) Playlists(context.Context) ([]subsonic.Playlist, error) {
	idx := l.index()
	out := make([]subsonic.Playlist, len(idx.playlists))
	for i, p := range idx.playlists {
		out[i] = playlistOut(p)
	}
	return out, nil
}

// Playlist returns a playlist with its tracks.
func (l *Library) Playlist(_ context.Context, id string) (*subsonic.Playlist, error) {
	p, ok := l.index().playlistByID[id]
	if !ok {
		return nil, fmt.Errorf("playlist not found")
	}
	out := playlistOut(p)
	out.Entry = l.songsOut(p.tracks)
	return &out, nil
}

// Search matches every word of query against artists, albums and songs.
func (l *Library) Search(_ context.Context, query string) (*subsonic.SearchResult, error) {
	words := strings.Fields(strings.ToLower(query))
	match := func(hay string) bool {
		hay = strings.ToLower(hay)
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return false
			}
		}
		return len(words) > 0
	}
	idx := l.index()
	res := &subsonic.SearchResult{}
	for _, a := range idx.artists {
		if len(res.Artist) < 20 && match(a.name) {
			res.Artist = append(res.Artist, l.artistOut(a))
		}
	}
	for _, al := range idx.albums {
		if len(res.Album) < 40 && match(al.name+" "+al.artistName) {
			res.Album = append(res.Album, l.albumOut(al))
		}
	}
	for _, t := range idx.tracks {
		if len(res.Song) >= 100 {
			break
		}
		if match(t.song.Title + " " + t.song.Artist + " " + t.song.Album) {
			res.Song = append(res.Song, l.songOut(t))
		}
	}
	return res, nil
}

// RandomSongs returns up to n random tracks.
func (l *Library) RandomSongs(_ context.Context, n int) ([]subsonic.Song, error) {
	tracks := l.index().tracks
	picks := rand.Perm(len(tracks))
	if len(picks) > n {
		picks = picks[:n]
	}
	out := make([]subsonic.Song, len(picks))
	for i, p := range picks {
		out[i] = l.songOut(tracks[p])
	}
	return out, nil
}

// Star marks a song, album or artist as a favourite.
func (l *Library) Star(_ context.Context, id string) error { return l.stats.setStar(id, true) }

// Unstar removes a favourite.
func (l *Library) Unstar(_ context.Context, id string) error { return l.stats.setStar(id, false) }

// Scrobble records a play; "now playing" notifications are ignored.
func (l *Library) Scrobble(_ context.Context, id string, submission bool) error {
	if !submission {
		return nil
	}
	return l.stats.addPlay(id, time.Now())
}

// Open opens a track for the player: the file itself when pulse can decode
// it, otherwise ffmpeg's PCM output.
func (l *Library) Open(ctx context.Context, m player.Media) (player.Source, error) {
	t, ok := l.index().byID[m.ID]
	if !ok {
		return player.Source{}, errors.New("track is no longer in the library (rescan with u)")
	}
	if t.native {
		f, err := os.Open(t.abs)
		if err != nil {
			return player.Source{}, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return player.Source{}, err
		}
		return player.Source{Body: f, Size: fi.Size(), Format: nativeFormat(t.song.Suffix)}, nil
	}
	if l.opts.FFmpeg == "" {
		return player.Source{}, fmt.Errorf("%s files need ffmpeg, which wasn't found", t.song.Suffix)
	}
	return openFFmpeg(ctx, l.opts.FFmpeg, t.abs, time.Duration(t.song.Duration)*time.Second)
}

// --- conversions to the shared model ---

func (l *Library) artistOut(a *artist) subsonic.Artist {
	return subsonic.Artist{ID: a.id, Name: a.name, AlbumCount: len(a.albums), Starred: l.stats.starred(a.id)}
}

func (l *Library) albumOut(al *album) subsonic.Album {
	return subsonic.Album{
		ID:        al.id,
		Name:      al.name,
		Artist:    al.artistName,
		ArtistID:  al.artistID,
		SongCount: len(al.tracks),
		Duration:  al.duration,
		Year:      al.year,
		Genre:     al.genre,
		Starred:   l.stats.starred(al.id),
	}
}

func (l *Library) songOut(t *track) subsonic.Song {
	s := t.song
	s.Starred = l.stats.starred(s.ID)
	return s
}

func (l *Library) songsOut(ts []*track) []subsonic.Song {
	out := make([]subsonic.Song, len(ts))
	for i, t := range ts {
		out[i] = l.songOut(t)
	}
	return out
}

func playlistOut(p *playlist) subsonic.Playlist {
	dur := 0
	for _, t := range p.tracks {
		dur += t.song.Duration
	}
	return subsonic.Playlist{ID: p.id, Name: p.name, Owner: "local", SongCount: len(p.tracks), Duration: dur}
}

// --- scan cache ---

const cacheVersion = 1

type cacheFile struct {
	Version   int      `json:"version"`
	Root      string   `json:"root"`
	Records   []record `json:"records"`
	Playlists []string `json:"playlists"`
}

func loadCache(path string) (*cacheFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c cacheFile
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Version != cacheVersion {
		return nil, errors.New("stale cache version")
	}
	return &c, nil
}

func (l *Library) saveCache() error {
	l.mu.RLock()
	c := cacheFile{Version: cacheVersion, Root: l.root, Records: l.records, Playlists: l.playlists}
	b, err := json.Marshal(c)
	l.mu.RUnlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(l.cachePath, b)
}

// writeFileAtomic writes via a temp file and rename so a crash never leaves
// a truncated file behind.
func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
