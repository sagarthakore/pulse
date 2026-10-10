package ui

import (
	"context"
	"errors"

	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
)

// fakeLib is an in-memory Library for UI tests.
type fakeLib struct {
	albums []subsonic.Album
	songs  map[string][]subsonic.Song // album id -> songs
}

func (f *fakeLib) Artists(context.Context) ([]subsonic.Artist, error) { return nil, nil }
func (f *fakeLib) Artist(context.Context, string) (*subsonic.Artist, error) {
	return nil, errors.New("no artists")
}
func (f *fakeLib) Album(_ context.Context, id string) (*subsonic.Album, error) {
	for _, a := range f.albums {
		if a.ID == id {
			a.Song = f.songs[id]
			return &a, nil
		}
	}
	return nil, errors.New("album not found")
}
func (f *fakeLib) AlbumList(context.Context, subsonic.AlbumListType, int, int) ([]subsonic.Album, error) {
	return f.albums, nil
}
func (f *fakeLib) Playlists(context.Context) ([]subsonic.Playlist, error) { return nil, nil }
func (f *fakeLib) Playlist(context.Context, string) (*subsonic.Playlist, error) {
	return nil, errors.New("no playlists")
}
func (f *fakeLib) Search(context.Context, string) (*subsonic.SearchResult, error) {
	return &subsonic.SearchResult{}, nil
}
func (f *fakeLib) RandomSongs(context.Context, int) ([]subsonic.Song, error) { return nil, nil }
func (f *fakeLib) Star(context.Context, string) error                        { return nil }
func (f *fakeLib) Unstar(context.Context, string) error                      { return nil }
func (f *fakeLib) Scrobble(context.Context, string, bool) error              { return nil }
func (f *fakeLib) Open(context.Context, player.Media) (player.Source, error) {
	return player.Source{}, errors.New("fake library can't play")
}

// newTestModel builds a model with a fake Navidrome library and no engine.
func newTestModel(opts Options) *Model {
	if opts.Navidrome == nil {
		opts.Navidrome = &fakeLib{}
	}
	if opts.NavidromeLabel == "" {
		opts.NavidromeLabel = "alice @ music.local:4533 · navidrome 0.53.3"
	}
	return New(nil, opts)
}
