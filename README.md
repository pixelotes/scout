# scout

Media metadata and subtitles for [Reel](https://github.com/pixelotes/reel) and
[RMS](https://github.com/pixelotes/rms), in one place. It works three ways:

- **Go module**: apps import it and call the providers in-process.
- **CLI**: `scout search`, `scout subs fetch`, `scout nfo`… for scripts and testing.
- **HTTP service**: `scout serve` (or the Docker image) answers the same calls,
  so API keys, the cache and the OpenSubtitles quota live in one place.

Apps pick the order in `backends`, and the other one is the fallback.

## Providers

| Provider | Movies | Shows | Anime | Key |
|---|---|---|---|---|
| tmdb | ✅ | ✅ | ✅ | API key |
| imdb (IMDb suggestions + Cinemeta) | ✅ | | | — |
| tvmaze | | ✅ | ✅ | — |
| anilist | anime films | | ✅ | — |
| kitsu | anime films | | ✅ | — |
| trakt | ✅ | ✅ | ✅ | client ID |
| opensubtitles (subtitles) | ✅ | ✅ | ✅ | API key |

Every result carries all the external IDs the provider knows
(`imdb`, `tmdb`, `tvdb`, `tvmaze`, `anilist`, `kitsu`, `mal`, `trakt`), and the
NFO writer stores all of them as `<uniqueid>`.

## Subtitles

The caller computes the OpenSubtitles hash (`moviehash.File`), so the service
never needs access to the video files. The search goes hash → IDs → text and
stops at the first step that finds something; for the hash step only real hash
matches count. Downloads go through the disk cache when `subtitle_dir` is set,
and the client stops calling the API once the daily quota is used up, until it
resets.

## CLI

```bash
go install github.com/pixelotes/scout/cmd/scout@latest

scout search movie "The Matrix" -year 1999
scout get tvmaze show 44933
scout identify anime "Frieren"
scout nfo movie "/media/Movies/The Matrix (1999)"   # movie.nfo + poster/fanart/logo
scout hash movie.mkv
scout subs fetch "/media/TV/Severance/Severance S01E01.mkv" -lang es,en
scout serve
```

Configuration: `-config file.yml` or `SCOUT_CONFIG`, with `SCOUT_*` env vars on
top. See [config.example.yml](config.example.yml). Without a config, scout uses
the keyless providers.

## HTTP API

| Method | Path | |
|---|---|---|
| GET | `/healthz` | |
| GET | `/v1/search?kind=&title=&year=&language=&providers=&limit=` | candidates |
| GET | `/v1/items/{provider}/{kind}/{id}?language=` | full details, episodes included |
| POST | `/v1/subtitles/search` | body: `{hash, size, imdb_id, tmdb_id, season, episode, query, languages}` |
| GET | `/v1/subtitles/{provider}/{file_id}` | the subtitle file |
| GET | `/v1/quota` | remaining subtitle downloads |

Errors are `{"error": "...", "code": "not_found|bad_request|quota_exceeded|unsupported|upstream"}`.
With `server.token` set, requests need `Authorization: Bearer <token>`.

## Docker

```bash
docker build -t pixelotes/scout .
docker run -p 8090:8090 \
  -e SCOUT_TMDB_API_KEY=... -e SCOUT_OPENSUBTITLES_API_KEY=... \
  -v scout-cache:/cache pixelotes/scout
```

To use a file instead, mount it and set `-e SCOUT_CONFIG=/config/config.yml`.

## In an app

```go
cfg := config.Default()           // or embed config.Config in the app's own config
cfg.Backends = []string{"service", "local"}
cfg.Service.URL = "http://scout:8090"
b, err := client.New(cfg, logger) // scout.Backend

items, err := b.Search(ctx, scout.Query{Kind: scout.Movie, Title: "Dune", Year: 2021})
full, err := b.Get(ctx, scout.Lookup{Kind: scout.Movie, Provider: items[0].Provider, ID: items[0].ID})

hash, size, _ := moviehash.File(path)
subs, err := b.SearchSubtitles(ctx, scout.SubtitleQuery{Hash: hash, Size: size, IMDbID: full.IDs["imdb"], Languages: []string{"es"}})
```

The chain moves to the next backend only on failures (connection refused,
timeouts, 5xx). Answers such as not found or quota exceeded are returned as they
are, so the fallback never spends quota twice. After `max_failures` failures in a
row, the service is skipped for `cooldown`.
