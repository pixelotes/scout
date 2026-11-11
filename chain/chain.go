// Package chain tries several backends in order. It moves to the next one
// only on failures (timeouts, connection errors, 5xx), never on answers like
// "not found" or "quota exceeded", which the next backend would repeat at the
// cost of more calls. A backend that keeps failing is skipped for a while.
package chain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pixelotes/scout"
)

// Member is one backend in the chain.
type Member struct {
	Name    string
	Backend scout.Backend
	// MaxFailures in a row open the breaker for Cooldown. 0 disables it.
	MaxFailures int
	Cooldown    time.Duration
}

// Chain implements scout.Backend.
type Chain struct {
	members []*member
	log     *slog.Logger
	now     func() time.Time
}

type member struct {
	Member
	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func New(log *slog.Logger, members ...Member) *Chain {
	if log == nil {
		log = slog.Default()
	}
	c := &Chain{log: log, now: time.Now}
	for _, m := range members {
		c.members = append(c.members, &member{Member: m})
	}
	return c
}

func (m *member) available(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.MaxFailures == 0 || now.After(m.openUntil)
}

func (m *member) record(ok bool, now time.Time) (opened bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.failures = 0
		return false
	}
	m.failures++
	if m.MaxFailures > 0 && m.failures >= m.MaxFailures {
		m.failures = 0
		m.openUntil = now.Add(m.Cooldown)
		return true
	}
	return false
}

// run calls fn on each available member until one answers. If every member
// is skipped by its breaker, the last one is tried anyway rather than failing
// without trying.
func run[T any](ctx context.Context, c *Chain, op string, fn func(scout.Backend) (T, error)) (T, error) {
	var zero T
	var errs []error
	now := c.now()
	var candidates []*member
	for _, m := range c.members {
		if m.available(now) {
			candidates = append(candidates, m)
		} else {
			c.log.Debug("backend skipped (breaker open)", "backend", m.Name, "op", op)
		}
	}
	if len(candidates) == 0 && len(c.members) > 0 {
		candidates = c.members[len(c.members)-1:]
	}
	for i, m := range candidates {
		v, err := fn(m.Backend)
		if err == nil || scout.IsAnswer(err) {
			m.record(true, c.now())
			if i > 0 {
				c.log.Info("answered by fallback backend", "backend", m.Name, "op", op)
			}
			return v, err
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if m.record(false, c.now()) {
			c.log.Warn("backend failing; skipping it for a while", "backend", m.Name, "cooldown", m.Cooldown)
		}
		c.log.Warn("backend failed", "backend", m.Name, "op", op, "err", err)
		errs = append(errs, fmt.Errorf("%s: %w", m.Name, err))
	}
	if len(errs) == 0 {
		return zero, errors.New("chain: no backends configured")
	}
	return zero, errors.Join(errs...)
}

func (c *Chain) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	return run(ctx, c, "search", func(b scout.Backend) ([]scout.Item, error) { return b.Search(ctx, q) })
}

func (c *Chain) Get(ctx context.Context, l scout.Lookup) (*scout.Item, error) {
	return run(ctx, c, "get", func(b scout.Backend) (*scout.Item, error) { return b.Get(ctx, l) })
}

func (c *Chain) SearchSubtitles(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	return run(ctx, c, "subtitles.search", func(b scout.Backend) ([]scout.Subtitle, error) { return b.SearchSubtitles(ctx, q) })
}

func (c *Chain) DownloadSubtitle(ctx context.Context, provider, fileID string) (*scout.SubtitleFile, error) {
	return run(ctx, c, "subtitles.download", func(b scout.Backend) (*scout.SubtitleFile, error) {
		return b.DownloadSubtitle(ctx, provider, fileID)
	})
}

// Quota asks the first backend that answers.
func (c *Chain) Quota(ctx context.Context) ([]scout.Quota, error) {
	return run(ctx, c, "quota", func(b scout.Backend) ([]scout.Quota, error) { return b.Quota(ctx) })
}
