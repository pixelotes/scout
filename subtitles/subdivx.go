package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pixelotes/scout"
)

// Subdivx serves Spanish subtitles from subdivx.com, which sits behind a
// Cloudflare challenge. A FlareSolverr-compatible service (Parasol or
// FlareSolverr) solves it once; the clearance cookies and user agent it
// returns are then reused for direct requests until Cloudflare asks again.
//
// It is scraping, not an API: the site can change under it. Searches are by
// title (plus SxxEyy for episodes); downloads are ZIP.
type Subdivx struct {
	solverURL string
	base      string
	http      *http.Client
	solver    *http.Client

	mu         sync.Mutex
	session    *sdxSession
	lastCall   time.Time
	lastSearch time.Time
	searchGap  time.Duration
}

type sdxSession struct {
	cookie    string
	userAgent string
	version   string // e.g. "431", from the page's id="vs" element
	at        time.Time
}

// NewSubdivx needs the FlareSolverr endpoint, e.g. http://parasol:8191/v1.
func NewSubdivx(solverURL string, timeout time.Duration) *Subdivx {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if !strings.HasSuffix(strings.TrimRight(solverURL, "/"), "/v1") {
		solverURL = strings.TrimRight(solverURL, "/") + "/v1"
	}
	return &Subdivx{
		solverURL: solverURL,
		base:      "https://subdivx.com",
		http:      &http.Client{Timeout: timeout},
		solver:    &http.Client{Timeout: 100 * time.Second},
		searchGap: 3 * time.Second,
	}
}

func (s *Subdivx) Name() string { return "subdivx" }

func (s *Subdivx) Quota() scout.Quota { return scout.Quota{Provider: "subdivx"} }

var (
	errChallenge = errors.New("subdivx: cloudflare challenge")
	sdxVersionRe = regexp.MustCompile(`id="vs">v(\d+)\.(\d+)<`)
)

// solve asks FlareSolverr for the home page and keeps its cookies.
func (s *Subdivx) solve(ctx context.Context) (*sdxSession, error) {
	body, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": s.base + "/", "maxTimeout": 90000})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.solverURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.solver.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subdivx: challenge solver unreachable: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status    int    `json:"status"`
			Response  string `json:"response"`
			UserAgent string `json:"userAgent"`
			Cookies   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"cookies"`
		} `json:"solution"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("subdivx: challenge solver: %w", err)
	}
	if r.Status != "ok" {
		return nil, fmt.Errorf("subdivx: challenge solver: %s", r.Message)
	}
	m := sdxVersionRe.FindStringSubmatch(r.Solution.Response)
	if m == nil {
		return nil, errors.New("subdivx: page layout changed (no version marker); the provider needs updating")
	}
	var cookies []string
	for _, c := range r.Solution.Cookies {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	return &sdxSession{
		cookie: strings.Join(cookies, "; "), userAgent: r.Solution.UserAgent,
		version: m[1] + m[2], at: time.Now(),
	}, nil
}

// sessionFor returns the cached session, solving the challenge when there is
// none or when fresh is set.
func (s *Subdivx) sessionFor(ctx context.Context, fresh bool) (*sdxSession, error) {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()
	if sess != nil && !fresh {
		return sess, nil
	}
	sess, err := s.solve(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()
	return sess, nil
}

// waitSearch keeps searches searchGap apart: subdivx answers a search that
// comes sooner with an empty result instead of an error.
func (s *Subdivx) waitSearch(ctx context.Context) error {
	s.mu.Lock()
	wait := time.Until(s.lastSearch.Add(s.searchGap))
	s.lastSearch = time.Now().Add(max(wait, 0))
	s.mu.Unlock()
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

var errTooSoon = errors.New("subdivx: searched too soon")

// throttle keeps direct requests at least a second apart; it is a small
// community site.
func (s *Subdivx) throttle(ctx context.Context) error {
	s.mu.Lock()
	wait := time.Until(s.lastCall.Add(time.Second))
	s.lastCall = time.Now().Add(max(wait, 0))
	s.mu.Unlock()
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

func (s *Subdivx) do(ctx context.Context, sess *sdxSession, method, path string, form url.Values) (*http.Response, error) {
	if err := s.throttle(ctx); err != nil {
		return nil, err
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", sess.userAgent)
	req.Header.Set("Cookie", sess.cookie)
	req.Header.Set("Referer", s.base+"/")
	req.Header.Set("Origin", s.base)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subdivx: %w", err)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		resp.Body.Close()
		return nil, errChallenge
	}
	return resp, nil
}

// withSession runs fn, solving the challenge again once if Cloudflare
// rejected the cached clearance.
func (s *Subdivx) withSession(ctx context.Context, fn func(*sdxSession) error) error {
	sess, err := s.sessionFor(ctx, false)
	if err != nil {
		return err
	}
	err = fn(sess)
	if !errors.Is(err, errChallenge) {
		return err
	}
	if sess, err = s.sessionFor(ctx, true); err != nil {
		return err
	}
	return fn(sess)
}

type sdxResult struct {
	ID          flexString `json:"id"`
	Titulo      flexString `json:"titulo"`
	Descripcion flexString `json:"descripcion"`
	Descargas   flexString `json:"descargas"`
	Promedio    flexString `json:"promedio"`
	Nick        flexString `json:"nick"`
	Pack        flexString `json:"pack"`
	Eliminado   flexString `json:"eliminado"`
}

// flexString accepts a JSON string, number, bool or null: the site is not
// consistent about which it sends.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flexString(b)
	return nil
}

var sdxIDRe = regexp.MustCompile(`^[0-9]+$`)

func (s *Subdivx) Search(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	if len(q.Languages) > 0 && !wantsLang(q.Languages, "es") {
		return nil, fmt.Errorf("subdivx only has Spanish: %w", scout.ErrNotFound)
	}
	title := CleanQuery(q.Query)
	if title == "" {
		return nil, fmt.Errorf("subdivx searches by title: %w", scout.ErrNotFound)
	}
	episode := q.Season > 0 || q.Episode > 0
	text := title
	if episode {
		text = fmt.Sprintf("%s S%02dE%02d", title, q.Season, q.Episode)
	} else if y := yearIn(q.Query); y > 0 && yearIn(title) == 0 {
		text = fmt.Sprintf("%s %d", title, y)
	}

	var results []sdxResult
	search := func(sess *sdxSession) error {
		if err := s.waitSearch(ctx); err != nil {
			return err
		}
		resp, err := s.do(ctx, sess, http.MethodGet, "/inc/gt.php?gt=1", nil)
		if err != nil {
			return err
		}
		var tok struct {
			Token string `json:"token"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&tok)
		resp.Body.Close()
		if err != nil || tok.Token == "" {
			return errChallenge // HTML instead of JSON: the clearance expired
		}
		form := url.Values{"tabla": {"resultados"}, "filtros": {""}, "buscar" + sess.version: {text}, "token": {tok.Token}}
		resp, err = s.do(ctx, sess, http.MethodPost, "/inc/ajax.php", form)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var r struct {
			AAData     []sdxResult `json:"aaData"`
			Estrategia *flexString `json:"estrategia"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
			return fmt.Errorf("subdivx: unexpected search response (the site may have changed): %w", err)
		}
		// A real answer always names its search strategy; an empty one
		// without it means the rate limit kicked in.
		if r.Estrategia == nil && len(r.AAData) == 0 {
			return errTooSoon
		}
		results = r.AAData
		return nil
	}
	// Episodes: if "Title SxxEyy" finds nothing, try the title alone and keep
	// season packs (often titled just "Title S01").
	texts := []string{text}
	if episode {
		texts = append(texts, title)
	}
	var out []scout.Subtitle
	for n, t := range texts {
		text, results = t, nil
		err := s.withSession(ctx, search)
		if errors.Is(err, errTooSoon) {
			err = s.withSession(ctx, search) // waitSearch spaces the retry
		}
		if errors.Is(err, errTooSoon) {
			return nil, fmt.Errorf("subdivx: rate limited, try again later")
		}
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			id := string(r.ID)
			if !sdxIDRe.MatchString(id) || r.Eliminado == "1" {
				continue
			}
			label := html.UnescapeString(string(r.Titulo + " " + r.Descripcion))
			if episode && !matchesEpisode(label, q.Season, q.Episode) {
				// A season pack: flagged as one, or (on the title-only
				// search) named for the season without another episode.
				pack := r.Pack == "1" || (n > 0 && !hasEpisode(label))
				if !pack || !matchesSeason(label, q.Season) {
					continue
				}
			}
			downloads, _ := strconv.Atoi(string(r.Descargas))
			rating, _ := strconv.ParseFloat(string(r.Promedio), 64)
			out = append(out, scout.Subtitle{
				Provider:  "subdivx",
				FileID:    episodeHint(id, q.Season, q.Episode),
				Language:  "es",
				FileName:  strings.TrimSpace(html.UnescapeString(string(r.Titulo))),
				Release:   truncate(strings.TrimSpace(html.UnescapeString(string(r.Descripcion))), 200),
				Uploader:  string(r.Nick),
				Downloads: downloads,
				Rating:    rating,
				MatchedBy: "query",
			})
		}
		if len(out) > 0 {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("subdivx: no subtitles found: %w", scout.ErrNotFound)
	}
	return out, nil
}

func (s *Subdivx) Download(ctx context.Context, fileID string) (*scout.SubtitleFile, error) {
	id, season, episode := splitHint(fileID)
	if !sdxIDRe.MatchString(id) {
		return nil, fmt.Errorf("subdivx: invalid file id: %w", scout.ErrBadRequest)
	}
	var data []byte
	err := s.withSession(ctx, func(sess *sdxSession) error {
		resp, err := s.do(ctx, sess, http.MethodGet, "/descargar.php?f=1&id="+id, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("subdivx: download HTTP %d", resp.StatusCode)
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			return errChallenge
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		return err
	})
	if err != nil {
		return nil, err
	}
	name, srt, err := extractSRT(data, season, episode)
	if err != nil {
		return nil, fmt.Errorf("subdivx: %w", err)
	}
	return &scout.SubtitleFile{FileName: first(name, id+".srt"), Data: srt}, nil
}

var seasonRe = regexp.MustCompile(`(?i)\b(?:s|temporada\s*)(\d{1,2})\b`)

// hasEpisode reports whether a label names any specific episode.
func hasEpisode(label string) bool {
	for _, re := range epPatterns {
		if re.MatchString(label) {
			return true
		}
	}
	return false
}

func matchesSeason(label string, season int) bool {
	for _, m := range seasonRe.FindAllStringSubmatch(label, -1) {
		if n, _ := strconv.Atoi(m[1]); n == season {
			return true
		}
	}
	return false
}

var yearRe = regexp.MustCompile(`\b(19|20)\d{2}\b`)

func yearIn(s string) int {
	if m := yearRe.FindString(s); m != "" {
		y, _ := strconv.Atoi(m)
		return y
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
