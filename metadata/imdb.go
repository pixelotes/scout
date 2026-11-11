package metadata

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pixelotes/scout"
)

// IMDb covers movies without an API key, using two unofficial but long-lived
// endpoints: IMDb's search suggestions (IDs, titles, years) and Cinemeta,
// Stremio's metadata service (plot, poster, rating, genres, runtime).
// Neither promises stability, so keep TMDB first when you have a key.
type IMDb struct {
	http        httpDoer
	suggestURL  string
	cinemetaURL string
}

func NewIMDb(timeout time.Duration) *IMDb {
	return &IMDb{
		http:        newDoer("imdb", timeout, nil),
		suggestURL:  "https://v2.sg.media-imdb.com/suggestion",
		cinemetaURL: "https://v3-cinemeta.strem.io",
	}
}

func (c *IMDb) Name() string { return "imdb" }

func (c *IMDb) Supports(k scout.Kind) bool { return k == scout.Movie }

var imdbMovieKinds = map[string]bool{"movie": true, "short": true, "tvMovie": true, "video": true}

func (c *IMDb) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	query := strings.ToLower(strings.TrimSpace(q.Title))
	fc := firstAlnum(query)
	if fc == 0 {
		return nil, fmt.Errorf("imdb: title needs letters or digits: %w", scout.ErrBadRequest)
	}
	var sug struct {
		D []struct {
			ID    string `json:"id"`
			Title string `json:"l"`
			Year  int    `json:"y"`
			Kind  string `json:"qid"`
			Image struct {
				URL string `json:"imageUrl"`
			} `json:"i"`
		} `json:"d"`
	}
	u := fmt.Sprintf("%s/%c/%s.json", c.suggestURL, fc, url.PathEscape(query))
	if err := c.http.get(ctx, u, &sug); err != nil {
		return nil, err
	}
	var items []scout.Item
	for _, d := range sug.D {
		if !strings.HasPrefix(d.ID, "tt") || !imdbMovieKinds[d.Kind] {
			continue
		}
		items = append(items, scout.Item{
			Kind: scout.Movie, Provider: "imdb", ID: d.ID, IDs: scout.IDs{"imdb": d.ID},
			Title: d.Title, Year: d.Year, PosterURL: d.Image.URL,
		})
	}
	items = preferYear(items, q.Year)
	if len(items) == 0 {
		return nil, notFound("imdb", q.Title)
	}
	if len(items) > limit(q) {
		items = items[:limit(q)]
	}

	// Fill in plot and rating; a failed lookup keeps what the suggestion gave.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range items {
		wg.Add(1)
		go func(it *scout.Item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c.enrich(ctx, it)
		}(&items[i])
	}
	wg.Wait()
	return items, nil
}

type cinemetaMeta struct {
	Meta *struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Poster      string   `json:"poster"`
		Background  string   `json:"background"`
		Logo        string   `json:"logo"`
		IMDbRating  string   `json:"imdbRating"`
		Genres      []string `json:"genres"`
		Runtime     string   `json:"runtime"`
		ReleaseInfo string   `json:"releaseInfo"`
		MovieDBID   int      `json:"moviedb_id"`
	} `json:"meta"`
}

func (c *IMDb) enrich(ctx context.Context, it *scout.Item) error {
	var m cinemetaMeta
	if err := c.http.get(ctx, fmt.Sprintf("%s/meta/movie/%s.json", c.cinemetaURL, it.ID), &m); err != nil {
		return err
	}
	if m.Meta == nil {
		return notFound("imdb", it.ID)
	}
	mm := m.Meta
	it.Title = first(it.Title, mm.Name)
	it.Overview = mm.Description
	it.PosterURL = strings.Replace(first(mm.Poster, it.PosterURL), "/poster/small/", "/poster/medium/", 1)
	it.BackdropURL = mm.Background
	it.LogoURL = mm.Logo
	it.Genres = mm.Genres
	if it.Year == 0 {
		it.Year = yearOf(mm.ReleaseInfo)
	}
	if r, err := strconv.ParseFloat(mm.IMDbRating, 64); err == nil {
		it.Rating = r
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(mm.Runtime, " min")); err == nil {
		it.Runtime = n
	}
	if mm.MovieDBID != 0 {
		it.IDs["tmdb"] = strconv.Itoa(mm.MovieDBID)
	}
	return nil
}

func (c *IMDb) Get(ctx context.Context, _ scout.Kind, id, _ string) (*scout.Item, error) {
	if !strings.HasPrefix(id, "tt") {
		return nil, fmt.Errorf("imdb IDs start with tt: %w", scout.ErrBadRequest)
	}
	it := &scout.Item{Kind: scout.Movie, Provider: "imdb", ID: id, IDs: scout.IDs{"imdb": id}}
	if err := c.enrich(ctx, it); err != nil {
		return nil, err
	}
	return it, nil
}

func firstAlnum(s string) rune {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
	}
	return 0
}
