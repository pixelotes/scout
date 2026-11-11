package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pixelotes/scout"
)

// fakeOS mimics the bits of api.opensubtitles.com the client uses.
type fakeOS struct {
	mu        sync.Mutex
	searches  []string
	remaining int
	srv       *httptest.Server
}

func newFakeOS(t *testing.T, hashHits bool) *fakeOS {
	f := &fakeOS{remaining: 2}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /subtitles", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		q := r.URL.Query()
		f.mu.Lock()
		f.searches = append(f.searches, q.Encode())
		f.mu.Unlock()
		match := q.Get("moviehash") != "" && hashHits
		data := []map[string]any{}
		// A hash search always pads with a loose match; only hashHits adds a real one.
		data = append(data, sub(1, "es", false))
		if match {
			data = append(data, sub(2, "es", true))
		}
		if q.Get("moviehash") != "" && !hashHits {
			data = data[:0]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("POST /download", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		if f.remaining == 0 {
			w.WriteHeader(406)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "quota", "reset_time_utc": reset})
			return
		}
		f.remaining--
		_ = json.NewEncoder(w).Encode(map[string]any{
			"link": f.srv.URL + "/file", "file_name": "x.srt", "remaining": f.remaining, "reset_time_utc": reset,
		})
	})
	mux.HandleFunc("GET /file", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("srt")) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func sub(id int, lang string, hash bool) map[string]any {
	return map[string]any{"attributes": map[string]any{
		"language": lang, "moviehash_match": hash,
		"files": []map[string]any{{"file_id": id, "file_name": "f.srt"}},
	}}
}

func client(f *fakeOS) *OpenSubtitles {
	o := NewOpenSubtitles(OpenSubtitlesConfig{APIKey: "k", Languages: []string{"es"}})
	o.base = f.srv.URL
	return o
}

func TestHashFirst(t *testing.T) {
	f := newFakeOS(t, true)
	subs, err := client(f).Search(context.Background(), scout.SubtitleQuery{Hash: "ABC", IMDbID: "tt0133093", Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].FileID != "2" || subs[0].MatchedBy != "hash" {
		t.Fatalf("want only the real hash match, got %+v", subs)
	}
	if len(f.searches) != 1 {
		t.Fatalf("hash hit should stop the search, got %v", f.searches)
	}
}

func TestFallsToIDForEpisode(t *testing.T) {
	f := newFakeOS(t, false)
	subs, err := client(f).Search(context.Background(),
		scout.SubtitleQuery{Hash: "abc", IMDbID: "tt0133093", Season: 1, Episode: 2})
	if err != nil {
		t.Fatal(err)
	}
	if subs[0].MatchedBy != "id" {
		t.Fatalf("got %+v", subs)
	}
	want := "episode_number=2&languages=es&parent_imdb_id=133093&season_number=1"
	if f.searches[1] != want {
		t.Fatalf("id search\n got %s\nwant %s", f.searches[1], want)
	}
}

func TestQuota(t *testing.T) {
	f := newFakeOS(t, true)
	o := client(f)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := o.Download(ctx, "2"); err != nil {
			t.Fatal(err)
		}
	}
	if q := o.Quota(); !q.Known || q.Remaining != 0 {
		t.Fatalf("quota %+v", q)
	}
	if _, err := o.Download(ctx, "2"); !errors.Is(err, scout.ErrQuotaExceeded) {
		t.Fatalf("want quota error before calling the API, got %v", err)
	}
}

func TestQuota406(t *testing.T) {
	f := newFakeOS(t, true)
	f.remaining = 0
	o := client(f)
	if _, err := o.Download(context.Background(), "2"); !errors.Is(err, scout.ErrQuotaExceeded) {
		t.Fatalf("406 should be quota exceeded, got %v", err)
	}
}

func TestNoKey(t *testing.T) {
	o := NewOpenSubtitles(OpenSubtitlesConfig{})
	if _, err := o.Search(context.Background(), scout.SubtitleQuery{Query: "x"}); !errors.Is(err, scout.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}
