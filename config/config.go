// Package config loads scout's settings: a YAML file, then SCOUT_* environment
// variables on top. Reel and RMS embed the same structure under their own
// `crawlers:` key.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/pixelotes/scout"
)

// Config is the whole configuration. Backends, Service and Local are what an
// app needs; Server is only read by `scout serve`.
type Config struct {
	// Backends is the order to try: "local", "service" or both.
	Backends []string `yaml:"backends"`
	Service  Service  `yaml:"service"`
	Local    Local    `yaml:"local"`
	Server   Server   `yaml:"server"`
}

// Service points at a scout server.
type Service struct {
	URL     string        `yaml:"url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
	// After this many failures in a row the service is skipped for Cooldown.
	MaxFailures int           `yaml:"max_failures"`
	Cooldown    time.Duration `yaml:"cooldown"`
}

// Local configures the in-process providers.
type Local struct {
	Language  string                  `yaml:"language"`
	Timeout   time.Duration           `yaml:"timeout"`
	Providers map[scout.Kind][]string `yaml:"providers"`
	TMDB      struct {
		APIKey string `yaml:"api_key"`
	} `yaml:"tmdb"`
	Trakt struct {
		ClientID string `yaml:"client_id"`
	} `yaml:"trakt"`
	OpenSubtitles struct {
		APIKey   string `yaml:"api_key"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"opensubtitles"`
	SubtitleLanguages []string `yaml:"subtitle_languages"`
	Cache             Cache    `yaml:"cache"`
}

// Cache keeps metadata in memory and subtitle files on disk.
type Cache struct {
	MetadataTTL time.Duration `yaml:"metadata_ttl"` // 0 disables
	MaxEntries  int           `yaml:"max_entries"`
	SubtitleDir string        `yaml:"subtitle_dir"` // empty disables
}

// Server configures `scout serve`.
type Server struct {
	Listen string `yaml:"listen"`
	Token  string `yaml:"token"` // when set, requests need "Authorization: Bearer <token>"
}

// Default returns a config that works with no keys: local providers only,
// keyless providers first.
func Default() Config {
	c := Config{Backends: []string{"local"}}
	c.Service.Timeout = 10 * time.Second
	c.Service.MaxFailures = 3
	c.Service.Cooldown = 2 * time.Minute
	c.Local.Language = "en"
	c.Local.Timeout = 15 * time.Second
	c.Local.Providers = map[scout.Kind][]string{
		scout.Movie: {"tmdb", "imdb"},
		scout.Show:  {"tvmaze", "tmdb"},
		scout.Anime: {"tvmaze", "anilist", "kitsu", "tmdb"},
	}
	c.Local.SubtitleLanguages = []string{"en"}
	c.Local.Cache.MetadataTTL = 24 * time.Hour
	c.Local.Cache.MaxEntries = 5000
	c.Server.Listen = ":8090"
	return c
}

// Load reads path (if not empty) over the defaults, then applies env vars.
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, err
		}
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	c.applyEnv()
	return c, c.Validate()
}

func (c *Config) applyEnv() {
	set := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	list := func(dst *[]string, key string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = splitList(v)
		}
	}
	list(&c.Backends, "SCOUT_BACKENDS")
	set(&c.Service.URL, "SCOUT_SERVICE_URL")
	set(&c.Service.Token, "SCOUT_SERVICE_TOKEN")
	set(&c.Local.Language, "SCOUT_LANGUAGE")
	set(&c.Local.TMDB.APIKey, "SCOUT_TMDB_API_KEY")
	set(&c.Local.Trakt.ClientID, "SCOUT_TRAKT_CLIENT_ID")
	set(&c.Local.OpenSubtitles.APIKey, "SCOUT_OPENSUBTITLES_API_KEY")
	set(&c.Local.OpenSubtitles.Username, "SCOUT_OPENSUBTITLES_USERNAME")
	set(&c.Local.OpenSubtitles.Password, "SCOUT_OPENSUBTITLES_PASSWORD")
	list(&c.Local.SubtitleLanguages, "SCOUT_SUBTITLE_LANGUAGES")
	set(&c.Local.Cache.SubtitleDir, "SCOUT_SUBTITLE_CACHE_DIR")
	set(&c.Server.Listen, "SCOUT_LISTEN")
	set(&c.Server.Token, "SCOUT_TOKEN")
	for _, k := range []scout.Kind{scout.Movie, scout.Show, scout.Anime} {
		if v, ok := os.LookupEnv("SCOUT_" + strings.ToUpper(string(k)) + "_PROVIDERS"); ok {
			c.Local.Providers[k] = splitList(v)
		}
	}
}

// Validate checks the backend list.
func (c Config) Validate() error {
	if len(c.Backends) == 0 {
		return fmt.Errorf("config: backends is empty; use local, service or both")
	}
	for _, b := range c.Backends {
		switch b {
		case "local":
		case "service":
			if c.Service.URL == "" {
				return fmt.Errorf("config: backend service needs service.url")
			}
		default:
			return fmt.Errorf("config: unknown backend %q (want local or service)", b)
		}
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
