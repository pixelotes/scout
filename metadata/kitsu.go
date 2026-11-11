package metadata

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/pixelotes/scout"
)

// Kitsu covers anime series and anime movies. Keyless, with episode titles.
type Kitsu struct {
	http httpDoer
	base string
}

func NewKitsu(timeout time.Duration) *Kitsu {
	return &Kitsu{
		http: newDoer("kitsu", timeout, map[string]string{"Accept": "application/vnd.api+json"}),
		base: "https://kitsu.io/api/edge",
	}
}

func (k *Kitsu) Name() string { return "kitsu" }

func (k *Kitsu) Supports(kind scout.Kind) bool { return kind == scout.Anime || kind == scout.Movie }

type kitsuAnime struct {
	ID         string `json:"id"`
	Attributes struct {
		CanonicalTitle string `json:"canonicalTitle"`
		Titles         struct {
			En   string `json:"en"`
			EnJp string `json:"en_jp"`
			JaJp string `json:"ja_jp"`
		} `json:"titles"`
		Synopsis      string `json:"synopsis"`
		StartDate     string `json:"startDate"`
		Status        string `json:"status"`
		Subtype       string `json:"subtype"`
		EpisodeCount  int    `json:"episodeCount"`
		EpisodeLength int    `json:"episodeLength"`
		AverageRating string `json:"averageRating"`
		PosterImage   *struct {
			Large    string `json:"large"`
			Original string `json:"original"`
		} `json:"posterImage"`
		CoverImage *struct {
			Large    string `json:"large"`
			Original string `json:"original"`
		} `json:"coverImage"`
	} `json:"attributes"`
}

func (a kitsuAnime) item(kind scout.Kind) scout.Item {
	at := a.Attributes
	it := scout.Item{
		Kind:     kind,
		Provider: "kitsu",
		ID:       a.ID,
		IDs:      scout.IDs{"kitsu": a.ID},
		Title:    first(at.Titles.En, at.CanonicalTitle, at.Titles.EnJp),
		Original: first(at.Titles.JaJp, at.Titles.EnJp),
		Year:     yearOf(at.StartDate),
		Overview: at.Synopsis,
		Status:   at.Status,
		Runtime:  at.EpisodeLength,
	}
	if at.PosterImage != nil {
		it.PosterURL = first(at.PosterImage.Large, at.PosterImage.Original)
	}
	if at.CoverImage != nil {
		it.BackdropURL = first(at.CoverImage.Large, at.CoverImage.Original)
	}
	if r, err := strconv.ParseFloat(at.AverageRating, 64); err == nil {
		it.Rating = r / 10
	}
	return it
}

func (k *Kitsu) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	p := url.Values{"filter[text]": {q.Title}, "page[limit]": {strconv.Itoa(limit(q) * 2)}}
	if q.Kind == scout.Movie {
		p.Set("filter[subtype]", "movie")
	} else {
		p.Set("filter[subtype]", "TV,ONA,OVA,special")
	}
	var resp struct {
		Data []kitsuAnime `json:"data"`
	}
	if err := k.http.get(ctx, k.base+"/anime?"+p.Encode(), &resp); err != nil {
		return nil, err
	}
	var items []scout.Item
	for _, a := range resp.Data {
		items = append(items, a.item(q.Kind))
	}
	items = preferYear(items, q.Year)
	if len(items) == 0 {
		return nil, notFound("kitsu", q.Title)
	}
	if len(items) > limit(q) {
		items = items[:limit(q)]
	}
	return items, nil
}

func (k *Kitsu) Get(ctx context.Context, kind scout.Kind, id, _ string) (*scout.Item, error) {
	var resp struct {
		Data     kitsuAnime `json:"data"`
		Included []struct {
			Type       string `json:"type"`
			Attributes struct {
				Name       string `json:"name"`
				Site       string `json:"externalSite"`
				ExternalID string `json:"externalId"`
				Role       string `json:"role"`
				Title      string `json:"title"`
				Producer   string `json:"producer"`
			} `json:"attributes"`
		} `json:"included"`
	}
	u := k.base + "/anime/" + url.PathEscape(id) + "?include=categories,mappings"
	if err := k.http.get(ctx, u, &resp); err != nil {
		return nil, err
	}
	it := resp.Data.item(kind)
	for _, inc := range resp.Included {
		switch inc.Type {
		case "categories":
			it.Genres = append(it.Genres, inc.Attributes.Title)
		case "mappings":
			switch inc.Attributes.Site {
			case "myanimelist/anime":
				it.IDs["mal"] = inc.Attributes.ExternalID
			case "anilist/anime":
				it.IDs["anilist"] = inc.Attributes.ExternalID
			case "thetvdb/series", "thetvdb":
				it.IDs["tvdb"] = inc.Attributes.ExternalID
			}
		}
	}
	if kind != scout.Movie {
		eps, err := k.episodes(ctx, id)
		if err != nil {
			return nil, err
		}
		it.Seasons = eps
	}
	return &it, nil
}

func (k *Kitsu) episodes(ctx context.Context, id string) (map[int][]scout.Episode, error) {
	const page = 20
	out := map[int][]scout.Episode{}
	for offset := 0; ; offset += page {
		var resp struct {
			Data []struct {
				Attributes struct {
					Number         int    `json:"number"`
					SeasonNumber   int    `json:"seasonNumber"`
					CanonicalTitle string `json:"canonicalTitle"`
					Airdate        string `json:"airdate"`
					Length         int    `json:"length"`
					Synopsis       string `json:"synopsis"`
				} `json:"attributes"`
			} `json:"data"`
			Meta struct {
				Count int `json:"count"`
			} `json:"meta"`
		}
		u := fmt.Sprintf("%s/anime/%s/episodes?page[limit]=%d&page[offset]=%d&sort=number",
			k.base, url.PathEscape(id), page, offset)
		if err := k.http.get(ctx, u, &resp); err != nil {
			if offset == 0 {
				return nil, err
			}
			return out, nil // keep what we have
		}
		for _, e := range resp.Data {
			a := e.Attributes
			s := a.SeasonNumber
			if s == 0 {
				s = 1
			}
			out[s] = append(out[s], scout.Episode{
				Number: a.Number, Title: first(a.CanonicalTitle, fmt.Sprintf("Episode %d", a.Number)),
				AirDate: a.Airdate, Runtime: a.Length, Plot: a.Synopsis,
			})
		}
		if len(resp.Data) == 0 || offset+page >= resp.Meta.Count {
			return out, nil
		}
	}
}
