package subtitles

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/pixelotes/scout"
)

// Provider is one subtitle source.
type Provider interface {
	Name() string
	// Search returns candidates for q. A provider that cannot help (no
	// language it covers, nothing to search by) returns scout.ErrNotFound.
	Search(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error)
	// Download fetches a file ID from one of its own search results.
	Download(ctx context.Context, fileID string) (*scout.SubtitleFile, error)
	// Quota reports the download allowance, when the provider has one.
	Quota() scout.Quota
}

// Multi searches several providers at once and merges their candidates, so a
// language one provider lacks can come from another. Downloads go to the
// provider that returned the candidate.
type Multi struct {
	providers []Provider
	byName    map[string]Provider
}

func NewMulti(providers ...Provider) *Multi {
	m := &Multi{byName: map[string]Provider{}}
	for _, p := range providers {
		if p == nil {
			continue
		}
		m.providers = append(m.providers, p)
		m.byName[p.Name()] = p
	}
	return m
}

// Names lists the providers in order.
func (m *Multi) Names() []string {
	out := make([]string, len(m.providers))
	for i, p := range m.providers {
		out[i] = p.Name()
	}
	return out
}

// Search asks every provider in parallel. It fails only when every provider
// failed; "not found" from all of them is scout.ErrNotFound.
func (m *Multi) Search(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	if len(m.providers) == 0 {
		return nil, fmt.Errorf("no subtitle provider configured: %w", scout.ErrUnsupported)
	}
	type res struct {
		subs []scout.Subtitle
		err  error
	}
	results := make([]res, len(m.providers))
	var wg sync.WaitGroup
	for i, p := range m.providers {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			subs, err := p.Search(ctx, q)
			results[i] = res{subs, err}
		}(i, p)
	}
	wg.Wait()

	var all []scout.Subtitle
	var errs []error
	allNotFound := true
	for i, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.providers[i].Name(), r.err))
			if !errors.Is(r.err, scout.ErrNotFound) {
				allNotFound = false
			}
			continue
		}
		all = append(all, r.subs...)
	}
	if len(all) > 0 {
		return all, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if allNotFound {
		return nil, fmt.Errorf("no subtitles found: %w", scout.ErrNotFound)
	}
	// Flatten: a mix of "not found" and real failures must stay a failure,
	// so a chain still tries its next backend.
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return nil, errors.New(strings.Join(msgs, "; "))
}

// Download routes to the named provider.
func (m *Multi) Download(ctx context.Context, provider, fileID string) (*scout.SubtitleFile, error) {
	if provider == "" && len(m.providers) > 0 {
		provider = m.providers[0].Name()
	}
	p, ok := m.byName[provider]
	if !ok {
		return nil, fmt.Errorf("unknown or unconfigured subtitle provider %q: %w", provider, scout.ErrBadRequest)
	}
	return p.Download(ctx, fileID)
}

// Quotas reports every provider that tracks one.
func (m *Multi) Quotas() []scout.Quota {
	var out []scout.Quota
	for _, p := range m.providers {
		out = append(out, p.Quota())
	}
	return out
}

// episodeHint appends the season and episode to a file ID, so that Download
// can pick the right file out of a season pack: "123_s01e02".
func episodeHint(id string, season, episode int) string {
	if season == 0 && episode == 0 {
		return id
	}
	return fmt.Sprintf("%s_s%02de%02d", id, season, episode)
}

// splitHint undoes episodeHint.
func splitHint(fileID string) (id string, season, episode int) {
	i := strings.LastIndex(fileID, "_s")
	if i < 0 {
		return fileID, 0, 0
	}
	rest := fileID[i+2:]
	j := strings.IndexByte(rest, 'e')
	if j < 0 {
		return fileID, 0, 0
	}
	s, err1 := strconv.Atoi(rest[:j])
	e, err2 := strconv.Atoi(rest[j+1:])
	if err1 != nil || err2 != nil {
		return fileID, 0, 0
	}
	return fileID[:i], s, e
}

// wantsLang reports whether lang is among langs (case-insensitive; "es"
// also matches "es-419" style variants).
func wantsLang(langs []string, lang string) bool {
	for _, l := range langs {
		l = strings.ToLower(l)
		if l == lang || strings.HasPrefix(l, lang+"-") {
			return true
		}
	}
	return false
}
