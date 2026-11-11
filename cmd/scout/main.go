// Command scout looks up media metadata and subtitles from the command line,
// or serves the same lookups over HTTP with `scout serve`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pixelotes/scout"
	"github.com/pixelotes/scout/client"
	"github.com/pixelotes/scout/config"
	"github.com/pixelotes/scout/local"
	"github.com/pixelotes/scout/moviehash"
	"github.com/pixelotes/scout/nfo"
	"github.com/pixelotes/scout/server"
	"github.com/pixelotes/scout/subtitles"
)

var version = "dev"

const usage = `scout — media metadata and subtitles

Usage:
  scout [-config file] <command> [flags] [args]

Commands:
  search <movie|show|anime> <title>       candidates, best first
  get <provider> <movie|show|anime> <id>  full details, episodes included
  identify <movie|show|anime> <title>     search, then get the best match
  nfo <movie|show|anime> <dir>            identify from the folder name and write NFO + artwork
  hash <file>                             OpenSubtitles hash and size
  subs search <video>                     subtitle candidates (hash, then IDs, then name)
  subs fetch <video>                      download missing languages next to the video
  quota                                   remaining subtitle downloads
  serve                                   run the HTTP API
  version

The config file defaults to $SCOUT_CONFIG; SCOUT_* env vars override it.
`

func main() {
	fs := flag.NewFlagSet("scout", flag.ExitOnError)
	cfgPath := fs.String("config", os.Getenv("SCOUT_CONFIG"), "config file (YAML)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	_ = fs.Parse(os.Args[1:])
	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		os.Exit(2)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]
	if cmd == "serve" {
		err = serve(ctx, cfg, rest)
	} else if cmd == "version" {
		fmt.Println(version)
	} else if cmd == "hash" {
		err = hashCmd(rest)
	} else {
		var b scout.Backend
		b, err = client.New(cfg, log)
		if err == nil {
			err = run(ctx, b, cfg, cmd, rest)
		}
	}
	if err != nil {
		fatal(err)
	}
}

func run(ctx context.Context, b scout.Backend, cfg config.Config, cmd string, args []string) error {
	switch cmd {
	case "search":
		return searchCmd(ctx, b, args)
	case "get":
		return getCmd(ctx, b, args)
	case "identify":
		return identifyCmd(ctx, b, args)
	case "nfo":
		return nfoCmd(ctx, b, args)
	case "subs":
		if len(args) == 0 {
			return errors.New("subs needs search or fetch")
		}
		switch args[0] {
		case "search":
			return subsSearchCmd(ctx, b, args[1:])
		case "fetch":
			return subsFetchCmd(ctx, b, cfg, args[1:])
		}
		return fmt.Errorf("unknown subs command %q", args[0])
	case "quota":
		q, err := b.Quota(ctx)
		if err != nil {
			return err
		}
		return printJSON(q)
	}
	return fmt.Errorf("unknown command %q (see scout -h)", cmd)
}

type queryFlags struct {
	year      *int
	lang      *string
	providers *string
	limit     *int
}

func addQueryFlags(fs *flag.FlagSet) queryFlags {
	return queryFlags{
		year:      fs.Int("year", 0, "release year"),
		lang:      fs.String("lang", "", "metadata language"),
		providers: fs.String("providers", "", "comma-separated provider order (default: config)"),
		limit:     fs.Int("limit", 0, "max results"),
	}
}

func (f queryFlags) query(kind, title string) (scout.Query, error) {
	k := scout.Kind(kind)
	if !k.Valid() {
		return scout.Query{}, fmt.Errorf("kind must be movie, show or anime, not %q", kind)
	}
	q := scout.Query{Kind: k, Title: title, Year: *f.year, Language: *f.lang, Limit: *f.limit}
	if *f.providers != "" {
		q.Providers = strings.Split(*f.providers, ",")
	}
	return q, nil
}

// parse lets flags come before or after the positional arguments.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func searchCmd(ctx context.Context, b scout.Backend, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	qf := addQueryFlags(fs)
	pos := parse(fs, args)
	if len(pos) < 2 {
		return errors.New("usage: scout search <movie|show|anime> <title> [-year N]")
	}
	q, err := qf.query(pos[0], strings.Join(pos[1:], " "))
	if err != nil {
		return err
	}
	items, err := b.Search(ctx, q)
	if err != nil {
		return err
	}
	return printJSON(items)
}

func getCmd(ctx context.Context, b scout.Backend, args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	lang := fs.String("lang", "", "metadata language")
	pos := parse(fs, args)
	if len(pos) != 3 {
		return errors.New("usage: scout get <provider> <movie|show|anime> <id>")
	}
	item, err := b.Get(ctx, scout.Lookup{Provider: pos[0], Kind: scout.Kind(pos[1]), ID: pos[2], Language: *lang})
	if err != nil {
		return err
	}
	return printJSON(item)
}

func identify(ctx context.Context, b scout.Backend, q scout.Query) (*scout.Item, error) {
	q.Limit = 1
	items, err := b.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	best := items[0]
	full, err := b.Get(ctx, scout.Lookup{Kind: q.Kind, Provider: best.Provider, ID: best.ID, Language: q.Language})
	if err != nil {
		return nil, err
	}
	// Keep IDs only the search knew (e.g. Cinemeta's tmdb for an imdb hit).
	for k, v := range best.IDs {
		if _, ok := full.IDs[k]; !ok {
			full.IDs[k] = v
		}
	}
	return full, nil
}

func identifyCmd(ctx context.Context, b scout.Backend, args []string) error {
	fs := flag.NewFlagSet("identify", flag.ExitOnError)
	qf := addQueryFlags(fs)
	pos := parse(fs, args)
	if len(pos) < 2 {
		return errors.New("usage: scout identify <movie|show|anime> <title> [-year N]")
	}
	q, err := qf.query(pos[0], strings.Join(pos[1:], " "))
	if err != nil {
		return err
	}
	item, err := identify(ctx, b, q)
	if err != nil {
		return err
	}
	return printJSON(item)
}

var yearRe = regexp.MustCompile(`\((\d{4})\)`)

// titleYear splits "The Matrix (1999)" into "The Matrix" and 1999.
func titleYear(name string) (string, int) {
	if m := yearRe.FindStringSubmatchIndex(name); m != nil {
		y, _ := strconv.Atoi(name[m[2]:m[3]])
		return strings.TrimSpace(name[:m[0]]), y
	}
	return subtitles.CleanQuery(name), 0
}

func nfoCmd(ctx context.Context, b scout.Backend, args []string) error {
	fs := flag.NewFlagSet("nfo", flag.ExitOnError)
	qf := addQueryFlags(fs)
	title := fs.String("title", "", "title to search (default: from the folder name)")
	force := fs.Bool("force", false, "overwrite an existing NFO")
	noArt := fs.Bool("no-artwork", false, "skip poster/fanart/logo")
	pos := parse(fs, args)
	if len(pos) != 2 {
		return errors.New("usage: scout nfo <movie|show|anime> <dir>")
	}
	dir := pos[1]
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	name := "tvshow.nfo"
	if pos[0] == string(scout.Movie) {
		name = "movie.nfo"
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil && !*force {
		return fmt.Errorf("%s already exists (use -force)", filepath.Join(dir, name))
	}
	t, y := titleYear(filepath.Base(filepath.Clean(dir)))
	if *title != "" {
		t = *title
	}
	if *qf.year == 0 {
		*qf.year = y
	}
	q, err := qf.query(pos[0], t)
	if err != nil {
		return err
	}
	item, err := identify(ctx, b, q)
	if err != nil {
		return err
	}
	if item.Kind == scout.Movie {
		err = nfo.WriteMovie(dir, item)
	} else {
		err = nfo.WriteShow(dir, item)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s (%d) via %s %v\n", name, item.Title, item.Year, item.Provider, item.IDs)
	if !*noArt {
		files, err := nfo.DownloadArtwork(ctx, dir, item)
		for _, f := range files {
			fmt.Println("artwork:", f)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
	}
	return nil
}

func hashCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: scout hash <file>")
	}
	h, size, err := moviehash.File(args[0])
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"hash": h, "size": size})
}

type subFlags struct {
	imdb, tmdb, text, lang *string
	season, episode        *int
}

func addSubFlags(fs *flag.FlagSet) subFlags {
	return subFlags{
		imdb:    fs.String("imdb", "", "IMDb ID (the show's for episodes)"),
		tmdb:    fs.String("tmdb", "", "TMDB ID (the show's for episodes)"),
		text:    fs.String("query", "", "text query (default: from the file name)"),
		lang:    fs.String("lang", "", "comma-separated languages (default: config)"),
		season:  fs.Int("season", 0, "season number"),
		episode: fs.Int("episode", 0, "episode number"),
	}
}

var sxe = regexp.MustCompile(`(?i)s(\d{1,2})e(\d{1,3})`)

func (f subFlags) query(video string) scout.SubtitleQuery {
	q := scout.SubtitleQuery{IMDbID: *f.imdb, TMDbID: *f.tmdb, Season: *f.season, Episode: *f.episode, Query: *f.text}
	if *f.lang != "" {
		q.Languages = strings.Split(*f.lang, ",")
	}
	if video != "" {
		if h, size, err := moviehash.File(video); err == nil {
			q.Hash, q.Size = h, size
		} else {
			fmt.Fprintln(os.Stderr, "warning: no hash:", err)
		}
		base := strings.TrimSuffix(filepath.Base(video), filepath.Ext(video))
		if q.Query == "" {
			q.Query = base
		}
		if m := sxe.FindStringSubmatch(base); m != nil && q.Season == 0 && q.Episode == 0 {
			q.Season, _ = strconv.Atoi(m[1])
			q.Episode, _ = strconv.Atoi(m[2])
			q.Query = strings.TrimSpace(base[:strings.Index(base, m[0])])
		}
	}
	return q
}

func subsSearchCmd(ctx context.Context, b scout.Backend, args []string) error {
	fs := flag.NewFlagSet("subs search", flag.ExitOnError)
	sf := addSubFlags(fs)
	pos := parse(fs, args)
	video := ""
	if len(pos) == 1 {
		video = pos[0]
	} else if len(pos) > 1 {
		return errors.New("usage: scout subs search [<video>] [-imdb tt..] [-query ..] [-lang es,en]")
	}
	subs, err := b.SearchSubtitles(ctx, sf.query(video))
	if err != nil {
		return err
	}
	return printJSON(subs)
}

// subsFetchCmd downloads the best candidate for each language that has no
// <video>.<lang>.srt yet. Best: hash match, then trusted, then most downloaded,
// skipping machine translations when anything else exists.
func subsFetchCmd(ctx context.Context, b scout.Backend, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("subs fetch", flag.ExitOnError)
	sf := addSubFlags(fs)
	force := fs.Bool("force", false, "replace existing subtitle files")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return errors.New("usage: scout subs fetch <video> [-lang es,en]")
	}
	video := pos[0]
	q := sf.query(video)
	if len(q.Languages) == 0 {
		q.Languages = cfg.Local.SubtitleLanguages
	}
	base := strings.TrimSuffix(video, filepath.Ext(video))
	var missing []string
	for _, l := range q.Languages {
		if _, err := os.Stat(base + "." + l + ".srt"); err != nil || *force {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		fmt.Println("nothing to do: all languages present")
		return nil
	}
	q.Languages = missing
	subs, err := b.SearchSubtitles(ctx, q)
	if err != nil {
		return err
	}
	var failed []string
	for _, l := range missing {
		best := pickBest(subs, l)
		if best == nil {
			fmt.Printf("%s: none found\n", l)
			continue
		}
		f, err := b.DownloadSubtitle(ctx, best.Provider, best.FileID)
		if err != nil {
			if errors.Is(err, scout.ErrQuotaExceeded) {
				return err
			}
			failed = append(failed, l)
			fmt.Fprintf(os.Stderr, "%s: %v\n", l, err)
			continue
		}
		dest := base + "." + l + ".srt"
		if err := os.WriteFile(dest, f.Data, 0o644); err != nil {
			return err
		}
		fmt.Printf("%s: %s (matched by %s)\n", l, filepath.Base(dest), best.MatchedBy)
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func pickBest(subs []scout.Subtitle, lang string) *scout.Subtitle {
	return scout.BestSubtitle(subs, lang)
}

func serve(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", cfg.Server.Listen, "address to listen on")
	_ = fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if cfg.Local.TMDB.APIKey == "" {
		log.Warn("no TMDB key: tmdb provider disabled")
	}
	if cfg.Local.OpenSubtitles.APIKey == "" {
		log.Warn("no OpenSubtitles key: subtitle lookups will fail")
	}
	// The server always answers with its own providers: chaining to another
	// service from here would only hide misconfiguration.
	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.Handler(local.New(cfg.Local), cfg.Server.Token, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("scout listening", "addr", *listen, "version", version, "auth", cfg.Server.Token != "")
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "scout:", err)
	code := 1
	if errors.Is(err, scout.ErrNotFound) {
		code = 3
	}
	os.Exit(code)
}
