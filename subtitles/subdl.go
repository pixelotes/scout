package subtitles

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pixelotes/scout"
)

// SubDL talks to api.subdl.com (free API key from your subdl.com profile).
// It searches by IMDb/TMDB ID or title, never by hash, and serves ZIPs.
type SubDL struct {
	apiKey    string
	languages []string
	base      string
	dlBase    string
	http      *http.Client
}

func NewSubDL(apiKey string, languages []string, timeout time.Duration) *SubDL {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if len(languages) == 0 {
		languages = []string{"en"}
	}
	return &SubDL{
		apiKey: apiKey, languages: languages,
		base:   "https://api.subdl.com/api/v1/subtitles",
		dlBase: "https://dl.subdl.com/subtitle/",
		http:   &http.Client{Timeout: timeout},
	}
}

func (s *SubDL) Name() string { return "subdl" }

func (s *SubDL) Quota() scout.Quota { return scout.Quota{Provider: "subdl"} }

// subdlLang maps scout's codes to SubDL's (uppercase, a few renamed).
func subdlLang(l string) string {
	switch strings.ToLower(l) {
	case "pt-br", "pb":
		return "BR_PT"
	case "pt", "pt-pt":
		return "PT"
	case "zh", "zh-cn":
		return "ZH"
	case "zh-tw":
		return "ZH_BG"
	}
	return strings.ToUpper(strings.SplitN(l, "-", 2)[0])
}

// fromSubdlLang maps back to scout's lowercase codes.
func fromSubdlLang(l string) string {
	switch strings.ToUpper(l) {
	case "BR_PT":
		return "pt-br"
	case "ZH_BG":
		return "zh-tw"
	}
	return strings.ToLower(l)
}

type subdlResponse struct {
	Status    bool   `json:"status"`
	Error     string `json:"error"`
	Message   string `json:"message"`
	Subtitles []struct {
		ReleaseName string `json:"release_name"`
		Name        string `json:"name"`
		Lang        string `json:"lang"`
		Language    string `json:"language"`
		Author      string `json:"author"`
		URL         string `json:"url"`
		Season      int    `json:"season"`
		Episode     *int   `json:"episode"`
		HI          bool   `json:"hi"`
		FullSeason  bool   `json:"full_season"`
		EpisodeFrom *int   `json:"episode_from"`
		EpisodeEnd  *int   `json:"episode_end"`
	} `json:"subtitles"`
}

var subdlIDRe = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

func (s *SubDL) Search(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("subdl: API key not configured: %w", scout.ErrUnsupported)
	}
	langs := q.Languages
	if len(langs) == 0 {
		langs = s.languages
	}
	codes := make([]string, len(langs))
	for i, l := range langs {
		codes[i] = subdlLang(l)
	}

	p := url.Values{"api_key": {s.apiKey}, "languages": {strings.Join(codes, ",")}, "subs_per_page": {"30"}}
	episode := q.Season > 0 || q.Episode > 0
	switch {
	case q.IMDbID != "":
		p.Set("imdb_id", q.IMDbID)
	case q.TMDbID != "":
		p.Set("tmdb_id", q.TMDbID)
	case strings.TrimSpace(q.Query) != "":
		p.Set("film_name", CleanQuery(q.Query))
	default:
		return nil, fmt.Errorf("subdl searches by ID or title, not by hash: %w", scout.ErrNotFound)
	}
	if episode {
		p.Set("type", "tv")
		if q.Season > 0 {
			p.Set("season_number", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			p.Set("episode_number", strconv.Itoa(q.Episode))
		}
	} else if q.IMDbID != "" || q.TMDbID != "" {
		p.Set("type", "movie")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"?"+p.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "scout/1.0")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subdl: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var r subdlResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("subdl: HTTP %d, unreadable response", resp.StatusCode)
	}
	if !r.Status {
		msg := strings.ToLower(r.Error + " " + r.Message)
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || strings.Contains(msg, "not_authorized") || strings.Contains(msg, "api key"):
			return nil, fmt.Errorf("subdl: rejected the API key (%s): %w", strings.TrimSpace(r.Error+" "+r.Message), scout.ErrBadRequest)
		case strings.Contains(msg, "can't find") || strings.Contains(msg, "not found") || strings.Contains(msg, "no subtitles"):
			return nil, fmt.Errorf("subdl: %s: %w", strings.TrimSpace(r.Error), scout.ErrNotFound)
		case resp.StatusCode == http.StatusTooManyRequests:
			return nil, fmt.Errorf("subdl: rate limited")
		}
		return nil, fmt.Errorf("subdl: HTTP %d %s", resp.StatusCode, strings.TrimSpace(r.Error+" "+r.Message))
	}

	var out []scout.Subtitle
	for _, sub := range r.Subtitles {
		id := strings.TrimSuffix(path.Base(sub.URL), ".zip")
		if !subdlIDRe.MatchString(id) {
			continue
		}
		// Season packs are fine as long as they cover the episode.
		if episode && sub.Episode != nil && *sub.Episode != 0 && *sub.Episode != q.Episode && !sub.FullSeason {
			continue
		}
		if episode && sub.Season != 0 && q.Season != 0 && sub.Season != q.Season {
			continue
		}
		matched := "query"
		if q.IMDbID != "" || q.TMDbID != "" {
			matched = "id"
		}
		out = append(out, scout.Subtitle{
			Provider:        "subdl",
			FileID:          episodeHint(id, q.Season, q.Episode),
			Language:        fromSubdlLang(first(sub.Language, sub.Lang)),
			FileName:        sub.Name,
			Release:         sub.ReleaseName,
			Uploader:        sub.Author,
			HearingImpaired: sub.HI,
			MatchedBy:       matched,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("subdl: no subtitles found: %w", scout.ErrNotFound)
	}
	return out, nil
}

func (s *SubDL) Download(ctx context.Context, fileID string) (*scout.SubtitleFile, error) {
	id, season, episode := splitHint(fileID)
	if !subdlIDRe.MatchString(id) {
		return nil, fmt.Errorf("subdl: invalid file id: %w", scout.ErrBadRequest)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.dlBase+id+".zip", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "scout/1.0")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subdl: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("subdl: file %s: %w", id, scout.ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subdl: download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("subdl: %w", err)
	}
	name, srt, err := extractSRT(data, season, episode)
	if err != nil {
		return nil, fmt.Errorf("subdl: %w", err)
	}
	return &scout.SubtitleFile{FileName: first(name, id+".srt"), Data: srt}, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
