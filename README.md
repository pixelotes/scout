# Scout

Scout finds metadata (titles, plots, artwork, episode lists, external IDs) and subtitles for movies, TV shows and anime, for [Reel](https://github.com/pixelotes/reel) and [RMS](https://github.com/pixelotes/rms).

Part of [MangoStack](https://github.com/pixelotes/MangoStack), a self-hosted media stack for the Raspberry Pi.

## Footprint

- Memory: about 11 MB RSS after a few searches, measured in Docker on a Mac (arm64) with the default config. Not yet measured on a Raspberry Pi.
- Binary: about 11 MB; the Docker image (Alpine) about 29 MB.
- What raises memory: the metadata cache (`local.cache.max_entries`, results kept for `metadata_ttl`), full show lookups with long episode lists, and season-pack subtitle downloads (about 25 MB RSS after opening a 28-episode Subdivx pack; the Go runtime returns it slowly).
- CPU is negligible; lookups are network-bound.
- Disk writes: only downloaded subtitles in `local.cache.subtitle_dir`, when set. The CLI's `nfo` and `subs fetch` commands write next to your media.

## Features

- One API for every provider: TMDB, IMDb (keyless), TVMaze, AniList, Kitsu, Trakt for metadata; OpenSubtitles, SubDL and Subdivx for subtitles.
- Three ways to use it, all with the same code:
  - **Go module**: Reel and RMS import it and call the providers in-process.
  - **CLI**: `scout search`, `scout nfo`, `scout subs fetch`…
  - **HTTP service**: `scout serve` or the Docker image, so API keys, the cache and the OpenSubtitles quota live in one place.
- Apps choose the order in `backends` (`service`, `local`, or both). The second is only used when the first fails (down, timeout, 5xx), never when it answers "not found" or "quota exceeded". A service that keeps failing is skipped for a while.
- Every result carries all the external IDs the provider knows (`imdb`, `tmdb`, `tvdb`, `tvmaze`, `anilist`, `kitsu`, `mal`, `trakt`), and the NFO writer stores all of them as `<uniqueid>`.
- Subtitle providers are searched together and their candidates merged, so a language one lacks can come from another. Within OpenSubtitles the search goes hash → IDs → text. The caller computes the hash, so the service never needs access to the video files.
- Season packs (SubDL, Subdivx) are opened and the right episode picked, by `S01E02`/`1x02` or by the bare number anime packs use (`Show_-_02_(1080p).srt`). Subtitles are converted to UTF-8.
- Downloaded subtitles are cached on disk and never charged against the quota twice. Once the daily quota is used up, Scout stops calling the API until it resets.
- Kodi/Jellyfin NFO files and artwork (`poster.jpg`, `folder.jpg`, `fanart.jpg`, `backdrop.jpg`, `logo.png`) with the names RMS reads.

| Provider | Movies | Shows | Anime | Needs |
|---|---|---|---|---|
| tmdb | ✅ | ✅ | ✅ | API key |
| imdb (IMDb suggestions + Cinemeta) | ✅ | | | — |
| tvmaze | | ✅ | ✅ | — |
| anilist | anime films | | ✅ | — |
| kitsu | anime films | | ✅ | — |
| trakt | ✅ | ✅ | ✅ | client ID |
| opensubtitles (subtitles) | ✅ | ✅ | ✅ | API key |
| subdl (subtitles) | ✅ | ✅ | ✅ | API key (free) |
| subdivx (Spanish subtitles) | ✅ | ✅ | ✅ | a FlareSolverr-compatible solver, e.g. [Parasol](https://github.com/pixelotes/parasol) |

## Quick start

Use the published `pixelotes/scout:latest` image, or build your own with `docker build -t scout .`.

```yaml
services:
  scout:
    image: pixelotes/scout:latest
    container_name: scout
    restart: unless-stopped
    ports:
      - "8090:8090"
    environment:
      - SCOUT_TMDB_API_KEY=your-tmdb-key
      - SCOUT_OPENSUBTITLES_API_KEY=your-opensubtitles-key
      - SCOUT_TOKEN=a-long-random-string
    volumes:
      - ./cache:/cache
```

Check it:

```bash
curl -H "Authorization: Bearer a-long-random-string" "http://localhost:8090/v1/search?kind=movie&title=Dune&year=2021"
```

## Configuration

Scout reads an optional YAML file (`-config file.yml` or `SCOUT_CONFIG`), then `SCOUT_*` environment variables on top. Without a file it uses the keyless providers. Reel and RMS embed the same block under their own `crawlers:` key. See [config.example.yml](config.example.yml) for every option.

```yaml
backends: ["service", "local"]   # order to try

service:
  url: "http://scout:8090"
  token: ""
  timeout: 10s
  max_failures: 3
  cooldown: 2m

local:
  language: "en"
  providers:
    movie: ["tmdb", "imdb"]
    show: ["tvmaze", "tmdb"]
    anime: ["tvmaze", "anilist", "kitsu", "tmdb"]
  tmdb:
    api_key: ""
  opensubtitles:
    api_key: ""
  subdl:
    api_key: ""
  subdivx:
    flaresolverr_url: "http://parasol:8191"
  subtitle_providers: ["opensubtitles", "subdl", "subdivx"]
  subtitle_languages: ["en", "es"]
  cache:
    metadata_ttl: 24h
    subtitle_dir: "/cache/subtitles"

server:
  listen: ":8090"
  token: ""
```

| Variable | Overrides |
|---|---|
| `SCOUT_CONFIG` | config file path |
| `SCOUT_BACKENDS` | `backends` (comma-separated) |
| `SCOUT_SERVICE_URL`, `SCOUT_SERVICE_TOKEN` | `service.url`, `service.token` |
| `SCOUT_LANGUAGE` | `local.language` |
| `SCOUT_MOVIE_PROVIDERS`, `SCOUT_SHOW_PROVIDERS`, `SCOUT_ANIME_PROVIDERS` | `local.providers.*` |
| `SCOUT_TMDB_API_KEY`, `SCOUT_TRAKT_CLIENT_ID` | provider keys |
| `SCOUT_OPENSUBTITLES_API_KEY`, `_USERNAME`, `_PASSWORD` | OpenSubtitles account |
| `SCOUT_SUBDL_API_KEY` | `local.subdl.api_key` |
| `SCOUT_FLARESOLVERR_URL` | `local.subdivx.flaresolverr_url` |
| `SCOUT_SUBTITLE_PROVIDERS` | `local.subtitle_providers` |
| `SCOUT_SUBTITLE_LANGUAGES` | `local.subtitle_languages` |
| `SCOUT_SUBTITLE_CACHE_DIR` | `local.cache.subtitle_dir` (`/cache/subtitles` in Docker) |
| `SCOUT_LISTEN`, `SCOUT_TOKEN` | `server.listen`, `server.token` |

## Usage

### CLI

```bash
scout search movie "The Matrix" -year 1999
scout get tvmaze show 44933
scout identify anime "Frieren"
scout nfo movie "/media/Movies/The Matrix (1999)"   # movie.nfo + artwork
scout hash movie.mkv
scout subs search movie.mkv -lang es
scout subs fetch "/media/TV/Severance/Severance S01E01.mkv" -lang es,en
scout quota
scout serve
```

With Docker: `docker run --rm pixelotes/scout search show "The Bear"`.

`subs fetch` only downloads languages that have no `<video>.<lang>.srt` yet. Exit codes: 0 ok, 1 error, 3 not found.

### HTTP API

| Method | Path | |
|---|---|---|
| GET | `/healthz` | no token needed |
| GET | `/v1/search?kind=&title=&year=&language=&providers=&limit=` | candidates, best first |
| GET | `/v1/items/{provider}/{kind}/{id}?language=` | full details, episodes included |
| POST | `/v1/subtitles/search` | body: `{hash, size, imdb_id, tmdb_id, season, episode, query, languages}` |
| GET | `/v1/subtitles/{provider}/{file_id}` | the subtitle file |
| GET | `/v1/quota` | remaining subtitle downloads |

Errors are `{"error": "...", "code": "not_found|bad_request|quota_exceeded|unsupported|upstream"}`. With `server.token` set, requests need `Authorization: Bearer <token>`.

### Go module

```go
cfg := config.Default()
cfg.Backends = []string{"service", "local"}
cfg.Service.URL = "http://scout:8090"
b, err := client.New(cfg, logger) // scout.Backend

items, err := b.Search(ctx, scout.Query{Kind: scout.Movie, Title: "Dune", Year: 2021})
full, err := b.Get(ctx, scout.Lookup{Kind: scout.Movie, Provider: items[0].Provider, ID: items[0].ID})

hash, size, _ := moviehash.File(path)
subs, err := b.SearchSubtitles(ctx, scout.SubtitleQuery{
    Hash: hash, Size: size, IMDbID: full.IDs["imdb"], Languages: []string{"es"},
})
```

## Building from source

```bash
git clone https://github.com/pixelotes/scout.git
cd scout
go build -o scout ./cmd/scout
./scout search movie "Alien" -year 1979

# Cross-compile for Raspberry Pi
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o scout ./cmd/scout
```

## Limits

- IMDb has no public API: the `imdb` provider relies on IMDb's search suggestions and Stremio's Cinemeta, neither of which promises stability. Keep `tmdb` first when you have a key.
- AniList has no episode titles; its episodes are numbered. Kitsu has them.
- TVMaze is English only; `language` affects TMDB alone.
- Only OpenSubtitles searches by file hash. SubDL searches by ID or title, Subdivx by title only.
- Subdivx is scraping behind Cloudflare, not an API: it breaks when the site or the challenge changes, and allows one search every ~3 seconds (Scout waits). Bazarr dropped it for that reason.
- SubDL's response handling was written from its documentation and tested against a fake server; not yet checked with a real key.
- RAR archives are not supported (neither SubDL nor Subdivx serves them today).
- The metadata cache is in memory and is lost on restart.
- One shared token, no per-client keys. Do not expose Scout to the internet; it assumes a trusted LAN.
- Reel uses it (pixelotes/reel#16); RMS does not yet.

## Part of MangoStack

| App | Role |
|---|---|
| [Scarf](https://github.com/pixelotes/scarf) | Indexer proxy (Torznab) |
| [Parasol](https://github.com/pixelotes/parasol) | Optional Cloudflare solver for Scarf |
| [Reel](https://github.com/pixelotes/reel) | Movie & TV automation |
| [Tango](https://github.com/pixelotes/tango) | Web UI and REST API for aria2c |
| [RMS](https://github.com/pixelotes/rms) | Media server (Jellyfin API) |
| [Scout](https://github.com/pixelotes/scout) | Metadata and subtitles for Reel and RMS |
| [Suika](https://github.com/pixelotes/suika) | Manga reader |
| [Kaze](https://github.com/pixelotes/kaze) | Music server (Subsonic API) |
| [Sumi](https://github.com/pixelotes/sumi) | EPUB reader |
| [Dashboarr](https://github.com/pixelotes/dashboarr) | Homepage |

## License

MIT
