// Package remote is a scout.Backend that talks to a scout server over HTTP.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pixelotes/scout"
	"github.com/pixelotes/scout/server"
)

// Client implements scout.Backend against a scout server.
type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: timeout}}
}

// Error is a failure the server reported or a transport error. Answer errors
// (not found, bad request, quota, unsupported) wrap the matching scout error,
// so scout.IsAnswer works on them.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("scout service: %s (HTTP %d)", e.Msg, e.Status) }

func (e *Error) Unwrap() error {
	switch e.Code {
	case server.CodeNotFound:
		return scout.ErrNotFound
	case server.CodeBadRequest:
		return scout.ErrBadRequest
	case server.CodeQuota:
		return scout.ErrQuotaExceeded
	case server.CodeUnsupport:
		return scout.ErrUnsupported
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scout service: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	var ae struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &ae) != nil || ae.Error == "" {
		ae.Error = strings.TrimSpace(string(raw))
	}
	return nil, &Error{Status: resp.StatusCode, Code: ae.Code, Msg: ae.Error}
}

func (c *Client) getJSON(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Search(ctx context.Context, q scout.Query) ([]scout.Item, error) {
	v := url.Values{"kind": {string(q.Kind)}, "title": {q.Title}}
	if q.Year > 0 {
		v.Set("year", strconv.Itoa(q.Year))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Language != "" {
		v.Set("language", q.Language)
	}
	if len(q.Providers) > 0 {
		v.Set("providers", strings.Join(q.Providers, ","))
	}
	var items []scout.Item
	return items, c.getJSON(ctx, http.MethodGet, "/v1/search?"+v.Encode(), nil, &items)
}

func (c *Client) Get(ctx context.Context, l scout.Lookup) (*scout.Item, error) {
	path := fmt.Sprintf("/v1/items/%s/%s/%s", url.PathEscape(l.Provider), url.PathEscape(string(l.Kind)), url.PathEscape(l.ID))
	if l.Language != "" {
		path += "?language=" + url.QueryEscape(l.Language)
	}
	var item scout.Item
	if err := c.getJSON(ctx, http.MethodGet, path, nil, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (c *Client) SearchSubtitles(ctx context.Context, q scout.SubtitleQuery) ([]scout.Subtitle, error) {
	var subs []scout.Subtitle
	return subs, c.getJSON(ctx, http.MethodPost, "/v1/subtitles/search", q, &subs)
}

func (c *Client) DownloadSubtitle(ctx context.Context, provider, fileID string) (*scout.SubtitleFile, error) {
	if provider == "" {
		provider = "opensubtitles"
	}
	resp, err := c.do(ctx, http.MethodGet, "/v1/subtitles/"+url.PathEscape(provider)+"/"+url.PathEscape(fileID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("scout service: %w", err)
	}
	name := fileID + ".srt"
	if _, p, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && p["filename"] != "" {
		name = p["filename"]
	}
	return &scout.SubtitleFile{FileName: name, Data: data}, nil
}

func (c *Client) Quota(ctx context.Context) ([]scout.Quota, error) {
	var q []scout.Quota
	return q, c.getJSON(ctx, http.MethodGet, "/v1/quota", nil, &q)
}
