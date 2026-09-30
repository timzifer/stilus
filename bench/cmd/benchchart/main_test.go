package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timzifer/figure"
)

const sample = `goos: linux
goarch: amd64
pkg: github.com/timzifer/stilus
BenchmarkScenes/hatch-2000-hairline/150dpi-4         	      60	  20000000 ns/op	       0 B/op	       0 allocs/op
BenchmarkScenes/hatch-2000-hairline/150dpi-4         	      60	  30000000 ns/op	       0 B/op	       0 allocs/op
BenchmarkScenes/hatch-2000-hairline/150dpi-4         	      60	  10000000 ns/op	       0 B/op	       0 allocs/op
BenchmarkScenes/short-20000-0.35mm/150dpi            	     100	  12345678 ns/op
BenchmarkScenes/short-20000-0.35mm/72dpi-4           	     100	   1000000 ns/op
PASS
`

func TestParse(t *testing.T) {
	got, err := parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"hatch-2000-hairline": 20000000,
		"short-20000-0.35mm":  12345678,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %v, want %v", k, got[k], v)
		}
	}
}

func TestUpsertAndRender(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	var hist []Entry
	hist = upsert(hist, Entry{Commit: "bbbbbbbbbb", Date: day(2), Results: map[string]float64{"a": 2e7, "b": 3e7}})
	hist = upsert(hist, Entry{Commit: "aaaaaaaaaa", Date: day(1), Results: map[string]float64{"a": 4e7}})
	hist = upsert(hist, Entry{Commit: "bbbbbbbbbb", Date: day(2), Results: map[string]float64{"a": 1e7, "b": 3e7}})
	if len(hist) != 2 || hist[0].Commit != "aaaaaaaaaa" || hist[1].Results["a"] != 1e7 {
		t.Fatalf("unexpected history %+v", hist)
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "bench.json")
	if err := save(db, hist); err != nil {
		t.Fatal(err)
	}
	loaded, err := load(db)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("load: %v, %d entries", err, len(loaded))
	}
	if err := chart(loaded).Render(figure.SVG(filepath.Join(dir, "bench.svg"))); err != nil {
		t.Fatal(err)
	}
}
