// Package local is the in-process Backend: providers called directly, with a
// metadata cache in memory and a subtitle cache on disk.
package local

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/pixelotes/scout"
	"github.com/pixelotes/scout/config"
	"github.com/pixelotes/scout/metadata"
	"github.com/pixelotes/scout/subtitles"
)

// Backend implements scout.Backend with local providers.
type Backend struct {
	reg    *metadata.Registry
	subs   *subtitles.OpenSubtitles
	lang   string
	cache  *ttlCache
	subDir string
}

// New builds the local backend. Providers that need a key are only registered
// when the key is set, so the order lists can name them unconditionally.
func New(c config.Local) *Backend {
	providers := []metadata.Provider{
		metadata.NewIMDb(c.Timeout),
		metadata.NewTVMaze(c.Timeout),
		metadata.NewAniList(c.Timeout),
		metadata.NewKitsu(c.Timeout),
	}
	if c.TMDB.APIKey != "" {
		providers = append(providers, metadata.NewTMDB(c.TMDB.APIKey, c.Language, c.Timeout))
	}
	if c.Trakt.ClientID != "" {
		providers = append(providers, metadata.NewTrakt(c.Trakt.ClientID, c.Timeout))
	}
	b := &Backend{
		reg: metadata.NewRegistry(c.Providers, providers...),
		subs: subtitles.NewOpenSubtitles(subtitles.OpenSubtitlesConfig{
			APIKey: c.OpenSubtitles.APIKey, Username: c.OpenSubtitles.Username,
			Password: c.OpenSubtitles.Password, Languages: c.SubtitleLanguages, Timeout: c.Timeout,
		}),
		lang:   c.Language,
		subDir: c.Cache.SubtitleDir,
	}
	if c.Cache.MetadataTTL > 0 {
		b.cache = newTTLCache(c.Cache.MetadataTTL, c.Cache.MaxEntries)
	}
	return b
}

func (b *Backend) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	q.Language = orDefault(q.Language, b.lang)
	key := cacheKey("search", q)
	if v, ok := b.cache.get(key); ok {
		return v.([]scout.Item), nil
	}
	items, err := b.reg.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	b.cache.put(key, items)
	return items, nil
}

func (b *Backend) Get(ctx context.Context, l scout.Lookup) (*scout.Item, error) {
	l.Language = orDefault(l.Language, b.lang)
	key := cacheKey("get", l)
	if v, ok := b.cache.get(key); ok {
		return v.(*scout.Item), nil
	}
	item, err := b.reg.Get(ctx, l)
	if err != nil {
		return nil, err
	}
	b.cache.put(key, item)
	return item, nil
}

func (b *Backend) SearchSubtitles(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	return b.subs.Search(ctx, q)
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// DownloadSubtitle serves from the disk cache when it can, so the same file is
// never charged against the quota twice.
func (b *Backend) DownloadSubtitle(ctx context.Context, provider, fileID string) (*scout.SubtitleFile, error) {
	if provider != "" && provider != b.subs.Name() {
		return nil, fmt.Errorf("unknown subtitle provider %q: %w", provider, scout.ErrBadRequest)
	}
	if !safeID.MatchString(fileID) {
		return nil, fmt.Errorf("invalid file id: %w", scout.ErrBadRequest)
	}
	var dataPath, namePath string
	if b.subDir != "" {
		dir := filepath.Join(b.subDir, b.subs.Name())
		dataPath, namePath = filepath.Join(dir, fileID), filepath.Join(dir, fileID+".name")
		if data, err := os.ReadFile(dataPath); err == nil {
			name, _ := os.ReadFile(namePath)
			return &scout.SubtitleFile{FileName: orDefault(string(name), fileID+".srt"), Data: data}, nil
		}
	}
	f, err := b.subs.Download(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if dataPath != "" {
		if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err == nil {
			_ = writeAtomic(dataPath, f.Data)
			_ = writeAtomic(namePath, []byte(f.FileName))
		}
	}
	return f, nil
}

func (b *Backend) Quota(context.Context) ([]scout.Quota, error) {
	return []scout.Quota{b.subs.Quota()}, nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func cacheKey(op string, v any) string {
	b, _ := json.Marshal(v)
	h := sha1.Sum(append([]byte(op+":"), b...))
	return hex.EncodeToString(h[:])
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ttlCache is a small map with expiry. When full, it drops expired entries
// first and then the oldest. A nil cache is a no-op.
type ttlCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	data map[string]entry
}

type entry struct {
	v   any
	exp time.Time
}

func newTTLCache(ttl time.Duration, max int) *ttlCache {
	if max <= 0 {
		max = 5000
	}
	return &ttlCache{ttl: ttl, max: max, data: map[string]entry{}}
}

func (c *ttlCache) get(k string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[k]
	if !ok || time.Now().After(e.exp) {
		delete(c.data, k)
		return nil, false
	}
	return e.v, true
}

func (c *ttlCache) put(k string, v any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.data) >= c.max {
		now := time.Now()
		var oldK string
		var oldT time.Time
		for key, e := range c.data {
			if now.After(e.exp) {
				delete(c.data, key)
				continue
			}
			if oldK == "" || e.exp.Before(oldT) {
				oldK, oldT = key, e.exp
			}
		}
		if len(c.data) >= c.max {
			delete(c.data, oldK)
		}
	}
	c.data[k] = entry{v: v, exp: time.Now().Add(c.ttl)}
}
