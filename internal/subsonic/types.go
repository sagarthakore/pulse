package subsonic

import "fmt"

// Artist is an ID3-tag based artist (getArtists / getArtist / search3).
type Artist struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	AlbumCount int     `json:"albumCount"`
	Starred    string  `json:"starred,omitempty"`
	Album      []Album `json:"album,omitempty"`
}

// Album is an ID3-tag based album (getAlbum / getAlbumList2 / search3).
type Album struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Artist    string `json:"artist"`
	ArtistID  string `json:"artistId"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	Year      int    `json:"year"`
	Genre     string `json:"genre"`
	Starred   string `json:"starred,omitempty"`
	Song      []Song `json:"song,omitempty"`
}

// Song is a single track ("child" in Subsonic terms).
type Song struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Album       string `json:"album"`
	AlbumID     string `json:"albumId"`
	Artist      string `json:"artist"`
	ArtistID    string `json:"artistId"`
	Track       int    `json:"track"`
	DiscNumber  int    `json:"discNumber"`
	Year        int    `json:"year"`
	Duration    int    `json:"duration"` // seconds
	BitRate     int    `json:"bitRate"`
	Suffix      string `json:"suffix"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Starred     string `json:"starred,omitempty"`
}

// Playlist is a server-side playlist.
type Playlist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	SongCount int    `json:"songCount"`
	Duration  int    `json:"duration"`
	Entry     []Song `json:"entry,omitempty"`
}

// SearchResult holds the results of a search3 call.
type SearchResult struct {
	Artist []Artist `json:"artist"`
	Album  []Album  `json:"album"`
	Song   []Song   `json:"song"`
}

// response mirrors the "subsonic-response" envelope. Only the fields this
// client uses are declared.
type response struct {
	Status        string    `json:"status"`
	Version       string    `json:"version"`
	Type          string    `json:"type"`
	ServerVersion string    `json:"serverVersion"`
	Error         *APIError `json:"error"`

	Artists *struct {
		Index []struct {
			Name   string   `json:"name"`
			Artist []Artist `json:"artist"`
		} `json:"index"`
	} `json:"artists"`
	Artist     *Artist `json:"artist"`
	Album      *Album  `json:"album"`
	AlbumList2 *struct {
		Album []Album `json:"album"`
	} `json:"albumList2"`
	Playlists *struct {
		Playlist []Playlist `json:"playlist"`
	} `json:"playlists"`
	Playlist      *Playlist     `json:"playlist"`
	SearchResult3 *SearchResult `json:"searchResult3"`
	RandomSongs   *struct {
		Song []Song `json:"song"`
	} `json:"randomSongs"`
	Starred2 *SearchResult `json:"starred2"`
}

// APIError is an error returned by the server inside a "failed" response.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("subsonic error %d: %s", e.Code, e.Message)
}
