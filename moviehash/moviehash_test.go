package moviehash

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestReader(t *testing.T) {
	// 192 KiB where word i holds i: head words 0..8191, tail words 16384..24575.
	const words = 3 * chunk / 8
	data := make([]byte, words*8)
	for i := 0; i < words; i++ {
		binary.LittleEndian.PutUint64(data[i*8:], uint64(i))
	}
	var want uint64 = uint64(len(data))
	for i := 0; i < chunk/8; i++ {
		want += uint64(i) + uint64(words-chunk/8+i)
	}
	got, err := Reader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got != fmt.Sprintf("%016x", want) {
		t.Fatalf("got %s want %016x", got, want)
	}
}

func TestTooSmall(t *testing.T) {
	if _, err := Reader(bytes.NewReader(make([]byte, 10)), 10); err != ErrTooSmall {
		t.Fatalf("want ErrTooSmall, got %v", err)
	}
}
