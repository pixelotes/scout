// Package moviehash computes the OpenSubtitles (OSDb) hash: the file size plus
// the sum of the first and last 64 KiB read as little-endian uint64 words.
// It only reads 128 KiB, so it is cheap even for large files.
package moviehash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const chunk = 64 * 1024

// ErrTooSmall is returned for files under 64 KiB, which have no OSDb hash.
var ErrTooSmall = errors.New("moviehash: file smaller than 64 KiB")

// File hashes the file at path and also returns its size.
func File(path string) (hash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h, err := Reader(f, st.Size())
	return h, st.Size(), err
}

// Reader hashes size bytes readable from r.
func Reader(r io.ReaderAt, size int64) (string, error) {
	if size < chunk {
		return "", ErrTooSmall
	}
	sum := uint64(size)
	buf := make([]byte, chunk)
	for _, off := range []int64{0, size - chunk} {
		if _, err := r.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		for i := 0; i < chunk; i += 8 {
			sum += binary.LittleEndian.Uint64(buf[i:])
		}
	}
	return fmt.Sprintf("%016x", sum), nil
}
