// Package nfo writes Kodi/Jellyfin-style NFO files and artwork next to media,
// with every external ID an item carries as <uniqueid>.
package nfo

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pixelotes/scout"
)

type uniqueID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type thumb struct {
	Aspect string `xml:"aspect,attr,omitempty"`
	URL    string `xml:",chardata"`
}

type common struct {
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	Year          int        `xml:"year,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	Tagline       string     `xml:"tagline,omitempty"`
	Runtime       int        `xml:"runtime,omitempty"`
	Rating        float64    `xml:"rating,omitempty"`
	Studio        string     `xml:"studio,omitempty"`
	Status        string     `xml:"status,omitempty"`
	Genres        []string   `xml:"genre"`
	UniqueIDs     []uniqueID `xml:"uniqueid"`
	Thumbs        []thumb    `xml:"thumb"`
	Fanart        *struct {
		Thumb string `xml:"thumb"`
	} `xml:"fanart,omitempty"`
}

// idPriority decides which ID is marked default.
var idPriority = []string{"imdb", "tmdb", "tvdb", "tvmaze", "anilist", "kitsu", "mal", "trakt"}

func uniqueIDs(ids scout.IDs) []uniqueID {
	keys := make([]string, 0, len(ids))
	for k, v := range ids {
		if v != "" {
			keys = append(keys, k)
		}
	}
	rank := func(k string) int {
		for i, p := range idPriority {
			if p == k {
				return i
			}
		}
		return len(idPriority)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := rank(keys[i]), rank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	out := make([]uniqueID, len(keys))
	for i, k := range keys {
		out[i] = uniqueID{Type: k, Value: ids[k], Default: i == 0}
	}
	return out
}

func fromItem(it *scout.Item) common {
	c := common{
		Title: it.Title, OriginalTitle: it.Original, Year: it.Year, Plot: it.Overview,
		Tagline: it.Tagline, Runtime: it.Runtime, Rating: it.Rating, Studio: it.Studio,
		Status: it.Status, Genres: it.Genres, UniqueIDs: uniqueIDs(it.IDs),
	}
	if c.OriginalTitle == c.Title {
		c.OriginalTitle = ""
	}
	if it.PosterURL != "" {
		c.Thumbs = append(c.Thumbs, thumb{Aspect: "poster", URL: it.PosterURL})
	}
	if it.LogoURL != "" {
		c.Thumbs = append(c.Thumbs, thumb{Aspect: "clearlogo", URL: it.LogoURL})
	}
	if it.BackdropURL != "" {
		c.Fanart = &struct {
			Thumb string `xml:"thumb"`
		}{Thumb: it.BackdropURL}
	}
	return c
}

// WriteMovie writes dir/movie.nfo.
func WriteMovie(dir string, it *scout.Item) error {
	return write(filepath.Join(dir, "movie.nfo"), struct {
		XMLName xml.Name `xml:"movie"`
		common
	}{common: fromItem(it)})
}

// WriteShow writes dir/tvshow.nfo.
func WriteShow(dir string, it *scout.Item) error {
	return write(filepath.Join(dir, "tvshow.nfo"), struct {
		XMLName xml.Name `xml:"tvshow"`
		common
	}{common: fromItem(it)})
}

// WriteEpisode writes <video name>.nfo next to the video.
func WriteEpisode(videoPath string, season int, ep scout.Episode) error {
	type episode struct {
		XMLName xml.Name `xml:"episodedetails"`
		Title   string   `xml:"title"`
		Season  int      `xml:"season"`
		Episode int      `xml:"episode"`
		Runtime int      `xml:"runtime,omitempty"`
		Aired   string   `xml:"aired,omitempty"`
		Plot    string   `xml:"plot,omitempty"`
	}
	path := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".nfo"
	return write(path, episode{Title: ep.Title, Season: season, Episode: ep.Number,
		Runtime: ep.Runtime, Aired: ep.AirDate, Plot: ep.Plot})
}

func write(path string, v any) error {
	out, err := xml.MarshalIndent(v, "", "    ")
	if err != nil {
		return fmt.Errorf("nfo: %w", err)
	}
	data := append([]byte(xml.Header), out...)
	data = append(data, '\n')
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Artwork names, matching what RMS already reads.
var artwork = []struct{ file, kind string }{
	{"poster.jpg", "poster"},
	{"folder.jpg", "poster"},
	{"fanart.jpg", "backdrop"},
	{"backdrop.jpg", "backdrop"},
	{"logo.png", "logo"},
}

// DownloadArtwork saves poster, fanart and logo into dir, skipping files that
// already exist. It returns the files written.
func DownloadArtwork(ctx context.Context, dir string, it *scout.Item) ([]string, error) {
	urls := map[string]string{"poster": it.PosterURL, "backdrop": it.BackdropURL, "logo": it.LogoURL}
	client := &http.Client{Timeout: 30 * time.Second}
	cache := map[string][]byte{}
	var written []string
	var errs []string
	for _, a := range artwork {
		dest := filepath.Join(dir, a.file)
		u := urls[a.kind]
		if u == "" {
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		data, ok := cache[u]
		if !ok {
			var err error
			data, err = fetch(ctx, client, u)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", a.file, err))
				continue
			}
			cache[u] = data
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return written, err
		}
		written = append(written, a.file)
	}
	if len(errs) > 0 {
		return written, fmt.Errorf("artwork: %s", strings.Join(errs, "; "))
	}
	return written, nil
}

func fetch(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "scout/1.0")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}
