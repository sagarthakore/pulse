package local

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.senan.xyz/taglib"

	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
)

var ctx = context.Background()

// writeWAV writes seconds of 16-bit stereo silence-free PCM at 44.1 kHz.
func writeWAV(t *testing.T, path string, seconds float64) {
	t.Helper()
	n := int(seconds * 44100)
	var b bytes.Buffer
	data := n * 4
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint32(44100))
	binary.Write(&b, binary.LittleEndian, uint32(44100*4))
	binary.Write(&b, binary.LittleEndian, uint16(4))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data))
	for i := 0; i < n; i++ {
		binary.Write(&b, binary.LittleEndian, int16(8000))
		binary.Write(&b, binary.LittleEndian, int16(-8000))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tagFile(t *testing.T, path string, tags map[string]string) {
	t.Helper()
	m := map[string][]string{}
	for k, v := range tags {
		m[k] = []string{v}
	}
	if err := taglib.WriteTags(path, m, 0); err != nil {
		t.Fatalf("tagging %s: %v", path, err)
	}
}

func song(t *testing.T, root, rel string, tags map[string]string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	writeWAV(t, p, 1.5)
	if tags != nil {
		tagFile(t, p, tags)
	}
	return p
}

func openLib(t *testing.T, root, state string, ffmpeg string) *Library {
	t.Helper()
	l, err := Open(root, Options{FFmpeg: ffmpeg, CacheDir: filepath.Join(state, "cache"), DataDir: filepath.Join(state, "data")})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// fixture builds a small library covering tagged, untagged, compilation,
// hidden, non-audio and playlist cases.
func fixture(t *testing.T) (root, state string) {
	t.Helper()
	root, state = t.TempDir(), t.TempDir()
	song(t, root, "Radiohead/OK Computer/02 Paranoid Android.wav", map[string]string{
		taglib.Title: "Paranoid Android", taglib.Artist: "Radiohead", taglib.Album: "OK Computer",
		taglib.TrackNumber: "2/12", taglib.Date: "1997-05-21",
	})
	song(t, root, "Radiohead/OK Computer/01 Airbag.wav", map[string]string{
		taglib.Title: "Airbag", taglib.Artist: "Radiohead", taglib.Album: "OK Computer",
		taglib.TrackNumber: "1/12", taglib.Date: "1997",
	})
	song(t, root, "Radiohead/Kid A/01.wav", map[string]string{
		taglib.Title: "Everything in Its Right Place", taglib.Artist: "Radiohead",
		taglib.AlbumArtist: "Radiohead", taglib.Album: "Kid A", taglib.Date: "2000",
	})
	song(t, root, "Comp/Mix Tape/a.wav", map[string]string{taglib.Title: "A Song", taglib.Artist: "Artist A", taglib.Album: "Mix Tape"})
	song(t, root, "Comp/Mix Tape/b.wav", map[string]string{taglib.Title: "B Song", taglib.Artist: "Artist B", taglib.Album: "Mix Tape"})
	song(t, root, "Loose/Untitled Demo.wav", nil)
	song(t, root, ".hidden/secret.wav", map[string]string{taglib.Title: "Hidden"})
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not music"), 0o644)
	os.WriteFile(filepath.Join(root, "broken.mp3"), []byte("definitely not an mp3"), 0o644)
	os.WriteFile(filepath.Join(root, "Favorites.m3u8"), []byte(utf8BOM+"#EXTM3U\n"+
		"#EXTINF:1,Radiohead - Airbag\nRadiohead/OK Computer/01 Airbag.wav\n"+
		"Loose\\Untitled Demo.wav\n"+
		"missing/file.mp3\n"), 0o644)
	return root, state
}

func scan(t *testing.T, l *Library) {
	t.Helper()
	if err := l.Scan(ctx); err != nil {
		t.Fatal(err)
	}
}

func names[T any](xs []T, f func(T) string) string {
	var out []string
	for _, x := range xs {
		out = append(out, f(x))
	}
	return strings.Join(out, " | ")
}

func TestScanBuildsLibrary(t *testing.T) {
	root, state := fixture(t)
	l := openLib(t, root, state, "")
	scan(t, l)

	st := l.Status()
	if st.Tracks != 6 || st.Scanning || st.Err != nil || st.Total != 7 {
		t.Fatalf("status = %+v", st)
	}

	artists, _ := l.Artists(ctx)
	if got := names(artists, func(a subsonic.Artist) string { return a.Name }); got != "Radiohead | Unknown Artist | Various Artists" {
		t.Fatalf("artists = %s", got)
	}

	ar, _ := l.Artist(ctx, artists[0].ID)
	if got := names(ar.Album, func(a subsonic.Album) string { return a.Name }); got != "OK Computer | Kid A" {
		t.Fatalf("Radiohead albums (by year) = %s", got)
	}
	okc, _ := l.Album(ctx, ar.Album[0].ID)
	if got := names(okc.Song, func(s subsonic.Song) string { return s.Title }); got != "Airbag | Paranoid Android" {
		t.Fatalf("track order = %s", got)
	}
	if okc.Year != 1997 || okc.Song[1].Track != 2 || okc.Song[0].Duration != 2 && okc.Song[0].Duration != 1 {
		t.Fatalf("album = %+v", okc)
	}

	// Untagged file: title from filename, album from folder.
	unknown, _ := l.Artist(ctx, artists[1].ID)
	demo, _ := l.Album(ctx, unknown.Album[0].ID)
	if demo.Name != "Loose" || demo.Song[0].Title != "Untitled Demo" {
		t.Fatalf("untagged fallback = %+v", demo)
	}

	// Compilation without ALBUMARTIST: one album under Various Artists.
	va, _ := l.Artist(ctx, artists[2].ID)
	if len(va.Album) != 1 || va.Album[0].SongCount != 2 {
		t.Fatalf("compilation = %+v", va)
	}

	res, _ := l.Search(ctx, "radio AIRBAG")
	if len(res.Song) != 1 || res.Song[0].Title != "Airbag" || len(res.Artist) != 0 {
		t.Fatalf("search = %+v", res)
	}
	if res, _ := l.Search(ctx, "hidden"); len(res.Song) != 0 {
		t.Fatal("dot-folders must be skipped")
	}

	pls, _ := l.Playlists(ctx)
	if len(pls) != 1 || pls[0].Name != "Favorites" || pls[0].SongCount != 2 {
		t.Fatalf("playlists = %+v", pls)
	}
	pl, _ := l.Playlist(ctx, pls[0].ID)
	if got := names(pl.Entry, func(s subsonic.Song) string { return s.Title }); got != "Airbag | Untitled Demo" {
		t.Fatalf("playlist entries = %s", got)
	}

	alpha, _ := l.AlbumList(ctx, subsonic.AlbumsAlphabetical, 2, 1)
	if got := names(alpha, func(a subsonic.Album) string { return a.Name }); got != "Loose | Mix Tape" {
		t.Fatalf("alphabetical page 2 = %s", got)
	}
	if r, _ := l.RandomSongs(ctx, 4); len(r) != 4 {
		t.Fatalf("random = %d songs", len(r))
	}
}

func TestRescanOnlyReadsChangedFiles(t *testing.T) {
	root, state := fixture(t)
	var reads atomic.Int32
	orig := readRecordFn
	readRecordFn = func(f fileEntry) record { reads.Add(1); return orig(f) }
	t.Cleanup(func() { readRecordFn = orig })

	l := openLib(t, root, state, "")
	scan(t, l)
	if n := reads.Swap(0); n != 7 {
		t.Fatalf("first scan read %d files, want 7", n)
	}
	scan(t, l)
	if n := reads.Swap(0); n != 0 {
		t.Fatalf("unchanged rescan read %d files", n)
	}

	airbag := filepath.Join(root, "Radiohead", "OK Computer", "01 Airbag.wav")
	tagFile(t, airbag, map[string]string{taglib.Title: "Airbag (Remastered)", taglib.Artist: "Radiohead", taglib.Album: "OK Computer", taglib.TrackNumber: "1"})
	later := time.Now().Add(time.Minute)
	os.Chtimes(airbag, later, later)
	song(t, root, "New/fresh.wav", map[string]string{taglib.Title: "Fresh"})
	gen := l.Status().Generation

	scan(t, l)
	if n := reads.Load(); n != 2 {
		t.Fatalf("rescan read %d files, want 2 (changed + new)", n)
	}
	if l.Status().Generation <= gen {
		t.Fatal("generation didn't advance")
	}
	if res, _ := l.Search(ctx, "remastered"); len(res.Song) != 1 {
		t.Fatal("changed tags not picked up")
	}

	// A new Library on the same folder starts from the cache, no scan needed.
	l2 := openLib(t, root, state, "")
	if st := l2.Status(); st.Tracks != 7 {
		t.Fatalf("cached library has %d tracks, want 7", st.Tracks)
	}
}

func TestStarsAndPlaysPersist(t *testing.T) {
	root, state := fixture(t)
	l := openLib(t, root, state, "")
	scan(t, l)
	res, _ := l.Search(ctx, "airbag")
	airbag := res.Song[0]

	if err := l.Star(ctx, airbag.AlbumID); err != nil {
		t.Fatal(err)
	}
	l.Star(ctx, airbag.ID)
	l.Scrobble(ctx, airbag.ID, false) // now-playing only: not a play
	l.Scrobble(ctx, airbag.ID, true)
	l.Scrobble(ctx, airbag.ID, true)

	l2 := openLib(t, root, state, "") // fresh instance reads the stats file
	starred, _ := l2.AlbumList(ctx, subsonic.AlbumsStarred, 10, 0)
	if len(starred) != 1 || starred[0].Name != "OK Computer" || starred[0].Starred == "" {
		t.Fatalf("starred albums = %+v", starred)
	}
	freq, _ := l2.AlbumList(ctx, subsonic.AlbumsFrequent, 10, 0)
	recent, _ := l2.AlbumList(ctx, subsonic.AlbumsRecent, 10, 0)
	if len(freq) != 1 || len(recent) != 1 || freq[0].ID != airbag.AlbumID {
		t.Fatalf("frequent = %+v recent = %+v", freq, recent)
	}
	if s, _ := l2.Search(ctx, "airbag"); s.Song[0].Starred == "" {
		t.Fatal("song star lost")
	}
	l2.Unstar(ctx, airbag.AlbumID)
	if s, _ := l2.AlbumList(ctx, subsonic.AlbumsStarred, 10, 0); len(s) != 0 {
		t.Fatal("unstar didn't stick")
	}
}

func TestOpenNativeFile(t *testing.T) {
	root, state := fixture(t)
	l := openLib(t, root, state, "")
	scan(t, l)
	res, _ := l.Search(ctx, "airbag")
	src, err := l.Open(ctx, player.Media{ID: res.Song[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Body.Close()
	head := make([]byte, 4)
	io.ReadFull(src.Body, head)
	if src.Format != "wav" || string(head) != "RIFF" || src.Size <= 0 {
		t.Fatalf("source = %q %q %d", src.Format, head, src.Size)
	}
	if _, err := l.Open(ctx, player.Media{ID: "t:missing.wav"}); err == nil {
		t.Fatal("expected an error for an unknown track")
	}
}

func TestScanSurvivesBadFiles(t *testing.T) {
	root, state := fixture(t)
	l := openLib(t, root, state, "")
	scan(t, l)
	if res, _ := l.Search(ctx, "broken"); len(res.Song) != 0 {
		t.Fatal("unreadable file listed")
	}
}

func TestLeadingInt(t *testing.T) {
	for in, want := range map[string]int{"3/12": 3, "1997-05-21": 1997, "07": 7, "": 0, "x": 0} {
		if got := leadingInt(in); got != want {
			t.Errorf("leadingInt(%q) = %d, want %d", in, got, want)
		}
	}
}

// --- ffmpeg-dependent tests (skipped when ffmpeg isn't installed) ---

func ffmpegOrSkip(t *testing.T) string {
	t.Helper()
	p := FindFFmpeg(os.Getenv("PULSE_TEST_FFMPEG"))
	if p == "" {
		t.Skip("ffmpeg not installed")
	}
	return p
}

func convert(t *testing.T, ffmpeg, src, dst string, args ...string) {
	t.Helper()
	cmd := exec.Command(ffmpeg, append(append([]string{"-nostdin", "-loglevel", "error", "-y", "-i", src}, args...), dst)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", dst, err, out)
	}
}

func TestFormatsNeedingFFmpeg(t *testing.T) {
	ffmpeg := ffmpegOrSkip(t)
	root, state := t.TempDir(), t.TempDir()
	wav := filepath.Join(state, "src.wav")
	writeWAV(t, wav, 3)
	convert(t, ffmpeg, wav, filepath.Join(root, "aac.m4a"), "-c:a", "aac", "-metadata", "title=AAC Song")
	convert(t, ffmpeg, wav, filepath.Join(root, "opus.opus"), "-c:a", "libopus", "-metadata", "title=Opus Song")
	convert(t, ffmpeg, wav, filepath.Join(root, "opus-in-ogg.ogg"), "-c:a", "libopus", "-metadata", "title=Ogg Opus Song")
	convert(t, ffmpeg, wav, filepath.Join(root, "vorbis.ogg"), "-c:a", "libvorbis", "-metadata", "title=Vorbis Song")
	convert(t, ffmpeg, wav, filepath.Join(root, "mp3.mp3"), "-c:a", "libmp3lame", "-metadata", "title=MP3 Song")

	// Without ffmpeg: only the natively decodable files are listed.
	noFF := openLib(t, root, state, "")
	scan(t, noFF)
	if st := noFF.Status(); st.Tracks != 2 || st.Skipped != 3 {
		t.Fatalf("without ffmpeg: %+v", st)
	}

	withFF := openLib(t, root, t.TempDir(), ffmpeg)
	scan(t, withFF)
	if st := withFF.Status(); st.Tracks != 5 || st.Skipped != 0 {
		t.Fatalf("with ffmpeg: %+v", st)
	}

	res, _ := withFF.Search(ctx, "aac song")
	if len(res.Song) != 1 {
		t.Fatalf("AAC song not found: %+v", res)
	}
	src, err := withFF.Open(ctx, player.Media{ID: res.Song[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := io.ReadAll(src.Body)
	src.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if src.Format != player.FormatPCM {
		t.Fatalf("format = %q", src.Format)
	}
	// ~3 s of 44.1 kHz stereo s16 (AAC adds a little padding).
	if secs := float64(len(pcm)) / 4 / 44100; secs < 2.9 || secs > 3.2 {
		t.Fatalf("decoded %.2fs of audio, want ~3s", secs)
	}
}

func TestFFmpegErrorsSurface(t *testing.T) {
	ffmpeg := ffmpegOrSkip(t)
	bad := filepath.Join(t.TempDir(), "bad.m4a")
	os.WriteFile(bad, []byte("not audio at all"), 0o644)
	src, err := openFFmpeg(ctx, ffmpeg, bad, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Body.Close()
	if _, err := io.ReadAll(src.Body); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("err = %v, want an ffmpeg error", err)
	}
}
