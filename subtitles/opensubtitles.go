// Package subtitles holds the subtitle providers. For now: OpenSubtitles.
package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pixelotes/scout"
)

// OpenSubtitles talks to the api.opensubtitles.com REST API (v1).
//
// Searching only needs the API key. Downloads count against a daily quota:
// with only a key it is small; with Username/Password it is the account's.
// The client remembers the remaining count the API reports after each
// download and refuses further downloads once it hits zero, until the reset
// time, so a scan does not keep hammering the API.
type OpenSubtitles struct {
	apiKey    string
	username  string
	password  string
	userAgent string
	languages []string
	base      string
	http      *http.Client

	mu        sync.Mutex
	token     string
	tokenAt   time.Time
	lastCall  time.Time
	remaining int
	resetAt   time.Time
	known     bool
}

// OpenSubtitlesConfig configures the client. APIKey is required.
type OpenSubtitlesConfig struct {
	APIKey    string
	Username  string
	Password  string
	UserAgent string // OpenSubtitles asks for "AppName vX.Y"
	Languages []string
	Timeout   time.Duration
}

func NewOpenSubtitles(c OpenSubtitlesConfig) *OpenSubtitles {
	if c.Timeout <= 0 {
		c.Timeout = 15 * time.Second
	}
	if len(c.Languages) == 0 {
		c.Languages = []string{"en"}
	}
	return &OpenSubtitles{
		apiKey: c.APIKey, username: c.Username, password: c.Password,
		userAgent: orDefault(c.UserAgent, "scout v1.0"),
		languages: c.Languages,
		base:      "https://api.opensubtitles.com/api/v1",
		http:      &http.Client{Timeout: c.Timeout},
	}
}

func (o *OpenSubtitles) Name() string { return "opensubtitles" }

// throttle keeps calls at least 250 ms apart (the API allows ~5/s).
func (o *OpenSubtitles) throttle(ctx context.Context) error {
	o.mu.Lock()
	wait := time.Until(o.lastCall.Add(250 * time.Millisecond))
	o.lastCall = time.Now().Add(max(wait, 0))
	o.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *OpenSubtitles) do(ctx context.Context, method, path string, body any, auth bool, out any) error {
	if o.apiKey == "" {
		return fmt.Errorf("opensubtitles: API key not configured: %w", scout.ErrUnsupported)
	}
	if err := o.throttle(ctx); err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Api-Key", o.apiKey)
	req.Header.Set("User-Agent", o.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		if tok, err := o.login(ctx); err != nil {
			return err
		} else if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return fmt.Errorf("opensubtitles: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusOK {
		return json.Unmarshal(raw, out)
	}

	msg := strings.TrimSpace(string(raw))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	err = fmt.Errorf("opensubtitles: HTTP %d %s", resp.StatusCode, msg)
	switch {
	case resp.StatusCode == http.StatusNotAcceptable || (resp.StatusCode == http.StatusTooManyRequests && strings.Contains(msg, "download")):
		// 406 is how /download says the daily quota is used up.
		o.noteQuota(0, parseReset(raw))
		return fmt.Errorf("%w (%v)", scout.ErrQuotaExceeded, err)
	case resp.StatusCode == http.StatusUnauthorized:
		o.mu.Lock()
		o.token = ""
		o.mu.Unlock()
		return err // could be a stale token: worth a retry elsewhere
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w (%v)", scout.ErrNotFound, err)
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
		return fmt.Errorf("%w (%v)", scout.ErrBadRequest, err)
	}
	return err
}

// login returns a user token when credentials are set, caching it for 12 h.
func (o *OpenSubtitles) login(ctx context.Context) (string, error) {
	if o.username == "" {
		return "", nil
	}
	o.mu.Lock()
	if o.token != "" && time.Since(o.tokenAt) < 12*time.Hour {
		t := o.token
		o.mu.Unlock()
		return t, nil
	}
	o.mu.Unlock()
	var resp struct {
		Token string `json:"token"`
	}
	if err := o.do(ctx, http.MethodPost, "/login", map[string]string{"username": o.username, "password": o.password}, false, &resp); err != nil {
		return "", fmt.Errorf("opensubtitles login: %w", err)
	}
	o.mu.Lock()
	o.token, o.tokenAt = resp.Token, time.Now()
	o.mu.Unlock()
	return resp.Token, nil
}

type osSearch struct {
	Data []struct {
		Attributes struct {
			Language        string  `json:"language"`
			DownloadCount   int     `json:"download_count"`
			HearingImpaired bool    `json:"hearing_impaired"`
			HD              bool    `json:"hd"`
			FPS             float64 `json:"fps"`
			Ratings         float64 `json:"ratings"`
			FromTrusted     bool    `json:"from_trusted"`
			AITranslated    bool    `json:"ai_translated"`
			Machine         bool    `json:"machine_translated"`
			Release         string  `json:"release"`
			MovieHashMatch  bool    `json:"moviehash_match"`
			Uploader        struct {
				Name string `json:"name"`
			} `json:"uploader"`
			Files []struct {
				FileID   int    `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

func (o *OpenSubtitles) search(ctx context.Context, p url.Values, by string) ([]scout.Subtitle, error) {
	var resp osSearch
	if err := o.do(ctx, http.MethodGet, "/subtitles?"+p.Encode(), nil, false, &resp); err != nil {
		return nil, err
	}
	var out []scout.Subtitle
	for _, d := range resp.Data {
		a := d.Attributes
		if len(a.Files) == 0 {
			continue
		}
		out = append(out, scout.Subtitle{
			Provider: "opensubtitles", FileID: strconv.Itoa(a.Files[0].FileID), Language: a.Language,
			FileName: a.Files[0].FileName, Release: a.Release, Uploader: a.Uploader.Name,
			Downloads: a.DownloadCount, Rating: a.Ratings, FPS: a.FPS, HD: a.HD,
			HearingImpaired: a.HearingImpaired, HashMatch: a.MovieHashMatch, Trusted: a.FromTrusted,
			MachineMade: a.AITranslated || a.Machine, MatchedBy: by,
		})
	}
	return out, nil
}

// Search tries the hash, then the IDs, then the text query, and returns the
// first step that finds anything. Only hash results that really matched the
// hash count for the first step; the API pads them with loose matches.
func (o *OpenSubtitles) Search(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	langs := q.Languages
	if len(langs) == 0 {
		langs = o.languages
	}
	base := func() url.Values {
		p := url.Values{"languages": {strings.Join(normLangs(langs), ",")}}
		if q.Season > 0 {
			p.Set("season_number", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			p.Set("episode_number", strconv.Itoa(q.Episode))
		}
		return p
	}
	episode := q.Season > 0 || q.Episode > 0

	var steps []struct {
		by string
		p  url.Values
	}
	if q.Hash != "" {
		p := base()
		p.Set("moviehash", strings.ToLower(q.Hash))
		steps = append(steps, struct {
			by string
			p  url.Values
		}{"hash", p})
	}
	if q.IMDbID != "" || q.TMDbID != "" {
		p := base()
		// For an episode the IDs are the show's, which the API calls parent.
		prefix := ""
		if episode {
			prefix = "parent_"
		}
		if q.IMDbID != "" {
			p.Set(prefix+"imdb_id", strings.TrimLeft(strings.TrimPrefix(q.IMDbID, "tt"), "0"))
		} else {
			p.Set(prefix+"tmdb_id", q.TMDbID)
		}
		steps = append(steps, struct {
			by string
			p  url.Values
		}{"id", p})
	}
	if strings.TrimSpace(q.Query) != "" {
		p := base()
		p.Set("query", CleanQuery(q.Query))
		steps = append(steps, struct {
			by string
			p  url.Values
		}{"query", p})
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("subtitle search needs a hash, an ID or a query: %w", scout.ErrBadRequest)
	}

	var errs []error
	for _, s := range steps {
		subs, err := o.search(ctx, s.p, s.by)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, scout.ErrUnsupported) {
				return nil, err
			}
			errs = append(errs, err)
			continue
		}
		if s.by == "hash" {
			subs = onlyHashMatches(subs)
		}
		if len(subs) > 0 {
			return subs, nil
		}
	}
	if len(errs) == len(steps) {
		return nil, errors.Join(errs...)
	}
	return nil, fmt.Errorf("opensubtitles: no subtitles found: %w", scout.ErrNotFound)
}

func onlyHashMatches(subs []scout.Subtitle) []scout.Subtitle {
	out := subs[:0]
	for _, s := range subs {
		if s.HashMatch {
			out = append(out, s)
		}
	}
	return out
}

// Download fetches one subtitle file.
func (o *OpenSubtitles) Download(ctx context.Context, fileID string) (*scout.SubtitleFile, error) {
	id, err := strconv.Atoi(fileID)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles file IDs are numeric: %w", scout.ErrBadRequest)
	}
	o.mu.Lock()
	exhausted := o.known && o.remaining <= 0 && time.Now().Before(o.resetAt)
	resetAt := o.resetAt
	o.mu.Unlock()
	if exhausted {
		return nil, fmt.Errorf("opensubtitles: resets at %s: %w", resetAt.Format(time.RFC3339), scout.ErrQuotaExceeded)
	}

	var resp struct {
		Link      string `json:"link"`
		FileName  string `json:"file_name"`
		Remaining int    `json:"remaining"`
		ResetUTC  string `json:"reset_time_utc"`
	}
	if err := o.do(ctx, http.MethodPost, "/download", map[string]int{"file_id": id}, true, &resp); err != nil {
		return nil, err
	}
	reset, _ := time.Parse(time.RFC3339, resp.ResetUTC)
	o.noteQuota(resp.Remaining, reset)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resp.Link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", o.userAgent)
	r, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opensubtitles: fetching file: %w", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opensubtitles: fetching file: HTTP %d", r.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	return &scout.SubtitleFile{FileName: orDefault(resp.FileName, fileID+".srt"), Data: data}, nil
}

func (o *OpenSubtitles) noteQuota(remaining int, reset time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.remaining, o.known = remaining, true
	if reset.IsZero() {
		reset = time.Now().Add(time.Hour)
	}
	o.resetAt = reset
}

// Quota reports what the last download said is left.
func (o *OpenSubtitles) Quota() scout.Quota {
	o.mu.Lock()
	defer o.mu.Unlock()
	q := scout.Quota{Provider: "opensubtitles", Remaining: o.remaining, Known: o.known}
	if o.known {
		if time.Now().After(o.resetAt) {
			q.Known = false // reset since: unknown until the next download
		} else {
			q.ResetsAt = o.resetAt.UTC().Format(time.RFC3339)
		}
	}
	return q
}

func parseReset(raw []byte) time.Time {
	var v struct {
		ResetUTC string `json:"reset_time_utc"`
	}
	_ = json.Unmarshal(raw, &v)
	t, _ := time.Parse(time.RFC3339, v.ResetUTC)
	return t
}

// normLangs maps the codes OpenSubtitles spells differently.
func normLangs(langs []string) []string {
	out := make([]string, len(langs))
	for i, l := range langs {
		l = strings.ToLower(strings.TrimSpace(l))
		switch l {
		case "pt":
			l = "pt-pt"
		case "pb", "pt_br":
			l = "pt-br"
		case "zh":
			l = "zh-cn"
		}
		out[i] = l
	}
	return out
}

// CleanQuery turns a file or folder name into a search query: drops the
// extension-like tail after "[", "(" or " - " and turns dots into spaces.
func CleanQuery(name string) string {
	for _, sep := range []string{"[", "(", " - "} {
		if i := strings.Index(name, sep); i > 0 {
			name = name[:i]
		}
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(name, ".", " "), "_", " "))
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
