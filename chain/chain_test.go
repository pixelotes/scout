package chain

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pixelotes/scout"
)

type fake struct {
	name  string
	err   error
	calls int
}

func (f *fake) Search(context.Context, scout.Query) ([]scout.Item, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []scout.Item{{Title: f.name}}, nil
}
func (f *fake) Get(context.Context, scout.Lookup) (*scout.Item, error) { return nil, f.err }
func (f *fake) SearchSubtitles(context.Context, scout.SubtitleQuery) ([]scout.Subtitle, error) {
	return nil, f.err
}
func (f *fake) DownloadSubtitle(context.Context, string, string) (*scout.SubtitleFile, error) {
	f.calls++
	return nil, f.err
}
func (f *fake) Quota(context.Context) ([]scout.Quota, error) { return nil, f.err }

func TestFallsBackOnFailure(t *testing.T) {
	svc := &fake{name: "service", err: errors.New("connection refused")}
	loc := &fake{name: "local"}
	c := New(nil, Member{Name: "service", Backend: svc}, Member{Name: "local", Backend: loc})
	items, err := c.Search(context.Background(), scout.Query{})
	if err != nil || items[0].Title != "local" {
		t.Fatalf("got %v, %v", items, err)
	}
}

func TestAnswersDoNotFallBack(t *testing.T) {
	for _, answer := range []error{scout.ErrNotFound, scout.ErrQuotaExceeded, scout.ErrBadRequest} {
		svc := &fake{err: fmt.Errorf("wrapped: %w", answer)}
		loc := &fake{}
		c := New(nil, Member{Name: "service", Backend: svc}, Member{Name: "local", Backend: loc})
		if _, err := c.DownloadSubtitle(context.Background(), "", "1"); !errors.Is(err, answer) {
			t.Fatalf("want %v, got %v", answer, err)
		}
		if loc.calls != 0 {
			t.Fatalf("%v: local was called", answer)
		}
	}
}

func TestBreakerSkipsFailingService(t *testing.T) {
	svc := &fake{err: errors.New("timeout")}
	loc := &fake{name: "local"}
	c := New(nil,
		Member{Name: "service", Backend: svc, MaxFailures: 2, Cooldown: time.Minute},
		Member{Name: "local", Backend: loc})
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if _, err := c.Search(context.Background(), scout.Query{}); err != nil {
			t.Fatal(err)
		}
	}
	if svc.calls != 2 {
		t.Fatalf("service called %d times, want 2 (then skipped)", svc.calls)
	}
	now = now.Add(2 * time.Minute)
	_, _ = c.Search(context.Background(), scout.Query{})
	if svc.calls != 3 {
		t.Fatalf("service not retried after cooldown: %d calls", svc.calls)
	}
}

func TestAllFail(t *testing.T) {
	c := New(nil,
		Member{Name: "a", Backend: &fake{err: errors.New("down")}},
		Member{Name: "b", Backend: &fake{err: errors.New("also down")}})
	if _, err := c.Search(context.Background(), scout.Query{}); err == nil {
		t.Fatal("want error")
	}
}
