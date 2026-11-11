package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pixelotes/scout"
	"github.com/pixelotes/scout/server"
)

type stub struct{}

func (stub) Search(_ context.Context, q scout.Query) ([]scout.Item, error) {
	if q.Title == "missing" {
		return nil, fmt.Errorf("nothing: %w", scout.ErrNotFound)
	}
	if q.Title == "boom" {
		return nil, errors.New("provider exploded")
	}
	return []scout.Item{{Kind: q.Kind, Title: q.Title, Year: q.Year, IDs: scout.IDs{"imdb": "tt1"}}}, nil
}
func (stub) Get(_ context.Context, l scout.Lookup) (*scout.Item, error) {
	return &scout.Item{Provider: l.Provider, ID: l.ID, Kind: l.Kind}, nil
}
func (stub) SearchSubtitles(_ context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	return []scout.Subtitle{{FileID: "7", Language: q.Languages[0], MatchedBy: "hash"}}, nil
}
func (stub) DownloadSubtitle(_ context.Context, p, id string) (*scout.SubtitleFile, error) {
	if id == "0" {
		return nil, scout.ErrQuotaExceeded
	}
	return &scout.SubtitleFile{FileName: "Película ñ.srt", Data: []byte("1\n00:00:01,000 --> 00:00:02,000\nhola\n")}, nil
}
func (stub) Quota(context.Context) ([]scout.Quota, error) {
	return []scout.Quota{{Provider: "opensubtitles", Remaining: 3, Known: true}}, nil
}

func TestRoundTrip(t *testing.T) {
	ts := httptest.NewServer(server.Handler(stub{}, "secret", nil))
	defer ts.Close()
	c := New(ts.URL, "secret", 5*time.Second)
	ctx := context.Background()

	items, err := c.Search(ctx, scout.Query{Kind: scout.Movie, Title: "Dune", Year: 2021})
	if err != nil || items[0].Title != "Dune" || items[0].Year != 2021 || items[0].IDs["imdb"] != "tt1" {
		t.Fatalf("search: %+v %v", items, err)
	}
	if _, err := c.Search(ctx, scout.Query{Title: "missing"}); !errors.Is(err, scout.ErrNotFound) || !scout.IsAnswer(err) {
		t.Fatalf("not found should map back: %v", err)
	}
	if _, err := c.Search(ctx, scout.Query{Title: "boom"}); err == nil || scout.IsAnswer(err) {
		t.Fatalf("upstream failure must not be an answer: %v", err)
	}
	it, err := c.Get(ctx, scout.Lookup{Provider: "tvmaze", Kind: scout.Show, ID: "44933"})
	if err != nil || it.ID != "44933" || it.Provider != "tvmaze" {
		t.Fatalf("get: %+v %v", it, err)
	}
	subs, err := c.SearchSubtitles(ctx, scout.SubtitleQuery{Hash: "abc", Languages: []string{"es"}})
	if err != nil || subs[0].Language != "es" {
		t.Fatalf("subs: %+v %v", subs, err)
	}
	f, err := c.DownloadSubtitle(ctx, "opensubtitles", "7")
	if err != nil || f.FileName != "Película ñ.srt" || len(f.Data) == 0 {
		t.Fatalf("download: %+v %v", f, err)
	}
	if _, err := c.DownloadSubtitle(ctx, "", "0"); !errors.Is(err, scout.ErrQuotaExceeded) {
		t.Fatalf("quota should map back: %v", err)
	}
	if q, err := c.Quota(ctx); err != nil || q[0].Remaining != 3 {
		t.Fatalf("quota: %+v %v", q, err)
	}
}

func TestAuth(t *testing.T) {
	ts := httptest.NewServer(server.Handler(stub{}, "secret", nil))
	defer ts.Close()
	_, err := New(ts.URL, "wrong", time.Second).Quota(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Status != 401 || scout.IsAnswer(err) {
		t.Fatalf("want 401 failure, got %v", err)
	}
}

func TestServiceDown(t *testing.T) {
	_, err := New("http://127.0.0.1:1", "", time.Second).Quota(context.Background())
	if err == nil || scout.IsAnswer(err) {
		t.Fatalf("connection error must be a failure: %v", err)
	}
}
