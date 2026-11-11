package metadata

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pixelotes/scout"
)

// TMDB covers movies, shows and anime. It needs an API key (v3 key or v4
// read token).
type TMDB struct {
	key  string
	lang string
	http httpDoer
	base string
}

func NewTMDB(apiKey, lang string, timeout time.Duration) *TMDB {
	t := &TMDB{key: apiKey, lang: orDefault(lang, "en"), base: "https://api.themoviedb.org/3"}
	headers := map[string]string{}
	// A v4 read access token is a JWT; send it as a bearer token instead of
	// putting it in the URL.
	if strings.Count(apiKey, ".") == 2 {
		headers["Authorization"] = "Bearer " + apiKey
	}
	t.http = newDoer("tmdb", timeout, headers)
	return t
}

func (t *TMDB) Name() string { return "tmdb" }

func (t *TMDB) Supports(k scout.Kind) bool { return k.Valid() }

func (t *TMDB) url(path string, params url.Values, lang string) string {
	if params == nil {
		params = url.Values{}
	}
	if !strings.Contains(t.key, ".") {
		params.Set("api_key", t.key)
	}
	params.Set("language", orDefault(lang, t.lang))
	return t.base + path + "?" + params.Encode()
}

func (t *TMDB) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	p := url.Values{"query": {q.Title}}
	path := "/search/tv"
	if q.Kind == scout.Movie {
		path = "/search/movie"
		if q.Year > 0 {
			p.Set("year", strconv.Itoa(q.Year))
		}
	} else if q.Year > 0 {
		p.Set("first_air_date_year", strconv.Itoa(q.Year))
	}

	var resp struct {
		Results []struct {
			ID           int     `json:"id"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			Original     string  `json:"original_title"`
			OriginalName string  `json:"original_name"`
			ReleaseDate  string  `json:"release_date"`
			FirstAir     string  `json:"first_air_date"`
			Overview     string  `json:"overview"`
			PosterPath   string  `json:"poster_path"`
			BackdropPath string  `json:"backdrop_path"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}
	if err := t.http.get(ctx, t.url(path, p, q.Language), &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, notFound("tmdb", q.Title)
	}

	var items []scout.Item
	for _, r := range resp.Results {
		if len(items) == limit(q) {
			break
		}
		id := strconv.Itoa(r.ID)
		items = append(items, scout.Item{
			Kind:        q.Kind,
			Provider:    "tmdb",
			ID:          id,
			IDs:         scout.IDs{"tmdb": id},
			Title:       first(r.Title, r.Name),
			Original:    first(r.Original, r.OriginalName),
			Year:        yearOf(first(r.ReleaseDate, r.FirstAir)),
			Overview:    r.Overview,
			PosterURL:   tmdbImage(r.PosterPath, "w500"),
			BackdropURL: tmdbImage(r.BackdropPath, "w1280"),
			Rating:      r.VoteAverage,
		})
	}
	return items, nil
}

type tmdbImages struct {
	Logos []struct {
		FilePath string `json:"file_path"`
		Lang     string `json:"iso_639_1"`
	} `json:"logos"`
}

func (t *TMDB) Get(ctx context.Context, k scout.Kind, id, lang string) (*scout.Item, error) {
	if k == scout.Movie {
		return t.getMovie(ctx, id, lang)
	}
	return t.getShow(ctx, k, id, lang)
}

func (t *TMDB) getMovie(ctx context.Context, id, lang string) (*scout.Item, error) {
	var d struct {
		ID           int     `json:"id"`
		Title        string  `json:"title"`
		Original     string  `json:"original_title"`
		ReleaseDate  string  `json:"release_date"`
		Overview     string  `json:"overview"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		VoteAverage  float64 `json:"vote_average"`
		Runtime      int     `json:"runtime"`
		Tagline      string  `json:"tagline"`
		Status       string  `json:"status"`
		IMDbID       string  `json:"imdb_id"`
		Genres       []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Companies []struct {
			Name string `json:"name"`
		} `json:"production_companies"`
		Images tmdbImages `json:"images"`
	}
	p := url.Values{
		"append_to_response":     {"images"},
		"include_image_language": {orDefault(lang, t.lang) + ",en,null"},
	}
	if err := t.http.get(ctx, t.url("/movie/"+url.PathEscape(id), p, lang), &d); err != nil {
		return nil, err
	}
	item := &scout.Item{
		Kind:        scout.Movie,
		Provider:    "tmdb",
		ID:          strconv.Itoa(d.ID),
		IDs:         scout.IDs{"tmdb": strconv.Itoa(d.ID)},
		Title:       d.Title,
		Original:    d.Original,
		Year:        yearOf(d.ReleaseDate),
		Overview:    d.Overview,
		PosterURL:   tmdbImage(d.PosterPath, "w500"),
		BackdropURL: tmdbImage(d.BackdropPath, "w1280"),
		LogoURL:     t.pickLogo(d.Images, lang),
		Rating:      d.VoteAverage,
		Runtime:     d.Runtime,
		Tagline:     d.Tagline,
		Status:      d.Status,
	}
	if d.IMDbID != "" {
		item.IDs["imdb"] = d.IMDbID
	}
	for _, g := range d.Genres {
		item.Genres = append(item.Genres, g.Name)
	}
	if len(d.Companies) > 0 {
		item.Studio = d.Companies[0].Name
	}
	return item, nil
}

func (t *TMDB) getShow(ctx context.Context, k scout.Kind, id, lang string) (*scout.Item, error) {
	var d struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		Original     string  `json:"original_name"`
		FirstAir     string  `json:"first_air_date"`
		Overview     string  `json:"overview"`
		PosterPath   string  `json:"poster_path"`
		BackdropPath string  `json:"backdrop_path"`
		VoteAverage  float64 `json:"vote_average"`
		Status       string  `json:"status"`
		RunTimes     []int   `json:"episode_run_time"`
		Genres       []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Networks []struct {
			Name string `json:"name"`
		} `json:"networks"`
		Seasons []struct {
			Number int `json:"season_number"`
		} `json:"seasons"`
		External struct {
			IMDb string `json:"imdb_id"`
			TVDB int    `json:"tvdb_id"`
		} `json:"external_ids"`
		Images tmdbImages `json:"images"`
	}
	p := url.Values{
		"append_to_response":     {"external_ids,images"},
		"include_image_language": {orDefault(lang, t.lang) + ",en,null"},
	}
	if err := t.http.get(ctx, t.url("/tv/"+url.PathEscape(id), p, lang), &d); err != nil {
		return nil, err
	}
	item := &scout.Item{
		Kind:        k,
		Provider:    "tmdb",
		ID:          strconv.Itoa(d.ID),
		IDs:         scout.IDs{"tmdb": strconv.Itoa(d.ID)},
		Title:       d.Name,
		Original:    d.Original,
		Year:        yearOf(d.FirstAir),
		Overview:    d.Overview,
		PosterURL:   tmdbImage(d.PosterPath, "w500"),
		BackdropURL: tmdbImage(d.BackdropPath, "w1280"),
		LogoURL:     t.pickLogo(d.Images, lang),
		Rating:      d.VoteAverage,
		Status:      d.Status,
		Seasons:     map[int][]scout.Episode{},
	}
	if d.External.IMDb != "" {
		item.IDs["imdb"] = d.External.IMDb
	}
	if d.External.TVDB != 0 {
		item.IDs["tvdb"] = strconv.Itoa(d.External.TVDB)
	}
	if len(d.RunTimes) > 0 {
		item.Runtime = d.RunTimes[0]
	}
	for _, g := range d.Genres {
		item.Genres = append(item.Genres, g.Name)
	}
	if len(d.Networks) > 0 {
		item.Studio = d.Networks[0].Name
	}

	for _, s := range d.Seasons {
		if s.Number == 0 { // specials
			continue
		}
		var sr struct {
			Episodes []struct {
				Number   int    `json:"episode_number"`
				Name     string `json:"name"`
				AirDate  string `json:"air_date"`
				Runtime  int    `json:"runtime"`
				Overview string `json:"overview"`
			} `json:"episodes"`
		}
		path := fmt.Sprintf("/tv/%d/season/%d", d.ID, s.Number)
		if err := t.http.get(ctx, t.url(path, nil, lang), &sr); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		for _, e := range sr.Episodes {
			item.Seasons[s.Number] = append(item.Seasons[s.Number], scout.Episode{
				Number: e.Number, Title: e.Name, AirDate: e.AirDate, Runtime: e.Runtime, Plot: e.Overview,
			})
		}
	}
	return item, nil
}

// pickLogo prefers a logo in the requested language, then English, then any.
func (t *TMDB) pickLogo(imgs tmdbImages, lang string) string {
	want := orDefault(lang, t.lang)
	for _, pref := range []string{want, "en", ""} {
		for _, l := range imgs.Logos {
			if pref == "" || l.Lang == pref {
				return tmdbImage(l.FilePath, "w500")
			}
		}
	}
	return ""
}

func tmdbImage(path, size string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http") {
		return path
	}
	return "https://image.tmdb.org/t/p/" + size + path
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
