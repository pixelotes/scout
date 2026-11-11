// Package scout holds the shared types and the Backend interface for media
// metadata and subtitle lookups. A Backend is either local (providers called
// in-process), remote (a scout server over HTTP) or a chain of both.
package scout

import (
	"context"
	"errors"
	"strings"
)

// Kind is the type of media being looked up.
type Kind string

const (
	Movie Kind = "movie"
	Show  Kind = "show"
	Anime Kind = "anime"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return k == Movie || k == Show || k == Anime }

// IDs maps an external database to its ID: imdb, tmdb, tvdb, tvmaze, anilist,
// kitsu, trakt. Values are strings so "tt0133093" and "603" fit alike.
type IDs map[string]string

// Item is a movie, show or anime from any provider. Fields a provider does not
// offer stay empty.
type Item struct {
	Kind        Kind              `json:"kind"`
	Provider    string            `json:"provider"`
	ID          string            `json:"id"` // the provider's own ID
	IDs         IDs               `json:"ids,omitempty"`
	Title       string            `json:"title"`
	Original    string            `json:"original_title,omitempty"`
	Year        int               `json:"year,omitempty"`
	Overview    string            `json:"overview,omitempty"`
	PosterURL   string            `json:"poster_url,omitempty"`
	BackdropURL string            `json:"backdrop_url,omitempty"`
	LogoURL     string            `json:"logo_url,omitempty"`
	Rating      float64           `json:"rating,omitempty"`
	Genres      []string          `json:"genres,omitempty"`
	Runtime     int               `json:"runtime,omitempty"` // minutes
	Studio      string            `json:"studio,omitempty"`
	Tagline     string            `json:"tagline,omitempty"`
	Status      string            `json:"status,omitempty"`
	Seasons     map[int][]Episode `json:"seasons,omitempty"`
}

// Episode is one episode of a show.
type Episode struct {
	Number  int    `json:"number"`
	Title   string `json:"title,omitempty"`
	AirDate string `json:"air_date,omitempty"`
	Runtime int    `json:"runtime,omitempty"`
	Plot    string `json:"plot,omitempty"`
}

// Query asks for candidates matching a title.
type Query struct {
	Kind      Kind     `json:"kind"`
	Title     string   `json:"title"`
	Year      int      `json:"year,omitempty"`
	Language  string   `json:"language,omitempty"`  // empty: backend default
	Providers []string `json:"providers,omitempty"` // empty: backend default order for Kind
	Limit     int      `json:"limit,omitempty"`     // empty: 5
}

// Lookup fetches full details (episodes included) for one provider ID.
type Lookup struct {
	Kind     Kind   `json:"kind"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Language string `json:"language,omitempty"`
}

// SubtitleQuery describes the video to find subtitles for. The caller computes
// Hash (see package moviehash), so the backend never needs the file itself.
// Search order: hash, then IDs, then Query.
type SubtitleQuery struct {
	Hash      string   `json:"hash,omitempty"`
	Size      int64    `json:"size,omitempty"`
	IMDbID    string   `json:"imdb_id,omitempty"`
	TMDbID    string   `json:"tmdb_id,omitempty"`
	Season    int      `json:"season,omitempty"`
	Episode   int      `json:"episode,omitempty"`
	Query     string   `json:"query,omitempty"`
	Languages []string `json:"languages,omitempty"` // empty: backend default
}

// Subtitle is one candidate file.
type Subtitle struct {
	Provider        string  `json:"provider"`
	FileID          string  `json:"file_id"`
	Language        string  `json:"language"`
	FileName        string  `json:"file_name,omitempty"`
	Release         string  `json:"release,omitempty"`
	Uploader        string  `json:"uploader,omitempty"`
	Downloads       int     `json:"downloads,omitempty"`
	Rating          float64 `json:"rating,omitempty"`
	FPS             float64 `json:"fps,omitempty"`
	HD              bool    `json:"hd,omitempty"`
	HearingImpaired bool    `json:"hearing_impaired,omitempty"`
	HashMatch       bool    `json:"hash_match,omitempty"`
	Trusted         bool    `json:"trusted,omitempty"`
	MachineMade     bool    `json:"machine_translated,omitempty"`
	MatchedBy       string  `json:"matched_by,omitempty"` // hash | id | query
}

// SubtitleFile is a downloaded subtitle.
type SubtitleFile struct {
	FileName string `json:"file_name"`
	Data     []byte `json:"-"`
}

// Quota is the provider's download allowance, when it reports one.
type Quota struct {
	Provider  string `json:"provider"`
	Remaining int    `json:"remaining"`
	ResetsAt  string `json:"resets_at,omitempty"` // RFC 3339
	Known     bool   `json:"known"`
}

// Backend is what reel and rms talk to.
type Backend interface {
	Search(ctx context.Context, q Query) ([]Item, error)
	Get(ctx context.Context, l Lookup) (*Item, error)
	SearchSubtitles(ctx context.Context, q SubtitleQuery) ([]Subtitle, error)
	DownloadSubtitle(ctx context.Context, provider, fileID string) (*SubtitleFile, error)
	Quota(ctx context.Context) ([]Quota, error)
}

// Errors that are answers, not failures: a Chain does not fall back on them.
var (
	ErrNotFound      = errors.New("not found")
	ErrBadRequest    = errors.New("bad request")
	ErrQuotaExceeded = errors.New("download quota exceeded")
	ErrUnsupported   = errors.New("not supported by this provider")
)

// IsAnswer reports whether err is a definitive answer that another backend
// would give too, so falling back would only waste calls and quota.
func IsAnswer(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrBadRequest) ||
		errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrUnsupported)
}

// BestSubtitle picks the candidate to download for lang: a hash match first,
// then trusted uploaders, then the most downloaded. Machine translations
// only win when nothing else exists. Returns nil when lang has no candidate.
func BestSubtitle(subs []Subtitle, lang string) *Subtitle {
	lang = strings.ToLower(lang)
	score := func(s *Subtitle) int {
		n := min(s.Downloads, 1_000_000)
		if s.HashMatch {
			n += 10_000_000
		}
		if s.Trusted {
			n += 2_000_000
		}
		if s.MachineMade {
			n -= 50_000_000
		}
		return n
	}
	var best *Subtitle
	for i := range subs {
		s := &subs[i]
		l := strings.ToLower(s.Language)
		if l != lang && !strings.HasPrefix(l, lang+"-") {
			continue
		}
		if best == nil || score(s) > score(best) {
			best = s
		}
	}
	return best
}
