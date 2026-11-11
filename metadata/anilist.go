package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pixelotes/scout"
)

// AniList covers anime series and anime movies. Keyless. It knows episode
// counts but not episode titles, so Get numbers them.
type AniList struct {
	http httpDoer
	base string
}

func NewAniList(timeout time.Duration) *AniList {
	return &AniList{http: newDoer("anilist", timeout, nil), base: "https://graphql.anilist.co"}
}

func (a *AniList) Name() string { return "anilist" }

func (a *AniList) Supports(k scout.Kind) bool { return k == scout.Anime || k == scout.Movie }

const anilistFields = `
  id idMal format status episodes duration averageScore genres bannerImage
  title { romaji english native }
  description(asHtml: false)
  coverImage { extraLarge large }
  startDate { year }
  studios(isMain: true) { nodes { name } }`

type anilistMedia struct {
	ID       int      `json:"id"`
	IDMal    int      `json:"idMal"`
	Format   string   `json:"format"`
	Status   string   `json:"status"`
	Episodes int      `json:"episodes"`
	Duration int      `json:"duration"`
	Score    int      `json:"averageScore"`
	Genres   []string `json:"genres"`
	Banner   string   `json:"bannerImage"`
	Title    struct {
		Romaji  string `json:"romaji"`
		English string `json:"english"`
		Native  string `json:"native"`
	} `json:"title"`
	Description string `json:"description"`
	Cover       struct {
		ExtraLarge string `json:"extraLarge"`
		Large      string `json:"large"`
	} `json:"coverImage"`
	StartDate struct {
		Year int `json:"year"`
	} `json:"startDate"`
	Studios struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"studios"`
}

func (m anilistMedia) item(k scout.Kind) scout.Item {
	id := strconv.Itoa(m.ID)
	it := scout.Item{
		Kind:        k,
		Provider:    "anilist",
		ID:          id,
		IDs:         scout.IDs{"anilist": id},
		Title:       first(m.Title.English, m.Title.Romaji),
		Original:    first(m.Title.Native, m.Title.Romaji),
		Year:        m.StartDate.Year,
		Overview:    stripHTML(m.Description),
		PosterURL:   first(m.Cover.ExtraLarge, m.Cover.Large),
		BackdropURL: m.Banner,
		Rating:      float64(m.Score) / 10,
		Genres:      m.Genres,
		Runtime:     m.Duration,
		Status:      m.Status,
	}
	if m.IDMal != 0 {
		it.IDs["mal"] = strconv.Itoa(m.IDMal)
	}
	if len(m.Studios.Nodes) > 0 {
		it.Studio = m.Studios.Nodes[0].Name
	}
	return it
}

func (a *AniList) query(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Status  int    `json:"status"`
		} `json:"errors"`
	}
	if err := a.http.do(ctx, http.MethodPost, a.base, bytes.NewReader(body), &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		e := resp.Errors[0]
		return &httpError{Provider: "anilist", Status: orInt(e.Status, 500), Body: e.Message}
	}
	return json.Unmarshal(resp.Data, out)
}

func (a *AniList) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	format := "[TV, TV_SHORT, ONA, OVA, SPECIAL]"
	if q.Kind == scout.Movie {
		format = "[MOVIE]"
	}
	gql := fmt.Sprintf(`query ($search: String, $perPage: Int) {
  Page(perPage: $perPage) {
    media(search: $search, type: ANIME, format_in: %s, sort: SEARCH_MATCH) {%s }
  }
}`, format, anilistFields)

	var data struct {
		Page struct {
			Media []anilistMedia `json:"media"`
		} `json:"Page"`
	}
	if err := a.query(ctx, gql, map[string]any{"search": q.Title, "perPage": limit(q) * 2}, &data); err != nil {
		return nil, err
	}
	var items []scout.Item
	for _, m := range data.Page.Media {
		items = append(items, m.item(q.Kind))
	}
	items = preferYear(items, q.Year)
	if len(items) == 0 {
		return nil, notFound("anilist", q.Title)
	}
	if len(items) > limit(q) {
		items = items[:limit(q)]
	}
	return items, nil
}

func (a *AniList) Get(ctx context.Context, k scout.Kind, id, _ string) (*scout.Item, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("anilist IDs are numeric: %w", scout.ErrBadRequest)
	}
	gql := `query ($id: Int) { Media(id: $id, type: ANIME) {` + anilistFields + ` } }`
	var data struct {
		Media *anilistMedia `json:"Media"`
	}
	if err := a.query(ctx, gql, map[string]any{"id": n}, &data); err != nil {
		return nil, err
	}
	if data.Media == nil {
		return nil, notFound("anilist", id)
	}
	it := data.Media.item(k)
	if k != scout.Movie && data.Media.Episodes > 0 {
		it.Seasons = map[int][]scout.Episode{}
		for i := 1; i <= data.Media.Episodes; i++ {
			it.Seasons[1] = append(it.Seasons[1], scout.Episode{
				Number: i, Title: fmt.Sprintf("Episode %d", i), Runtime: data.Media.Duration,
			})
		}
	}
	return &it, nil
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
