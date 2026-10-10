package ui

import (
	"context"
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sagarthakore/pulse/internal/subsonic"
)

type tabID int

const (
	tabArtists tabID = iota
	tabAlbums
	tabPlaylists
	tabSearch
	numTabs
)

var tabNames = [numTabs]string{"Artists", "Albums", "Playlists", "Search"}

type pageKind int

const (
	pageArtists pageKind = iota
	pageAlbums
	pagePlaylists
	pageSearch
	pageArtist
	pageAlbum
	pagePlaylist
)

const (
	albumPageSize = 200
	apiTimeout    = 30 * time.Second
)

var albumTypes = []subsonic.AlbumListType{
	subsonic.AlbumsNewest,
	subsonic.AlbumsRecent,
	subsonic.AlbumsFrequent,
	subsonic.AlbumsRandom,
	subsonic.AlbumsAlphabetical,
	subsonic.AlbumsStarred,
}

var albumTypeNames = map[subsonic.AlbumListType]string{
	subsonic.AlbumsNewest:       "recently added",
	subsonic.AlbumsRecent:       "recently played",
	subsonic.AlbumsFrequent:     "most played",
	subsonic.AlbumsRandom:       "random",
	subsonic.AlbumsAlphabetical: "A-Z",
	subsonic.AlbumsStarred:      "starred",
}

// page is one level of the browser's navigation stack.
type page struct {
	seq       int
	kind      pageKind
	id        string // entity id, or the query for search pages
	title     string
	list      list
	loading   bool
	loaded    bool
	err       error
	albumType subsonic.AlbumListType
	exhausted bool
}

type pageLoadedMsg struct {
	seq       int
	items     []item
	err       error
	append    bool
	exhausted bool
}

func (m *Model) newPage(kind pageKind, id, title string) *page {
	m.seq++
	return &page{seq: m.seq, kind: kind, id: id, title: title}
}

func (m *Model) findPage(seq int) *page {
	for _, s := range m.sources {
		for _, stack := range s.stacks {
			for _, p := range stack {
				if p.seq == seq {
					return p
				}
			}
		}
	}
	return nil
}

func (m *Model) currentPage() *page {
	s := m.src()
	stack := s.stacks[s.tab]
	if len(stack) == 0 {
		return nil
	}
	return stack[len(stack)-1]
}

func (m *Model) pushPage(p *page) tea.Cmd {
	s := m.src()
	s.stacks[s.tab] = append(s.stacks[s.tab], p)
	return m.loadPage(s, p, 0)
}

// loadPage fetches a page's contents (or the next chunk of a paged list)
// from library s.
func (m *Model) loadPage(s *source, p *page, offset int) tea.Cmd {
	if (p.kind == pageSearch && p.id == "") || s.api == nil {
		p.loaded = true
		return nil
	}
	p.loading = true
	client := s.api
	seq, kind, id, albumType := p.seq, p.kind, p.id, p.albumType
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		msg := pageLoadedMsg{seq: seq, append: offset > 0}
		switch kind {
		case pageArtists:
			artists, err := client.Artists(ctx)
			msg.err = err
			for _, a := range artists {
				msg.items = append(msg.items, artistItem(a))
			}
		case pageAlbums:
			albums, err := client.AlbumList(ctx, albumType, albumPageSize, offset)
			msg.err = err
			msg.exhausted = len(albums) < albumPageSize
			for _, a := range albums {
				msg.items = append(msg.items, albumItem(a, true))
			}
		case pagePlaylists:
			pls, err := client.Playlists(ctx)
			msg.err = err
			for _, pl := range pls {
				msg.items = append(msg.items, playlistItem(pl))
			}
		case pageSearch:
			res, err := client.Search(ctx, id)
			msg.err = err
			if res != nil {
				msg.items = searchItems(res)
			}
		case pageArtist:
			a, err := client.Artist(ctx, id)
			msg.err = err
			if a != nil {
				albums := a.Album
				sort.SliceStable(albums, func(i, j int) bool { return albums[i].Year < albums[j].Year })
				for _, al := range albums {
					msg.items = append(msg.items, albumItem(al, false))
				}
			}
		case pageAlbum:
			al, err := client.Album(ctx, id)
			msg.err = err
			if al != nil {
				multiDisc := false
				for _, s := range al.Song {
					if s.DiscNumber > 1 {
						multiDisc = true
					}
				}
				for _, s := range al.Song {
					msg.items = append(msg.items, trackSongItem(s, multiDisc))
				}
			}
		case pagePlaylist:
			pl, err := client.Playlist(ctx, id)
			msg.err = err
			if pl != nil {
				for _, s := range pl.Entry {
					msg.items = append(msg.items, songItem(s, true))
				}
			}
		}
		return msg
	}
}

func searchItems(res *subsonic.SearchResult) []item {
	var items []item
	if len(res.Artist) > 0 {
		items = append(items, headerItem(fmt.Sprintf("Artists (%d)", len(res.Artist)), cBlue))
		for _, a := range res.Artist {
			items = append(items, artistItem(a))
		}
	}
	if len(res.Album) > 0 {
		items = append(items, headerItem(fmt.Sprintf("Albums (%d)", len(res.Album)), cMauve))
		for _, a := range res.Album {
			items = append(items, albumItem(a, true))
		}
	}
	if len(res.Song) > 0 {
		items = append(items, headerItem(fmt.Sprintf("Songs (%d)", len(res.Song)), cGreen))
		for _, s := range res.Song {
			items = append(items, songItem(s, true))
		}
	}
	return items
}

func (m *Model) handlePageLoaded(msg pageLoadedMsg) tea.Cmd {
	p := m.findPage(msg.seq)
	if p == nil {
		return nil // page was closed while loading
	}
	p.loading = false
	p.loaded = true
	p.err = msg.err
	if msg.err != nil {
		m.flashError(msg.err)
		return nil
	}
	p.exhausted = msg.exhausted
	if msg.append {
		p.list.setItems(append(p.list.items, msg.items...))
	} else {
		p.list.setItems(msg.items)
	}
	return nil
}

// maybeLoadMore fetches the next chunk of a paged album list when the cursor
// approaches the end.
func (m *Model) maybeLoadMore(p *page) tea.Cmd {
	if p.kind != pageAlbums || p.loading || p.exhausted || p.err != nil {
		return nil
	}
	if p.list.cursor < len(p.list.items)-20 {
		return nil
	}
	return m.loadPage(m.src(), p, len(p.list.items))
}

// --- fetching songs for queue actions ---

type queueAction int

const (
	actAppend queueAction = iota
	actPlayNext
	actReplace
)

type songsMsg struct {
	src    SourceKind
	action queueAction
	songs  []subsonic.Song
	start  int
	label  string
	err    error
}

// fetchSongs resolves an item (song, album, artist or playlist) of library
// src into songs for its queue.
func (m *Model) fetchSongs(src *source, it item, action queueAction) tea.Cmd {
	kind := src.kind
	if it.kind == kindSong && it.song != nil {
		s := *it.song
		return func() tea.Msg {
			return songsMsg{src: kind, action: action, songs: []subsonic.Song{s}, label: s.Title}
		}
	}
	client := src.api
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*apiTimeout)
		defer cancel()
		msg := songsMsg{src: kind, action: action, label: it.title}
		switch it.kind {
		case kindAlbum:
			al, err := client.Album(ctx, it.id)
			if err != nil {
				msg.err = err
				break
			}
			msg.songs = al.Song
		case kindPlaylist:
			pl, err := client.Playlist(ctx, it.id)
			if err != nil {
				msg.err = err
				break
			}
			msg.songs = pl.Entry
		case kindArtist:
			a, err := client.Artist(ctx, it.id)
			if err != nil {
				msg.err = err
				break
			}
			albums := a.Album
			sort.SliceStable(albums, func(i, j int) bool { return albums[i].Year < albums[j].Year })
			for _, al := range albums {
				full, err := client.Album(ctx, al.ID)
				if err != nil {
					msg.err = err
					break
				}
				msg.songs = append(msg.songs, full.Song...)
			}
		}
		return msg
	}
}

type randomSongsMsg struct {
	src   SourceKind
	songs []subsonic.Song
	err   error
}

func (m *Model) fetchRandom(src *source) tea.Cmd {
	client, kind := src.api, src.kind
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		songs, err := client.RandomSongs(ctx, 100)
		return randomSongsMsg{src: kind, songs: songs, err: err}
	}
}

// --- favorites & scrobbling ---

type starMsg struct {
	src     SourceKind
	id      string
	starred bool
	title   string
	err     error
}

func (m *Model) toggleStar(src *source, it *item) tea.Cmd {
	client, kind := src.api, src.kind
	if client == nil {
		return nil
	}
	id, want, title := it.id, !it.starred, it.title
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		var err error
		if want {
			err = client.Star(ctx, id)
		} else {
			err = client.Unstar(ctx, id)
		}
		return starMsg{src: kind, id: id, starred: want, title: title, err: err}
	}
}

func (m *Model) applyStar(msg starMsg) {
	stamp := ""
	if msg.starred {
		stamp = time.Now().UTC().Format(time.RFC3339)
	}
	s := m.sources[msg.src]
	for _, stack := range s.stacks {
		for _, p := range stack {
			for i := range p.list.items {
				it := &p.list.items[i]
				if it.id == msg.id && it.kind != kindHeader {
					it.starred = msg.starred
					if it.song != nil {
						it.song.Starred = stamp
					}
				}
			}
		}
	}
	for i := range s.queue.songs {
		if s.queue.songs[i].ID == msg.id {
			s.queue.songs[i].Starred = stamp
		}
	}
	s.queue.sync()
}

type scrobbleMsg struct{ err error }

func (m *Model) scrobble(src *source, id string, submission bool) tea.Cmd {
	client := src.api
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		return scrobbleMsg{err: client.Scrobble(ctx, id, submission)}
	}
}
