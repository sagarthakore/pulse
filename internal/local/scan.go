package local

import (
	"context"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.senan.xyz/taglib"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

// audioExts are the file types considered music. mp3/flac/ogg/wav play
// natively; the rest need ffmpeg.
var audioExts = map[string]bool{
	"mp3": true, "flac": true, "ogg": true, "oga": true, "wav": true, "wave": true,
	"m4a": true, "m4b": true, "mp4": true, "aac": true, "alac": true, "opus": true,
	"wma": true, "ape": true, "wv": true, "aiff": true, "aif": true, "aifc": true,
	"mpc": true, "tta": true, "dsf": true, "webm": true, "mka": true,
}

var playlistExts = map[string]bool{"m3u": true, "m3u8": true}

// utf8BOM may start .m3u8 files written by Windows tools.
const utf8BOM = "\xef\xbb\xbf"

const (
	unknownArtist  = "Unknown Artist"
	unknownAlbum   = "Unknown Album"
	variousArtists = "Various Artists"
)

// record is everything pulse remembers about one file between scans.
type record struct {
	Path        string `json:"path"` // slash-separated, relative to the root
	Size        int64  `json:"size"`
	ModTime     int64  `json:"mtime"` // unix nanoseconds
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	AlbumArtist string `json:"albumArtist,omitempty"`
	Album       string `json:"album,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Track       int    `json:"track,omitempty"`
	Disc        int    `json:"disc,omitempty"`
	Year        int    `json:"year,omitempty"`
	DurationMS  int64  `json:"durationMs,omitempty"`
	BitRate     int    `json:"bitRate,omitempty"`
	// Native: decodable without ffmpeg.
	Native bool `json:"native,omitempty"`
	// Bad: tags/properties unreadable; kept so it isn't re-read every scan.
	Bad bool `json:"bad,omitempty"`
}

type fileEntry struct {
	rel, abs string
	size     int64
	mtime    int64
}

// Scan walks the folder, reads tags for new or changed files (reusing the
// cache for the rest) and swaps in the new index. It is safe to browse the
// library while a scan runs.
func (l *Library) Scan(ctx context.Context) error {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()

	var done, total atomic.Int64
	l.setScan(func(s *Status) { s.Scanning, s.Done, s.Total, s.Err = true, 0, 0, nil })
	stopProgress := make(chan struct{})
	go func() { // publish progress a few times a second
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-t.C:
				l.setScan(func(s *Status) { s.Done, s.Total = int(done.Load()), int(total.Load()) })
			}
		}
	}()

	err := l.scan(ctx, &done, &total)
	close(stopProgress)
	l.setScan(func(s *Status) {
		s.Scanning, s.Done, s.Total, s.Err = false, int(done.Load()), int(total.Load()), err
	})
	return err
}

func (l *Library) setScan(f func(*Status)) {
	l.mu.Lock()
	f(&l.status)
	l.mu.Unlock()
}

func (l *Library) scan(ctx context.Context, done, total *atomic.Int64) error {
	var files []fileEntry
	var playlists []string
	err := filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == l.root {
				return err
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir // unreadable folder: skip it, keep going
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != l.root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := extOf(name)
		rel, rerr := filepath.Rel(l.root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case playlistExts[ext]:
			playlists = append(playlists, rel)
		case audioExts[ext]:
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			files = append(files, fileEntry{rel: rel, abs: p, size: info.Size(), mtime: info.ModTime().UnixNano()})
			total.Add(1)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning %s: %w", l.root, err)
	}

	l.mu.RLock()
	old := make(map[string]record, len(l.records))
	for _, r := range l.records {
		old[r.Path] = r
	}
	l.mu.RUnlock()

	records := make([]record, len(files))
	var todo []int
	for i, f := range files {
		if r, ok := old[f.rel]; ok && r.Size == f.size && r.ModTime == f.mtime {
			records[i] = r
			done.Add(1)
		} else {
			todo = append(todo, i)
		}
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(runtime.NumCPU(), 8); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				records[i] = readRecordFn(files[i])
				done.Add(1)
			}
		}()
	}
	for _, i := range todo {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	sort.Strings(playlists)
	l.mu.Lock()
	l.records, l.playlists = records, playlists
	l.mu.Unlock()
	l.rebuild()
	return l.saveCache()
}

// readRecordFn is swapped in tests to count tag reads.
var readRecordFn = readRecord

// readRecord reads one file's tags and audio properties.
func readRecord(f fileEntry) record {
	r := record{Path: f.rel, Size: f.size, ModTime: f.mtime}
	props, err := taglib.ReadProperties(f.abs)
	if err != nil || props.Length <= 0 {
		r.Bad = true
		return r
	}
	tags, _ := taglib.ReadTags(f.abs)
	first := func(keys ...string) string {
		for _, k := range keys {
			for _, v := range tags[k] {
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			}
		}
		return ""
	}
	r.Title = first(taglib.Title)
	r.Artist = first(taglib.Artist)
	r.AlbumArtist = first(taglib.AlbumArtist)
	r.Album = first(taglib.Album)
	r.Genre = first(taglib.Genre)
	r.Track = leadingInt(first(taglib.TrackNumber))
	r.Disc = leadingInt(first(taglib.DiscNumber))
	r.Year = leadingInt(first(taglib.Date, taglib.OriginalDate))
	if r.Year < 1000 || r.Year > 9999 {
		r.Year = 0
	}
	r.DurationMS = props.Length.Milliseconds()
	r.BitRate = int(props.BitRate)
	r.Native = isNative(extOf(f.rel), props)
	return r
}

// isNative reports whether pulse's own decoders handle the file.
func isNative(ext string, p taglib.Properties) bool {
	codec := strings.ToLower(p.InnerCodec)
	switch ext {
	case "mp3":
		return true
	case "flac":
		return true
	case "ogg", "oga":
		return codec == "vorbis"
	case "wav", "wave":
		return (codec == "" || codec == "pcm") && (p.BitDepth == 8 || p.BitDepth == 16 || p.BitDepth == 24)
	}
	return false
}

// nativeFormat maps a native file suffix to the player's format name.
func nativeFormat(suffix string) string {
	switch suffix {
	case "oga":
		return "ogg"
	case "wave":
		return "wav"
	}
	return suffix
}

func extOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
}

// leadingInt parses the number at the start of s ("3/12" -> 3, "1997-05-01" -> 1997).
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// sortKey orders names case-insensitively, ignoring a leading "The ".
func sortKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimPrefix(s, "the ")
}

// --- index ---

type track struct {
	song   subsonic.Song
	abs    string
	native bool
	disc   int
	path   string
}

type album struct {
	id, name, artistName, artistID, genre string
	year, duration                        int
	added                                 int64
	tracks                                []*track
}

type artist struct {
	id, name string
	albums   []*album
}

type playlist struct {
	id, name string
	tracks   []*track
}

type index struct {
	tracks       []*track
	byID         map[string]*track
	albums       []*album
	albumByID    map[string]*album
	artists      []*artist
	artistByID   map[string]*artist
	playlists    []*playlist
	playlistByID map[string]*playlist
	skipped      int
}

func hashID(prefix string, parts ...string) string {
	h := fnvString(strings.ToLower(strings.Join(parts, "\x00")))
	return prefix + h
}

func fnvString(s string) string {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

// buildIndex groups records into artists, albums and tracks. Missing tags
// fall back to the file and folder names; untagged compilations (one folder,
// one album name, several artists) are filed under "Various Artists".
func buildIndex(root string, records []record, playlists []string, ffmpeg bool) *index {
	idx := &index{
		byID:         map[string]*track{},
		albumByID:    map[string]*album{},
		artistByID:   map[string]*artist{},
		playlistByID: map[string]*playlist{},
	}

	albumName := func(r record) string {
		if r.Album != "" {
			return r.Album
		}
		dir := path.Dir(r.Path)
		if dir == "." {
			return unknownAlbum
		}
		return path.Base(dir)
	}
	trackArtist := func(r record) string {
		if r.Artist != "" {
			return r.Artist
		}
		if r.AlbumArtist != "" {
			return r.AlbumArtist
		}
		return unknownArtist
	}

	// Album artists for files without an ALBUMARTIST tag.
	folderArtists := map[string]map[string]bool{}
	folderKey := func(r record) string { return path.Dir(r.Path) + "\x00" + strings.ToLower(albumName(r)) }
	for _, r := range records {
		if r.Bad || r.AlbumArtist != "" {
			continue
		}
		k := folderKey(r)
		if folderArtists[k] == nil {
			folderArtists[k] = map[string]bool{}
		}
		folderArtists[k][strings.ToLower(trackArtist(r))] = true
	}

	for _, r := range records {
		if r.Bad {
			continue
		}
		if !r.Native && !ffmpeg {
			idx.skipped++
			continue
		}
		aName := albumName(r)
		albumArtist := r.AlbumArtist
		if albumArtist == "" {
			if len(folderArtists[folderKey(r)]) > 1 {
				albumArtist = variousArtists
			} else {
				albumArtist = trackArtist(r)
			}
		}

		ar := idx.artistByID[hashID("r:", albumArtist)]
		if ar == nil {
			ar = &artist{id: hashID("r:", albumArtist), name: albumArtist}
			idx.artistByID[ar.id] = ar
			idx.artists = append(idx.artists, ar)
		}
		al := idx.albumByID[hashID("a:", albumArtist, aName)]
		if al == nil {
			al = &album{id: hashID("a:", albumArtist, aName), name: aName, artistName: ar.name, artistID: ar.id}
			idx.albumByID[al.id] = al
			idx.albums = append(idx.albums, al)
			ar.albums = append(ar.albums, al)
		}

		title := r.Title
		if title == "" {
			base := path.Base(r.Path)
			title = strings.TrimSuffix(base, path.Ext(base))
		}
		ext := extOf(r.Path)
		t := &track{
			abs:    filepath.Join(root, filepath.FromSlash(r.Path)),
			native: r.Native,
			disc:   r.Disc,
			path:   r.Path,
			song: subsonic.Song{
				ID:          "t:" + r.Path,
				Title:       title,
				Album:       aName,
				AlbumID:     al.id,
				Artist:      trackArtist(r),
				ArtistID:    ar.id,
				Track:       r.Track,
				DiscNumber:  r.Disc,
				Year:        r.Year,
				Duration:    int((r.DurationMS + 500) / 1000),
				BitRate:     r.BitRate,
				Suffix:      ext,
				ContentType: mime.TypeByExtension("." + ext),
				Size:        r.Size,
			},
		}
		idx.tracks = append(idx.tracks, t)
		idx.byID[t.song.ID] = t
		al.tracks = append(al.tracks, t)
		al.duration += t.song.Duration
		if r.Year > 0 && (al.year == 0 || r.Year < al.year) {
			al.year = r.Year
		}
		if al.genre == "" {
			al.genre = r.Genre
		}
		al.added = max(al.added, r.ModTime)
	}

	for _, al := range idx.albums {
		sort.SliceStable(al.tracks, func(i, j int) bool {
			a, b := al.tracks[i], al.tracks[j]
			if a.disc != b.disc {
				return a.disc < b.disc
			}
			if a.song.Track != b.song.Track {
				return a.song.Track < b.song.Track
			}
			return a.path < b.path
		})
	}
	for _, ar := range idx.artists {
		sort.SliceStable(ar.albums, func(i, j int) bool {
			a, b := ar.albums[i], ar.albums[j]
			if a.year != b.year {
				return a.year < b.year
			}
			return sortKey(a.name) < sortKey(b.name)
		})
	}
	sort.SliceStable(idx.artists, func(i, j int) bool { return sortKey(idx.artists[i].name) < sortKey(idx.artists[j].name) })
	sort.SliceStable(idx.albums, func(i, j int) bool {
		a, b := idx.albums[i], idx.albums[j]
		if ka, kb := sortKey(a.artistName), sortKey(b.artistName); ka != kb {
			return ka < kb
		}
		return a.year < b.year
	})
	// Tracks in library order (by artist, album, disc, track) for search.
	idx.tracks = idx.tracks[:0]
	for _, al := range idx.albums {
		idx.tracks = append(idx.tracks, al.tracks...)
	}

	idx.playlists = loadPlaylists(root, playlists, idx)
	for _, p := range idx.playlists {
		idx.playlistByID[p.id] = p
	}
	return idx
}

// --- playlists ---

// loadPlaylists reads .m3u/.m3u8 files and resolves their entries to tracks
// in the library; entries that aren't in the library are dropped.
func loadPlaylists(root string, rels []string, idx *index) []*playlist {
	byPath := make(map[string]*track, len(idx.tracks))
	for _, t := range idx.tracks {
		byPath[normPath(t.abs)] = t
	}
	var out []*playlist
	for _, rel := range rels {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		base := path.Base(rel)
		p := &playlist{id: "p:" + rel, name: strings.TrimSuffix(base, path.Ext(base))}
		dir := filepath.Dir(abs)
		for _, line := range strings.Split(strings.TrimPrefix(string(b), utf8BOM), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "file://")
			entry := filepath.FromSlash(line)
			if !filepath.IsAbs(entry) {
				entry = filepath.Join(dir, entry)
			}
			if t, ok := byPath[normPath(entry)]; ok {
				p.tracks = append(p.tracks, t)
			}
		}
		out = append(out, p)
	}
	return out
}

// normPath normalises a path for comparison (case-insensitive where the
// filesystem usually is).
func normPath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}
