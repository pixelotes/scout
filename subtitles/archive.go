package subtitles

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxSubtitle caps one extracted file; real subtitles are well under 1 MiB.
const maxSubtitle = 5 << 20

var errNoSRT = errors.New("archive has no .srt file")

// extractSRT returns the .srt to use from data, which may be a plain
// subtitle or a ZIP. With a season and episode, it prefers the file
// named for that episode (season packs); otherwise the largest .srt. The
// result is converted to UTF-8.
func extractSRT(data []byte, season, episode int) (name string, out []byte, err error) {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07")):
		// Neither subdivx nor SubDL serve RAR today (subdivx repacks as ZIP),
		// so it is not worth a dependency.
		return "", nil, errors.New("RAR archives are not supported")
	default:
		// Not an archive: treat as the subtitle itself.
		return "", toUTF8(data), nil
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", nil, fmt.Errorf("zip: %w", err)
	}
	// Choose by name and size first, then decompress only that file: a
	// season pack can hold dozens of subtitles.
	var files []*zip.File
	for _, f := range zr.File {
		if isSRT(f.Name) && f.UncompressedSize64 <= maxSubtitle {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return "", nil, errNoSRT
	}
	larger := func(a, b *zip.File) bool { return b == nil || a.UncompressedSize64 > b.UncompressedSize64 }

	var best *zip.File
	if season > 0 || episode > 0 {
		for _, f := range files {
			if matchesEpisode(f.Name, season, episode) && larger(f, best) {
				best = f
			}
		}
		// Anime packs number episodes on their own: "Show_-_01_(1080p).srt".
		if best == nil {
			for _, f := range files {
				if matchesAbsolute(f.Name, episode) {
					if best != nil {
						return "", nil, fmt.Errorf("season pack has several files for episode %d", episode)
					}
					best = f
				}
			}
		}
		if best == nil && len(files) > 1 {
			return "", nil, fmt.Errorf("season pack has no file for S%02dE%02d", season, episode)
		}
	}
	if best == nil {
		for _, f := range files {
			if larger(f, best) {
				best = f
			}
		}
	}

	rc, err := best.Open()
	if err != nil {
		return "", nil, fmt.Errorf("zip: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxSubtitle))
	if err != nil {
		return "", nil, fmt.Errorf("zip: %w", err)
	}
	return path.Base(strings.ReplaceAll(best.Name, "\\", "/")), toUTF8(b), nil
}

func isSRT(name string) bool {
	return strings.EqualFold(path.Ext(name), ".srt") && !strings.HasPrefix(path.Base(name), "._")
}

var epPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)s(\d{1,2})[ ._-]?e(\d{1,3})`),
	regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`),
}

// matchesEpisode reports whether a file name or release title is for the
// given episode (S01E02, s1e2, 1x02).
func matchesEpisode(name string, season, episode int) bool {
	for _, re := range epPatterns {
		for _, m := range re.FindAllStringSubmatch(name, -1) {
			s, _ := strconv.Atoi(m[1])
			e, _ := strconv.Atoi(m[2])
			if s == season && e == episode {
				return true
			}
		}
	}
	return false
}

// absRe finds a bare episode number between separators, e.g. "_-_01_",
// " - 12 ", "[03]", "E07"; "1080p", "x265" and "10bit" do not match.
var absRe = regexp.MustCompile(`(?i)(?:^|[\s_.\-\[(])(?:e|ep)?0*(\d{1,3})(?:v\d)?(?:[\s_.\-\])]|$)`)

func matchesAbsolute(name string, episode int) bool {
	base := strings.TrimSuffix(path.Base(strings.ReplaceAll(name, "\\", "/")), path.Ext(name))
	for _, m := range absRe.FindAllStringSubmatch(base, -1) {
		if n, _ := strconv.Atoi(m[1]); n == episode {
			return true
		}
	}
	return false
}

// toUTF8 converts Windows-1252 (the usual encoding of Spanish subtitles that
// are not UTF-8) to UTF-8, and drops a UTF-8 BOM. Valid UTF-8 is kept.
func toUTF8(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if utf8.Valid(b) {
		return b
	}
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/8)
	for _, c := range b {
		switch {
		case c < 0x80:
			out.WriteByte(c)
		case c < 0xa0:
			out.WriteRune(cp1252[c-0x80])
		default:
			out.WriteRune(rune(c)) // Latin-1 range matches Unicode
		}
	}
	return out.Bytes()
}

// cp1252 maps bytes 0x80-0x9F; the rest of Windows-1252 matches Latin-1.
var cp1252 = [32]rune{
	'€', '\u0081', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '\u008d', 'Ž', '\u008f',
	'\u0090', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '\u009d', 'ž', 'Ÿ',
}
