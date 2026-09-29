// Command stilus-scenes renders the benchmark scenes, prints timings and
// allocations and optionally writes PNGs for visual inspection.
//
//	go run ./cmd/stilus-scenes -dpi 150 -runs 20 -out /tmp/scenes
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

func main() {
	dpi := flag.Float64("dpi", 150, "resolution")
	runs := flag.Int("runs", 15, "timed runs per scene")
	out := flag.String("out", "", "directory for PNG output (empty: none)")
	threads := flag.Int("threads", 1, "worker goroutines rendering horizontal bands")
	only := flag.String("scene", "", "only scenes whose name contains this")
	flag.Parse()

	size := scenes.Size(*dpi)
	fmt.Printf("page %dx%d px @ %g dpi, %d thread(s), GOMAXPROCS %d\n", size.Dx(), size.Dy(), *dpi, *threads, runtime.GOMAXPROCS(0))
	fmt.Printf("%-24s %10s %10s %12s\n", "scene", "min ms", "median ms", "allocs/run")
	for _, s := range scenes.All() {
		if *only != "" && !strings.Contains(s.Name, *only) {
			continue
		}
		img := image.NewRGBA(size)
		bands := split(size, *threads)
		canvases := make([]*stilus.Canvas, len(bands))
		for i := range canvases {
			canvases[i] = stilus.NewCanvas(img)
		}
		render := func() {
			for i := range img.Pix {
				img.Pix[i] = 0xff
			}
			if len(bands) == 1 {
				canvases[0].Reset(img, bands[0])
				s.Draw(canvases[0], *dpi)
				return
			}
			var wg sync.WaitGroup
			for i := range bands {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					canvases[i].Reset(img, bands[i])
					s.Draw(canvases[i], *dpi)
				}(i)
			}
			wg.Wait()
		}
		render() // warm-up
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		mallocs := ms.Mallocs
		times := make([]float64, 0, *runs)
		for i := 0; i < *runs; i++ {
			t := time.Now()
			render()
			times = append(times, float64(time.Since(t).Microseconds())/1000)
		}
		runtime.ReadMemStats(&ms)
		slices.Sort(times)
		fmt.Printf("%-24s %10.1f %10.1f %12.1f\n", s.Name, times[0], times[len(times)/2],
			float64(ms.Mallocs-mallocs)/float64(*runs))
		for _, c := range canvases {
			if err := c.Err(); err != nil {
				fmt.Println("  error:", err)
			}
		}
		if *out != "" {
			if err := write(filepath.Join(*out, s.Name+".png"), img); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
}

// split divides r into n horizontal bands.
func split(r image.Rectangle, n int) []image.Rectangle {
	if n < 1 {
		n = 1
	}
	out := make([]image.Rectangle, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, image.Rect(r.Min.X, r.Min.Y+r.Dy()*i/n, r.Max.X, r.Min.Y+r.Dy()*(i+1)/n))
	}
	return out
}

func write(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
