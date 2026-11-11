package metadata

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/pixelotes/scout"
)

// TVMaze covers shows and anime. Keyless; English only.
type TVMaze struct {
	http httpDoer
	base string
}

func NewTVMaze(timeout time.Duration) *TVMaze {
	return &TVMaze{http: newDoer("tvmaze", timeout, nil), base: "https://api.tvmaze.com"}
}

func (t *TVMaze) Name() string { return "tvmaze" }

func (t *TVMaze) Supports(k scout.Kind) bool { return k == scout.Show || k == scout.Anime }

type tvmazeShow struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Premiered string   `json:"premiered"`
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Runtime   int      `json:"averageRuntime"`
	Genres    []string `json:"genres"`
	Image     *struct {
		Original string `json:"original"`
	} `json:"image"`
	Rating struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Network *struct {
		Name string `json:"name"`
	} `json:"network"`
	WebChannel *struct {
		Name string `json:"name"`
	} `json:"webChannel"`
	Externals struct {
		IMDb    string `json:"imdb"`
		TheTVDB int    `json:"thetvdb"`
	} `json:"externals"`
	Embedded struct {
		Episodes []struct {
			Season  int    `json:"season"`
			Number  int    `json:"number"`
			Name    string `json:"name"`
			Airdate string `json:"airdate"`
			Runtime int    `json:"runtime"`
			Summary string `json:"summary"`
		} `json:"episodes"`
	} `json:"_embedded"`
}

func (s tvmazeShow) item(k scout.Kind) scout.Item {
	id := strconv.Itoa(s.ID)
	it := scout.Item{
		Kind:     k,
		Provider: "tvmaze",
		ID:       id,
		IDs:      scout.IDs{"tvmaze": id},
		Title:    s.Name,
		Year:     yearOf(s.Premiered),
		Overview: stripHTML(s.Summary),
		Rating:   s.Rating.Average,
		Status:   s.Status,
		Genres:   s.Genres,
		Runtime:  s.Runtime,
	}
	if s.Image != nil {
		it.PosterURL = s.Image.Original
	}
	if s.Network != nil {
		it.Studio = s.Network.Name
	} else if s.WebChannel != nil {
		it.Studio = s.WebChannel.Name
	}
	if s.Externals.IMDb != "" {
		it.IDs["imdb"] = s.Externals.IMDb
	}
	if s.Externals.TheTVDB != 0 {
		it.IDs["tvdb"] = strconv.Itoa(s.Externals.TheTVDB)
	}
	return it
}

func (t *TVMaze) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	var resp []struct {
		Show tvmazeShow `json:"show"`
	}
	if err := t.http.get(ctx, t.base+"/search/shows?q="+url.QueryEscape(q.Title), &resp); err != nil {
		return nil, err
	}
	var items []scout.Item
	for _, r := range resp {
		items = append(items, r.Show.item(q.Kind))
	}
	items = preferYear(items, q.Year)
	if len(items) == 0 {
		return nil, notFound("tvmaze", q.Title)
	}
	if len(items) > limit(q) {
		items = items[:limit(q)]
	}
	return items, nil
}

func (t *TVMaze) Get(ctx context.Context, k scout.Kind, id, _ string) (*scout.Item, error) {
	var s tvmazeShow
	if err := t.http.get(ctx, t.base+"/shows/"+url.PathEscape(id)+"?embed=episodes", &s); err != nil {
		return nil, err
	}
	it := s.item(k)
	it.Seasons = map[int][]scout.Episode{}
	for _, e := range s.Embedded.Episodes {
		it.Seasons[e.Season] = append(it.Seasons[e.Season], scout.Episode{
			Number: e.Number, Title: e.Name, AirDate: e.Airdate, Runtime: e.Runtime, Plot: stripHTML(e.Summary),
		})
	}
	return &it, nil
}

// preferYear moves items from the requested year to the front, keeping the
// provider's relevance order otherwise.
func preferYear(items []scout.Item, year int) []scout.Item {
	if year <= 0 {
		return items
	}
	out := make([]scout.Item, 0, len(items))
	for _, it := range items {
		if it.Year == year {
			out = append(out, it)
		}
	}
	for _, it := range items {
		if it.Year != year {
			out = append(out, it)
		}
	}
	return out
}
