// Package subsonic is a minimal client for the Subsonic / OpenSubsonic API as
// implemented by Navidrome.
package subsonic

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiVersion = "1.16.1"
	clientName = "pulse"
)

// Client talks to a single Subsonic-compatible server.
type Client struct {
	baseURL  string
	username string
	password string

	api    *http.Client // short timeout, for JSON endpoints
	stream *http.Client // no overall timeout, for audio streams
}

// Options configures a Client.
type Options struct {
	InsecureSkipVerify bool
}

// New creates a client for the server at baseURL (e.g. https://music.example.com).
func New(baseURL, username, password string, opts Options) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Streams have no overall timeout, but a server that never starts
	// answering should not leave the player buffering forever.
	transport.ResponseHeaderTimeout = 30 * time.Second
	if opts.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed certs
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		api:      &http.Client{Transport: transport, Timeout: 30 * time.Second},
		stream:   &http.Client{Transport: transport},
	}
}

// BaseURL returns the server URL this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// Username returns the user this client authenticates as.
func (c *Client) Username() string { return c.username }

// authParams builds the token-auth query parameters. A fresh salt is used for
// every request, as recommended by the Subsonic API.
func (c *Client) authParams() url.Values {
	saltBytes := make([]byte, 8)
	_, _ = rand.Read(saltBytes)
	salt := hex.EncodeToString(saltBytes)
	sum := md5.Sum([]byte(c.password + salt))

	q := url.Values{}
	q.Set("u", c.username)
	q.Set("t", hex.EncodeToString(sum[:]))
	q.Set("s", salt)
	q.Set("v", apiVersion)
	q.Set("c", clientName)
	return q
}

func (c *Client) endpoint(name string, q url.Values) string {
	params := c.authParams()
	for k, vs := range q {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	return c.baseURL + "/rest/" + name + "?" + params.Encode()
}

func (c *Client) call(ctx context.Context, name string, q url.Values) (*response, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("f", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(name, q), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s: HTTP %d: %s", name, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var env struct {
		Response response `json:"subsonic-response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("%s: decoding response: %w", name, err)
	}
	if env.Response.Status != "ok" {
		if env.Response.Error != nil {
			return nil, env.Response.Error
		}
		return nil, fmt.Errorf("%s: server returned status %q", name, env.Response.Status)
	}
	return &env.Response, nil
}

// Ping checks connectivity and credentials. It returns the server type and
// version when the server reports them (OpenSubsonic extension).
func (c *Client) Ping(ctx context.Context) (string, error) {
	r, err := c.call(ctx, "ping", nil)
	if err != nil {
		return "", err
	}
	server := r.Type
	if server == "" {
		server = "subsonic"
	}
	if r.ServerVersion != "" {
		server += " " + r.ServerVersion
	}
	return server, nil
}

// Artists returns every artist in the library, in server index order.
func (c *Client) Artists(ctx context.Context) ([]Artist, error) {
	r, err := c.call(ctx, "getArtists", nil)
	if err != nil {
		return nil, err
	}
	var out []Artist
	if r.Artists != nil {
		for _, idx := range r.Artists.Index {
			out = append(out, idx.Artist...)
		}
	}
	return out, nil
}

// Artist returns an artist together with its albums.
func (c *Client) Artist(ctx context.Context, id string) (*Artist, error) {
	r, err := c.call(ctx, "getArtist", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Artist == nil {
		return nil, fmt.Errorf("getArtist: artist %s not found", id)
	}
	return r.Artist, nil
}

// Album returns an album together with its songs.
func (c *Client) Album(ctx context.Context, id string) (*Album, error) {
	r, err := c.call(ctx, "getAlbum", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Album == nil {
		return nil, fmt.Errorf("getAlbum: album %s not found", id)
	}
	return r.Album, nil
}

// AlbumListType is the "type" parameter of getAlbumList2.
type AlbumListType string

const (
	AlbumsNewest       AlbumListType = "newest"
	AlbumsRecent       AlbumListType = "recent"
	AlbumsFrequent     AlbumListType = "frequent"
	AlbumsRandom       AlbumListType = "random"
	AlbumsAlphabetical AlbumListType = "alphabeticalByName"
	AlbumsStarred      AlbumListType = "starred"
)

// AlbumList returns a page of albums ordered according to listType.
func (c *Client) AlbumList(ctx context.Context, listType AlbumListType, size, offset int) ([]Album, error) {
	q := url.Values{
		"type":   {string(listType)},
		"size":   {strconv.Itoa(size)},
		"offset": {strconv.Itoa(offset)},
	}
	r, err := c.call(ctx, "getAlbumList2", q)
	if err != nil {
		return nil, err
	}
	if r.AlbumList2 == nil {
		return nil, nil
	}
	return r.AlbumList2.Album, nil
}

// Playlists returns the playlists visible to the user.
func (c *Client) Playlists(ctx context.Context) ([]Playlist, error) {
	r, err := c.call(ctx, "getPlaylists", nil)
	if err != nil {
		return nil, err
	}
	if r.Playlists == nil {
		return nil, nil
	}
	return r.Playlists.Playlist, nil
}

// Playlist returns a playlist together with its entries.
func (c *Client) Playlist(ctx context.Context, id string) (*Playlist, error) {
	r, err := c.call(ctx, "getPlaylist", url.Values{"id": {id}})
	if err != nil {
		return nil, err
	}
	if r.Playlist == nil {
		return nil, fmt.Errorf("getPlaylist: playlist %s not found", id)
	}
	return r.Playlist, nil
}

// Search runs a search3 query across artists, albums and songs.
func (c *Client) Search(ctx context.Context, query string) (*SearchResult, error) {
	q := url.Values{
		"query":       {query},
		"artistCount": {"20"},
		"albumCount":  {"40"},
		"songCount":   {"100"},
	}
	r, err := c.call(ctx, "search3", q)
	if err != nil {
		return nil, err
	}
	if r.SearchResult3 == nil {
		return &SearchResult{}, nil
	}
	return r.SearchResult3, nil
}

// RandomSongs returns up to size random songs.
func (c *Client) RandomSongs(ctx context.Context, size int) ([]Song, error) {
	r, err := c.call(ctx, "getRandomSongs", url.Values{"size": {strconv.Itoa(size)}})
	if err != nil {
		return nil, err
	}
	if r.RandomSongs == nil {
		return nil, nil
	}
	return r.RandomSongs.Song, nil
}

// Starred returns the user's starred artists, albums and songs.
func (c *Client) Starred(ctx context.Context) (*SearchResult, error) {
	r, err := c.call(ctx, "getStarred2", nil)
	if err != nil {
		return nil, err
	}
	if r.Starred2 == nil {
		return &SearchResult{}, nil
	}
	return r.Starred2, nil
}

// Star marks a song, album or artist as a favourite; Unstar reverses it.
func (c *Client) Star(ctx context.Context, id string) error {
	_, err := c.call(ctx, "star", url.Values{"id": {id}})
	return err
}

// Unstar removes a song, album or artist from favourites.
func (c *Client) Unstar(ctx context.Context, id string) error {
	_, err := c.call(ctx, "unstar", url.Values{"id": {id}})
	return err
}

// Scrobble reports playback. With submission=false the server only updates
// "now playing"; with submission=true it records a play.
func (c *Client) Scrobble(ctx context.Context, id string, submission bool) error {
	q := url.Values{
		"id":         {id},
		"submission": {strconv.FormatBool(submission)},
	}
	if submission {
		q.Set("time", strconv.FormatInt(time.Now().UnixMilli(), 10))
	}
	_, err := c.call(ctx, "scrobble", q)
	return err
}

// StreamOptions controls server-side transcoding of a stream.
type StreamOptions struct {
	// Format is the target format ("raw" for the original file, "mp3", ...).
	Format string
	// MaxBitRate in kbps; 0 means no limit.
	MaxBitRate int
}

// Stream opens the audio stream for a song. The caller must close the body.
func (c *Client) Stream(ctx context.Context, id string, opts StreamOptions) (*http.Response, error) {
	q := url.Values{"id": {id}}
	if opts.Format != "" {
		q.Set("format", opts.Format)
	}
	if opts.MaxBitRate > 0 {
		q.Set("maxBitRate", strconv.Itoa(opts.MaxBitRate))
	}
	if opts.Format != "" && opts.Format != "raw" {
		// Ask for a Content-Length estimate so progress can be shown for
		// transcoded streams too.
		q.Set("estimateContentLength", "true")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("stream", q), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("stream: HTTP %d", resp.StatusCode)
	}
	// On failure the server answers 200 with a JSON/XML error envelope.
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/xml") || strings.HasPrefix(ct, "application/xml") {
		defer resp.Body.Close()
		var env struct {
			Response response `json:"subsonic-response"`
		}
		if json.NewDecoder(resp.Body).Decode(&env) == nil && env.Response.Error != nil {
			return nil, env.Response.Error
		}
		return nil, fmt.Errorf("stream: unexpected content type %q", ct)
	}
	return resp, nil
}
