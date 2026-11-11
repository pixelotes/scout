package subtitles

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pixelotes/scout"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func TestExtractSRT(t *testing.T) {
	pack := zipOf(t, map[string]string{
		"Show.S01E01.srt":       "one",
		"Show.S01E02.srt":       "two",
		"Show 1x03.srt":         "three",
		"readme.txt":            "x",
		"__MACOSX/._S01E02.srt": "junk",
	})
	for ep, want := range map[int]string{1: "one", 2: "two", 3: "three"} {
		name, data, err := extractSRT(pack, 1, ep)
		if err != nil || string(data) != want {
			t.Fatalf("ep %d: %q %q %v", ep, name, data, err)
		}
	}
	if _, _, err := extractSRT(pack, 1, 9); err == nil {
		t.Fatal("missing episode in a pack should fail")
	}
	anime := zipOf(t, map[string]string{
		"[DB]Sousou no Frieren_-_01_(Dual Audio_10bit_1080p_x265).srt": "uno",
		"[DB]Sousou no Frieren_-_10_(Dual Audio_10bit_1080p_x265).srt": "diez",
		"[DB]Sousou no Frieren_-_28_(Dual Audio_10bit_1080p_x265).srt": "veintiocho",
	})
	for ep, want := range map[int]string{1: "uno", 10: "diez", 28: "veintiocho"} {
		if _, data, err := extractSRT(anime, 1, ep); err != nil || string(data) != want {
			t.Fatalf("anime ep %d: %q %v", ep, data, err)
		}
	}
	if _, _, err := extractSRT(anime, 1, 5); err == nil {
		t.Fatal("anime ep 5 is not in the pack")
	}
	// A single file is used whatever its name.
	if _, data, err := extractSRT(zipOf(t, map[string]string{"x.srt": "only"}), 1, 9); err != nil || string(data) != "only" {
		t.Fatalf("single: %q %v", data, err)
	}
	if _, _, err := extractSRT(zipOf(t, map[string]string{"a.txt": "x"}), 0, 0); !errors.Is(err, errNoSRT) {
		t.Fatalf("want errNoSRT, got %v", err)
	}
	// Not an archive: passed through.
	if _, data, _ := extractSRT([]byte("1\n00:00:01,000 --> 00:00:02,000\nhola\n"), 0, 0); !bytes.HasPrefix(data, []byte("1\n")) {
		t.Fatalf("plain: %q", data)
	}
}

func TestToUTF8(t *testing.T) {
	// "¿Qué? — sí" in Windows-1252
	in := []byte{0xbf, 'Q', 'u', 0xe9, '?', ' ', 0x97, ' ', 's', 0xed}
	if got := string(toUTF8(in)); got != "¿Qué? — sí" {
		t.Fatalf("got %q", got)
	}
	if got := string(toUTF8([]byte("\xef\xbb\xbfya utf8 ñ"))); got != "ya utf8 ñ" {
		t.Fatalf("got %q", got)
	}
}

func TestHint(t *testing.T) {
	id := episodeHint("3197651-3213944", 2, 10)
	if id != "3197651-3213944_s02e10" {
		t.Fatal(id)
	}
	if b, s, e := splitHint(id); b != "3197651-3213944" || s != 2 || e != 10 {
		t.Fatal(b, s, e)
	}
	if b, s, e := splitHint("639051"); b != "639051" || s != 0 || e != 0 {
		t.Fatal(b, s, e)
	}
}

func TestSubDL(t *testing.T) {
	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Encode()
		if r.URL.Query().Get("api_key") != "k" {
			w.WriteHeader(403)
			w.Write([]byte(`{"status":false,"statusCode":403,"error":"not_authorized","message":"Not Authorized"}`))
			return
		}
		ep := func(n int) *int { return &n }
		json.NewEncoder(w).Encode(map[string]any{"status": true, "subtitles": []map[string]any{
			{"release_name": "Show.S01E02.WEB", "name": "a.zip", "language": "ES", "author": "ana", "url": "/subtitle/100-200.zip", "season": 1, "episode": ep(2)},
			{"release_name": "Show.S01E03.WEB", "language": "ES", "url": "/subtitle/100-300.zip", "season": 1, "episode": ep(3)},
			{"release_name": "Show.S01.Pack", "language": "BR_PT", "url": "/subtitle/100-400.zip", "season": 1, "full_season": true},
		}})
	})
	mux.HandleFunc("GET /subtitle/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipOf(t, map[string]string{"Show.S01E01.srt": "uno", "Show.S01E02.srt": "dos"}))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := NewSubDL("k", []string{"es"}, 0)
	s.base, s.dlBase = srv.URL+"/api/v1/subtitles", srv.URL+"/subtitle/"
	subs, err := s.Search(context.Background(), scout.SubtitleQuery{IMDbID: "tt11280740", Season: 1, Episode: 2, Languages: []string{"es", "pt-br"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"imdb_id=tt11280740", "type=tv", "season_number=1", "episode_number=2", "languages=ES%2CBR_PT"} {
		if !strings.Contains(got, want) {
			t.Errorf("query %q lacks %q", got, want)
		}
	}
	if len(subs) != 2 || subs[0].FileID != "100-200_s01e02" || subs[0].Language != "es" || subs[1].Language != "pt-br" || subs[0].MatchedBy != "id" {
		t.Fatalf("subs %+v", subs)
	}
	f, err := s.Download(context.Background(), subs[1].FileID)
	if err != nil || string(f.Data) != "dos" {
		t.Fatalf("pack download picked %q, %v", f, err)
	}

	bad := NewSubDL("wrong", nil, 0)
	bad.base = srv.URL + "/api/v1/subtitles"
	if _, err := bad.Search(context.Background(), scout.SubtitleQuery{Query: "x"}); !errors.Is(err, scout.ErrBadRequest) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := s.Search(context.Background(), scout.SubtitleQuery{Hash: "abc"}); !errors.Is(err, scout.ErrNotFound) {
		t.Fatalf("hash only: %v", err)
	}
}

// fakeSubdivx is the site plus a FlareSolverr that hands out a cookie. The
// site answers 403 to stale cookies, like Cloudflare.
type fakeSubdivx struct {
	site, solver *httptest.Server
	solves       atomic.Int32
	valid        atomic.Value // current good cookie
	lastSearch   string
}

func newFakeSubdivx(t *testing.T) *fakeSubdivx {
	f := &fakeSubdivx{}
	f.valid.Store("")
	site := http.NewServeMux()
	ok := func(r *http.Request) bool {
		return strings.Contains(r.Header.Get("Cookie"), "cf_clearance="+f.valid.Load().(string))
	}
	site.HandleFunc("GET /inc/gt.php", func(w http.ResponseWriter, r *http.Request) {
		if !ok(r) {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{"token":"tok"}`))
	})
	site.HandleFunc("POST /inc/ajax.php", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.lastSearch = r.Form.Get("buscar431")
		if !ok(r) || r.Form.Get("token") != "tok" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{"estrategia":"1","aaData":[
			{"id":"639051","titulo":"Severance  S01E02","descripcion":"WEBRip ION10","descargas":"6281","promedio":"4.9","nick":"MarcusL","pack":"0","eliminado":"0"},
			{"id":"639052","titulo":"Severance S01E03","descripcion":"otro","descargas":"9","pack":"0","eliminado":"0"},
			{"id":"700000","titulo":"Severance Temporada 1","descripcion":"pack completo","descargas":"50","pack":"1","eliminado":"0"}]}`))
	})
	site.HandleFunc("GET /descargar.php", func(w http.ResponseWriter, r *http.Request) {
		if !ok(r) {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipOf(t, map[string]string{"Severance.S01E02.srt": "hola \xe9"}))
	})
	f.site = httptest.NewServer(site)
	f.solver = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.solves.Add(1)
		cookie := fmt.Sprintf("c%d", n)
		f.valid.Store(cookie)
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{
			"status": 200, "userAgent": "UA", "response": `<span id="vs">v4.31</span>`,
			"cookies": []map[string]string{{"name": "cf_clearance", "value": cookie}},
		}})
	}))
	t.Cleanup(func() { f.site.Close(); f.solver.Close() })
	return f
}

func TestSubdivx(t *testing.T) {
	f := newFakeSubdivx(t)
	s := NewSubdivx(f.solver.URL, 0)
	s.base = f.site.URL
	s.searchGap = 0
	ctx := context.Background()

	if _, err := s.Search(ctx, scout.SubtitleQuery{Query: "Severance", Languages: []string{"en"}}); !errors.Is(err, scout.ErrNotFound) {
		t.Fatalf("english only should be not found without calling the site: %v", err)
	}
	if f.solves.Load() != 0 {
		t.Fatal("solved without need")
	}

	subs, err := s.Search(ctx, scout.SubtitleQuery{Query: "Severance", Season: 1, Episode: 2, Languages: []string{"es", "en"}})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastSearch != "Severance S01E02" {
		t.Fatalf("search text %q", f.lastSearch)
	}
	if len(subs) != 2 || subs[0].FileID != "639051_s01e02" || subs[0].Downloads != 6281 || subs[1].FileID != "700000_s01e02" {
		t.Fatalf("want the episode and the season pack, got %+v", subs)
	}

	// Cloudflare expires the clearance: one new solve, then it works.
	f.valid.Store("expired-elsewhere")
	file, err := s.Download(ctx, subs[0].FileID)
	if err != nil {
		t.Fatal(err)
	}
	if string(file.Data) != "hola é" || f.solves.Load() != 2 {
		t.Fatalf("data %q, solves %d", file.Data, f.solves.Load())
	}
}

type stubProvider struct {
	name string
	subs []scout.Subtitle
	err  error
}

func (p stubProvider) Name() string { return p.name }
func (p stubProvider) Search(context.Context, scout.SubtitleQuery) ([]scout.Subtitle, error) {
	return p.subs, p.err
}
func (p stubProvider) Download(_ context.Context, id string) (*scout.SubtitleFile, error) {
	return &scout.SubtitleFile{FileName: p.name + id}, nil
}
func (p stubProvider) Quota() scout.Quota { return scout.Quota{Provider: p.name} }

func TestMulti(t *testing.T) {
	m := NewMulti(
		stubProvider{name: "a", subs: []scout.Subtitle{{Provider: "a", Language: "en"}}},
		stubProvider{name: "b", err: errors.New("down")},
		stubProvider{name: "c", subs: []scout.Subtitle{{Provider: "c", Language: "es"}}},
	)
	subs, err := m.Search(context.Background(), scout.SubtitleQuery{Query: "x"})
	if err != nil || len(subs) != 2 {
		t.Fatalf("merge: %v %v", subs, err)
	}
	if f, err := m.Download(context.Background(), "c", "1"); err != nil || f.FileName != "c1" {
		t.Fatalf("route: %v %v", f, err)
	}
	if _, err := m.Download(context.Background(), "zz", "1"); !errors.Is(err, scout.ErrBadRequest) {
		t.Fatalf("unknown provider: %v", err)
	}

	nf := NewMulti(stubProvider{name: "a", err: scout.ErrNotFound}, stubProvider{name: "b", err: scout.ErrNotFound})
	if _, err := nf.Search(context.Background(), scout.SubtitleQuery{}); !errors.Is(err, scout.ErrNotFound) {
		t.Fatalf("all not found: %v", err)
	}
	mixed := NewMulti(stubProvider{name: "a", err: scout.ErrNotFound}, stubProvider{name: "b", err: errors.New("down")})
	if _, err := mixed.Search(context.Background(), scout.SubtitleQuery{}); err == nil || errors.Is(err, scout.ErrNotFound) {
		t.Fatalf("a failure must not look like not found: %v", err)
	}
}
