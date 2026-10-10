package subsonic

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testPassword = "sesame"

// fakeServer checks token auth and serves canned responses.
func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sum := md5.Sum([]byte(testPassword + q.Get("s")))
		if q.Get("u") != "alice" || q.Get("t") != hex.EncodeToString(sum[:]) || q.Get("p") != "" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"subsonic-response":{"status":"failed","version":"1.16.1","error":{"code":40,"message":"Wrong username or password"}}}`)
			return
		}
		switch r.URL.Path {
		case "/rest/ping":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"subsonic-response":{"status":"ok","version":"1.16.1","type":"navidrome","serverVersion":"0.53.3","openSubsonic":true}}`)
		case "/rest/getArtists":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"subsonic-response":{"status":"ok","artists":{"index":[
				{"name":"A","artist":[{"id":"ar1","name":"ABBA","albumCount":3}]},
				{"name":"R","artist":[{"id":"ar2","name":"Radiohead","albumCount":9,"starred":"2024-01-01T00:00:00Z"}]}]}}}`)
		case "/rest/getAlbum":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"subsonic-response":{"status":"ok","album":{"id":"al1","name":"OK Computer","artist":"Radiohead","year":1997,"songCount":2,
				"song":[{"id":"s1","title":"Airbag","track":1,"duration":284,"suffix":"flac"},{"id":"s2","title":"Paranoid Android","track":2,"duration":383,"suffix":"flac"}]}}}`)
		case "/rest/stream":
			if q.Get("id") == "missing" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"subsonic-response":{"status":"failed","error":{"code":70,"message":"not found"}}}`)
				return
			}
			if q.Get("format") != "mp3" || q.Get("maxBitRate") != "192" || q.Get("estimateContentLength") != "true" {
				t.Errorf("unexpected stream params: %v", q)
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			io.WriteString(w, "ID3fakeaudio")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPingReportsServer(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := New(srv.URL+"/", "alice", testPassword, Options{})
	got, err := c.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "navidrome 0.53.3" {
		t.Fatalf("Ping = %q", got)
	}
}

func TestWrongPasswordReturnsAPIError(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := New(srv.URL, "alice", "nope", Options{})
	_, err := c.Ping(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 40 {
		t.Fatalf("err = %v, want APIError 40", err)
	}
}

func TestArtistsFlattensIndexes(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := New(srv.URL, "alice", testPassword, Options{})
	artists, err := c.Artists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 2 || artists[1].Name != "Radiohead" || artists[1].Starred == "" {
		t.Fatalf("artists = %+v", artists)
	}
}

func TestAlbumSongs(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := New(srv.URL, "alice", testPassword, Options{})
	al, err := c.Album(context.Background(), "al1")
	if err != nil {
		t.Fatal(err)
	}
	if al.Name != "OK Computer" || len(al.Song) != 2 || al.Song[1].Duration != 383 {
		t.Fatalf("album = %+v", al)
	}
}

func TestStream(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()
	c := New(srv.URL, "alice", testPassword, Options{})

	resp, err := c.Stream(context.Background(), "s1", StreamOptions{Format: "mp3", MaxBitRate: 192})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ID3fakeaudio" {
		t.Fatalf("body = %q", body)
	}

	_, err = c.Stream(context.Background(), "missing", StreamOptions{Format: "mp3", MaxBitRate: 192})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 70 {
		t.Fatalf("err = %v, want APIError 70", err)
	}
}
