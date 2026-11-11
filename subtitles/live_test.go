package subtitles

import (
	"context"
	"os"
	"testing"

	"github.com/pixelotes/scout"
)

// Live checks against the real sites. Run with, e.g.:
//
//	SCOUT_LIVE_FLARESOLVERR=http://127.0.0.1:8191 go test ./subtitles -run Live -v
func TestLiveSubdivx(t *testing.T) {
	solver := os.Getenv("SCOUT_LIVE_FLARESOLVERR")
	if solver == "" {
		t.Skip("SCOUT_LIVE_FLARESOLVERR not set")
	}
	s := NewSubdivx(solver, 0)
	for _, q := range []scout.SubtitleQuery{
		{Query: "The Bear", Season: 2, Episode: 7, Languages: []string{"es"}},
		{Query: "Severance", Season: 1, Episode: 2, Languages: []string{"es"}},
		{Query: "Dune 2021", Languages: []string{"es"}},
		{Query: "Frieren", Season: 1, Episode: 1, Languages: []string{"es"}},
	} {
		subs, err := s.Search(context.Background(), q)
		if err != nil {
			t.Errorf("%+v: %v", q, err)
			continue
		}
		t.Logf("%s S%dE%d: %d results, first %s %q", q.Query, q.Season, q.Episode, len(subs), subs[0].FileID, subs[0].FileName)
	}
}
