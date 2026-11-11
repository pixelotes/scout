package metadata

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/pixelotes/scout"
)

// Trakt covers movies, shows and anime. Needs a client ID. It has no images,
// but it carries the IMDb, TMDB and TVDB IDs, which helps cross-referencing.
type Trakt struct {
	http httpDoer
	base string
}

func NewTrakt(clientID string, timeout time.Duration) *Trakt {
	return &Trakt{
		http: newDoer("trakt", timeout, map[string]string{"trakt-api-version": "2", "trakt-api-key": clientID}),
		base: "https://api.trakt.tv",
	}
}

func (t *Trakt) Name() string { return "trakt" }

func (t *Trakt) Supports(k scout.Kind) bool { return k.Valid() }

type traktMedia struct {
	Title    string   `json:"title"`
	Year     int      `json:"year"`
	Overview string   `json:"overview"`
	Rating   float64  `json:"rating"`
	Runtime  int      `json:"runtime"`
	Genres   []string `json:"genres"`
	Status   string   `json:"status"`
	Network  string   `json:"network"`
	Tagline  string   `json:"tagline"`
	IDs      struct {
		Trakt int    `json:"trakt"`
		Slug  string `json:"slug"`
		IMDb  string `json:"imdb"`
		TMDB  int    `json:"tmdb"`
		TVDB  int    `json:"tvdb"`
	} `json:"ids"`
}

func (m traktMedia) item(k scout.Kind) scout.Item {
	id := strconv.Itoa(m.IDs.Trakt)
	it := scout.Item{
		Kind: k, Provider: "trakt", ID: id, IDs: scout.IDs{"trakt": id},
		Title: m.Title, Year: m.Year, Overview: m.Overview, Rating: m.Rating,
		Runtime: m.Runtime, Genres: m.Genres, Status: m.Status, Studio: m.Network, Tagline: m.Tagline,
	}
	if m.IDs.IMDb != "" {
		it.IDs["imdb"] = m.IDs.IMDb
	}
	if m.IDs.TMDB != 0 {
		it.IDs["tmdb"] = strconv.Itoa(m.IDs.TMDB)
	}
	if m.IDs.TVDB != 0 {
		it.IDs["tvdb"] = strconv.Itoa(m.IDs.TVDB)
	}
	return it
}

func traktType(k scout.Kind) string {
	if k == scout.Movie {
		return "movie"
	}
	return "show"
}

func (t *Trakt) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	typ := traktType(q.Kind)
	p := url.Values{"query": {q.Title}, "limit": {strconv.Itoa(limit(q))}, "extended": {"full"}}
	if q.Year > 0 {
		p.Set("years", strconv.Itoa(q.Year))
	}
	var resp []map[string]traktMedia
	if err := t.http.get(ctx, fmt.Sprintf("%s/search/%s?%s", t.base, typ, p.Encode()), &resp); err != nil {
		return nil, err
	}
	var items []scout.Item
	for _, r := range resp {
		if m, ok := r[typ]; ok {
			items = append(items, m.item(q.Kind))
		}
	}
	if len(items) == 0 {
		return nil, notFound("trakt", q.Title)
	}
	return items, nil
}

func (t *Trakt) Get(ctx context.Context, k scout.Kind, id, _ string) (*scout.Item, error) {
	typ := traktType(k)
	var m traktMedia
	if err := t.http.get(ctx, fmt.Sprintf("%s/%ss/%s?extended=full", t.base, typ, url.PathEscape(id)), &m); err != nil {
		return nil, err
	}
	it := m.item(k)
	if k == scout.Movie {
		return &it, nil
	}
	var seasons []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Number     int    `json:"number"`
			Title      string `json:"title"`
			FirstAired string `json:"first_aired"`
			Runtime    int    `json:"runtime"`
			Overview   string `json:"overview"`
		} `json:"episodes"`
	}
	if err := t.http.get(ctx, fmt.Sprintf("%s/shows/%s/seasons?extended=episodes,full", t.base, url.PathEscape(id)), &seasons); err != nil {
		return nil, err
	}
	it.Seasons = map[int][]scout.Episode{}
	for _, s := range seasons {
		if s.Number == 0 {
			continue
		}
		for _, e := range s.Episodes {
			air := ""
			if ts, err := time.Parse(time.RFC3339, e.FirstAired); err == nil {
				air = ts.Format("2006-01-02")
			}
			it.Seasons[s.Number] = append(it.Seasons[s.Number], scout.Episode{
				Number: e.Number, Title: e.Title, AirDate: air, Runtime: e.Runtime, Plot: e.Overview,
			})
		}
	}
	return &it, nil
}
