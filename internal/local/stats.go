package local

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// stats holds favorites and play counts for a local library, kept in a
// small JSON file next to pulse's config.
type stats struct {
	mu   sync.Mutex
	path string
	data statsData
}

type statsData struct {
	Starred map[string]time.Time `json:"starred"`
	Plays   map[string]playStat  `json:"plays"`
}

type playStat struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

func loadStats(path string) *stats {
	s := &stats{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data) // a corrupt file just starts fresh
	}
	if s.data.Starred == nil {
		s.data.Starred = map[string]time.Time{}
	}
	if s.data.Plays == nil {
		s.data.Plays = map[string]playStat{}
	}
	return s
}

// starred returns the star time in RFC 3339 (as Subsonic does), or "".
func (s *stats) starred(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.data.Starred[id]; ok {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func (s *stats) plays(id string) playStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Plays[id]
}

func (s *stats) setStar(id string, on bool) error {
	s.mu.Lock()
	if on {
		s.data.Starred[id] = time.Now()
	} else {
		delete(s.data.Starred, id)
	}
	b, err := json.Marshal(s.data)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b)
}

func (s *stats) addPlay(id string, at time.Time) error {
	s.mu.Lock()
	p := s.data.Plays[id]
	p.Count++
	p.Last = at
	s.data.Plays[id] = p
	b, err := json.Marshal(s.data)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b)
}
