// Command benchchart keeps the history of BenchmarkScenes at 150 dpi, one
// entry per commit, and draws it as a chart: commits along x, ms/op along y,
// one line per scene.
//
//	go test -run '^$' -bench '^BenchmarkScenes$/./^150dpi$' -count 5 . |
//	    benchchart record -db bench.json -commit $(git rev-parse HEAD) -subject ... -date ...
//	benchchart render -db bench.json -o bench.svg
//
// CI runs it from .github/workflows/bench.yml and publishes the chart on the
// badges branch.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/timzifer/figure"
	"github.com/timzifer/figure/geom"
	"github.com/timzifer/figure/palette"
	"github.com/timzifer/figure/scale"
)

// Entry is one commit's results: scene name to median ns/op.
type Entry struct {
	Commit  string             `json:"commit"`
	Subject string             `json:"subject"`
	Date    time.Time          `json:"date"`
	Results map[string]float64 `json:"results"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "record":
		err = record(os.Args[2:], os.Stdin)
	case "render":
		err = render(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchchart:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: benchchart record|render [flags]")
	os.Exit(2)
}

func record(args []string, in io.Reader) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	db := fs.String("db", "bench.json", "history file")
	commit := fs.String("commit", "", "commit hash")
	subject := fs.String("subject", "", "commit subject")
	date := fs.String("date", "", "commit date, RFC 3339")
	fs.Parse(args)
	if *commit == "" {
		return errors.New("record: -commit is required")
	}
	when, err := time.Parse(time.RFC3339, *date)
	if err != nil {
		return fmt.Errorf("record: -date: %w", err)
	}
	results, err := parse(in)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New("record: no BenchmarkScenes/*/150dpi results in input")
	}
	hist, err := load(*db)
	if err != nil {
		return err
	}
	hist = upsert(hist, Entry{Commit: *commit, Subject: *subject, Date: when, Results: results})
	return save(*db, hist)
}

// benchLine matches "BenchmarkScenes/<scene>/150dpi-8   30   38123456 ns/op ...".
var benchLine = regexp.MustCompile(`^BenchmarkScenes/(\S+)/150dpi(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op`)

// parse reads `go test -bench` output and returns the median ns/op per scene.
func parse(in io.Reader) (map[string]float64, error) {
	runs := map[string][]float64{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		m := benchLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", sc.Text(), err)
		}
		runs[m[1]] = append(runs[m[1]], v)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(runs))
	for scene, vs := range runs {
		out[scene] = median(vs)
	}
	return out, nil
}

func median(vs []float64) float64 {
	sort.Float64s(vs)
	n := len(vs)
	if n%2 == 1 {
		return vs[n/2]
	}
	return (vs[n/2-1] + vs[n/2]) / 2
}

// upsert replaces the entry for e.Commit, or adds it, and keeps the history in
// commit-date order.
func upsert(hist []Entry, e Entry) []Entry {
	for i := range hist {
		if hist[i].Commit == e.Commit {
			hist[i] = e
			return hist
		}
	}
	hist = append(hist, e)
	sort.SliceStable(hist, func(i, j int) bool { return hist[i].Date.Before(hist[j].Date) })
	return hist
}

func load(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hist []Entry
	if err := json.Unmarshal(b, &hist); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return hist, nil
}

func save(path string, hist []Entry) error {
	b, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func render(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	db := fs.String("db", "bench.json", "history file")
	out := fs.String("o", "bench.svg", "output SVG")
	fs.Parse(args)
	hist, err := load(*db)
	if err != nil {
		return err
	}
	if len(hist) == 0 {
		return fmt.Errorf("render: %s has no entries", *db)
	}
	return chart(hist).Render(figure.SVG(*out))
}

// chart draws one line per scene over the commits in hist, which is in commit
// order. A scene a commit did not have is simply absent from that commit.
func chart(hist []Entry) *figure.Plot {
	commits := make([]string, len(hist))
	sceneSet := map[string]bool{}
	for i, e := range hist {
		commits[i] = short(e.Commit)
		for s := range e.Results {
			sceneSet[s] = true
		}
	}
	scenes := make([]string, 0, len(sceneSet))
	for s := range sceneSet {
		scenes = append(scenes, s)
	}
	sort.Strings(scenes)

	p := figure.New(
		figure.Size(900, 480),
		figure.Title("BenchmarkScenes at 150 dpi, one core"),
		figure.XTitle("commit (main)"),
		figure.YTitle("ms/op"),
	)
	p.X(scale.Ordinal(scale.Categories(commits...)))
	p.Y(scale.Linear(scale.Nice(), scale.Zero()))
	for i, s := range scenes {
		var xs []string
		var ys []float64
		for _, e := range hist {
			if ns, ok := e.Results[s]; ok {
				xs = append(xs, short(e.Commit))
				ys = append(ys, ns/1e6)
			}
		}
		src := figure.NewTable().String("commit", xs).Float64("ms", ys)
		opts := []geom.Option{
			geom.X("commit"), geom.Y("ms"),
			geom.Color(palette.OkabeIto.At(i)),
			geom.Label(s),
		}
		// The palette has eight colours: dash the series that reuse one.
		if i >= len(palette.OkabeIto) {
			opts = append(opts, geom.Dash(6, 4))
		}
		p.Add(geom.Line(src, opts...))
		p.Add(geom.Scatter(src, geom.X("commit"), geom.Y("ms"),
			geom.Color(palette.OkabeIto.At(i)), geom.Size(3), geom.Label(s)))
	}
	return p
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
