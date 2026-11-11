// Package metadata holds the metadata providers and the Registry that tries
// them in order.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pixelotes/scout"
)

// Provider is one metadata source.
type Provider interface {
	Name() string
	Supports(k scout.Kind) bool
	// Search returns candidates, best first, without episode lists.
	Search(ctx context.Context, q scout.Query) ([]scout.Item, error)
	// Get returns full details, episodes included, for one of its own IDs.
	Get(ctx context.Context, k scout.Kind, id, lang string) (*scout.Item, error)
}

const userAgent = "scout/1.0 (+https://github.com/pixelotes/scout)"

// httpError is a non-200 response. 404 maps to scout.ErrNotFound; 4xx other
// than 429 maps to scout.ErrBadRequest; the rest are failures worth retrying
// elsewhere.
type httpError struct {
	Provider string
	Status   int
	Body     string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.Provider, e.Status, e.Body)
}

func (e *httpError) Unwrap() error {
	switch {
	case e.Status == http.StatusNotFound:
		return scout.ErrNotFound
	case e.Status >= 400 && e.Status < 500 && e.Status != http.StatusTooManyRequests:
		return scout.ErrBadRequest
	}
	return nil
}

type httpDoer struct {
	name    string
	client  *http.Client
	headers map[string]string
}

func newDoer(name string, timeout time.Duration, headers map[string]string) httpDoer {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return httpDoer{name: name, client: &http.Client{Timeout: timeout}, headers: headers}
}

func (d httpDoer) do(ctx context.Context, method, url string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range d.headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", d.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return &httpError{Provider: d.name, Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decoding response: %w", d.name, err)
	}
	return nil
}

func (d httpDoer) get(ctx context.Context, url string, out any) error {
	return d.do(ctx, http.MethodGet, url, nil, out)
}

func notFound(provider, title string) error {
	return fmt.Errorf("%s: no results for %q: %w", provider, title, scout.ErrNotFound)
}

func yearOf(date string) int {
	if len(date) >= 4 {
		var y int
		if _, err := fmt.Sscanf(date[:4], "%d", &y); err == nil {
			return y
		}
	}
	return 0
}

func limit(q scout.Query) int {
	if q.Limit <= 0 {
		return 5
	}
	return q.Limit
}

// stripHTML removes tags and collapses the <br> line breaks some providers use.
func stripHTML(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n").Replace(s)
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// Registry holds the configured providers and the default order per kind.
type Registry struct {
	providers map[string]Provider
	order     map[scout.Kind][]string
}

// NewRegistry builds a registry. order lists provider names per kind; names
// without a registered provider are skipped at lookup time.
func NewRegistry(order map[scout.Kind][]string, providers ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}, order: order}
	for _, p := range providers {
		r.providers[p.Name()] = p
	}
	return r
}

// Provider returns a provider by name.
func (r *Registry) Provider(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Search tries providers in order and returns the first non-empty result.
// Not-found answers move on to the next provider; if every provider fails,
// the errors are joined.
func (r *Registry) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	if !q.Kind.Valid() || strings.TrimSpace(q.Title) == "" {
		return nil, fmt.Errorf("search needs a valid kind and a title: %w", scout.ErrBadRequest)
	}
	names := q.Providers
	if len(names) == 0 {
		names = r.order[q.Kind]
	}
	var errs []error
	tried := 0
	for _, name := range names {
		p, ok := r.providers[name]
		if !ok || !p.Supports(q.Kind) {
			continue
		}
		tried++
		items, err := p.Search(ctx, q)
		if err == nil && len(items) > 0 {
			return items, nil
		}
		if err == nil {
			err = notFound(name, q.Title)
		}
		errs = append(errs, err)
	}
	if tried == 0 {
		return nil, fmt.Errorf("no configured provider supports %s: %w", q.Kind, scout.ErrUnsupported)
	}
	return nil, joinErrs(errs)
}

// Get fetches full details from the named provider.
func (r *Registry) Get(ctx context.Context, l scout.Lookup) (*scout.Item, error) {
	p, ok := r.providers[l.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown or unconfigured provider %q: %w", l.Provider, scout.ErrBadRequest)
	}
	if !p.Supports(l.Kind) {
		return nil, fmt.Errorf("%s does not handle %s: %w", l.Provider, l.Kind, scout.ErrUnsupported)
	}
	return p.Get(ctx, l.Kind, l.ID, l.Language)
}

// joinErrs keeps ErrNotFound visible only when every error was a not-found,
// so a mix of "nothing found" and "provider down" stays a failure.
func joinErrs(errs []error) error {
	all := true
	for _, e := range errs {
		if !errors.Is(e, scout.ErrNotFound) {
			all = false
		}
	}
	joined := errors.Join(errs...)
	if all {
		return joined
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return errors.New(strings.Join(msgs, "; "))
}
