// Package server exposes any scout.Backend over HTTP. Routes:
//
//	GET  /healthz
//	GET  /v1/search?kind=&title=&year=&language=&providers=a,b&limit=
//	GET  /v1/items/{provider}/{kind}/{id}?language=
//	POST /v1/subtitles/search          body: scout.SubtitleQuery
//	GET  /v1/subtitles/{provider}/{file_id}   returns the file
//	GET  /v1/quota
//
// Errors are JSON {"error": "...", "code": "not_found|bad_request|
// quota_exceeded|unsupported|upstream"}; package remote maps them back.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pixelotes/scout"
)

// Handler returns the API. token, when set, is required as a bearer token on
// every route except /healthz.
func Handler(b scout.Backend, token string, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	s := &srv{b: b, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/search", s.search)
	mux.HandleFunc("GET /v1/items/{provider}/{kind}/{id}", s.get)
	mux.HandleFunc("POST /v1/subtitles/search", s.subSearch)
	mux.HandleFunc("GET /v1/subtitles/{provider}/{id}", s.subDownload)
	mux.HandleFunc("GET /v1/quota", s.quota)
	return s.wrap(mux, token)
}

type srv struct {
	b   scout.Backend
	log *slog.Logger
}

func (s *srv) wrap(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if token != "" && r.URL.Path != "/healthz" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, apiError{Error: "missing or wrong token", Code: "unauthorized"})
				return
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" {
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *srv) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := scout.Query{
		Kind:     scout.Kind(v.Get("kind")),
		Title:    v.Get("title"),
		Language: v.Get("language"),
	}
	q.Year, _ = strconv.Atoi(v.Get("year"))
	q.Limit, _ = strconv.Atoi(v.Get("limit"))
	if p := v.Get("providers"); p != "" {
		q.Providers = strings.Split(p, ",")
	}
	items, err := s.b.Search(r.Context(), q)
	s.reply(w, items, err)
}

func (s *srv) get(w http.ResponseWriter, r *http.Request) {
	item, err := s.b.Get(r.Context(), scout.Lookup{
		Provider: r.PathValue("provider"),
		Kind:     scout.Kind(r.PathValue("kind")),
		ID:       r.PathValue("id"),
		Language: r.URL.Query().Get("language"),
	})
	s.reply(w, item, err)
}

func (s *srv) subSearch(w http.ResponseWriter, r *http.Request) {
	var q scout.SubtitleQuery
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&q); err != nil {
		s.reply(w, nil, fmt.Errorf("invalid JSON body: %v: %w", err, scout.ErrBadRequest))
		return
	}
	subs, err := s.b.SearchSubtitles(r.Context(), q)
	s.reply(w, subs, err)
}

func (s *srv) subDownload(w http.ResponseWriter, r *http.Request) {
	f, err := s.b.DownloadSubtitle(r.Context(), r.PathValue("provider"), r.PathValue("id"))
	if err != nil {
		s.reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-subrip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.FileName}))
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	_, _ = w.Write(f.Data)
}

func (s *srv) quota(w http.ResponseWriter, r *http.Request) {
	q, err := s.b.Quota(r.Context())
	s.reply(w, q, err)
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Codes shared with package remote.
const (
	CodeNotFound   = "not_found"
	CodeBadRequest = "bad_request"
	CodeQuota      = "quota_exceeded"
	CodeUnsupport  = "unsupported"
	CodeUpstream   = "upstream"
)

func (s *srv) reply(w http.ResponseWriter, v any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, v)
		return
	}
	status, code := http.StatusBadGateway, CodeUpstream
	switch {
	case errors.Is(err, scout.ErrBadRequest):
		status, code = http.StatusBadRequest, CodeBadRequest
	case errors.Is(err, scout.ErrNotFound):
		status, code = http.StatusNotFound, CodeNotFound
	case errors.Is(err, scout.ErrQuotaExceeded):
		status, code = http.StatusTooManyRequests, CodeQuota
	case errors.Is(err, scout.ErrUnsupported):
		status, code = http.StatusNotImplemented, CodeUnsupport
	case errors.Is(err, context.Canceled):
		return // client went away
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	}
	if code == CodeUpstream {
		s.log.Warn("backend error", "err", err)
	}
	writeJSON(w, status, apiError{Error: err.Error(), Code: code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
