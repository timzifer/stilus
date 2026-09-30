package stilus_test

import (
	"fmt"
	"image"
	"runtime"
	"sync"
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

func clear32(img *image.RGBA) {
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
}

// BenchmarkScenes renders each scene on one core. The spec's acceptance
// targets at 150 dpi: hatch-2000 ≤ 60 ms, short-20000 ≤ 40 ms, 0 allocs.
func BenchmarkScenes(b *testing.B) {
	for _, dpi := range []float64{72, 150, 300} {
		for _, s := range scenes.All() {
			b.Run(fmt.Sprintf("%s/%gdpi", s.Name, dpi), func(b *testing.B) {
				img := image.NewRGBA(scenes.Size(dpi))
				c := stilus.NewCanvas(img)
				s.Draw(c, dpi) // warm-up
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					c.Reset(img, img.Bounds())
					s.Draw(c, dpi)
				}
			})
		}
	}
}

// BenchmarkScenesParallel splits the page into horizontal bands, one canvas
// per worker, each playing the full operation list.
func BenchmarkScenesParallel(b *testing.B) {
	const dpi = 150
	workers := runtime.GOMAXPROCS(0)
	for _, s := range scenes.All() {
		b.Run(fmt.Sprintf("%s/%d-workers", s.Name, workers), func(b *testing.B) {
			img := image.NewRGBA(scenes.Size(dpi))
			bands := splitBands(img.Bounds(), workers*2)
			canvases := make([]*stilus.Canvas, len(bands))
			for i := range canvases {
				canvases[i] = stilus.NewCanvas(img)
			}
			render := func() {
				var wg sync.WaitGroup
				next := make(chan int, len(bands))
				for i := range bands {
					next <- i
				}
				close(next)
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := range next {
							c := canvases[i]
							c.Reset(img, bands[i])
							s.Draw(c, dpi)
						}
					}()
				}
				wg.Wait()
			}
			render()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				render()
			}
		})
	}
}

func splitBands(r image.Rectangle, n int) []image.Rectangle {
	out := make([]image.Rectangle, 0, n)
	for i := 0; i < n; i++ {
		y0 := r.Min.Y + r.Dy()*i/n
		y1 := r.Min.Y + r.Dy()*(i+1)/n
		out = append(out, image.Rect(r.Min.X, y0, r.Max.X, y1))
	}
	return out
}

func TestScenesNoAllocs(t *testing.T) {
	for _, s := range scenes.All() {
		img := image.NewRGBA(scenes.Size(72))
		c := stilus.NewCanvas(img)
		s.Draw(c, 72)
		allocs := testing.AllocsPerRun(3, func() {
			c.Reset(img, img.Bounds())
			s.Draw(c, 72)
		})
		if allocs != 0 {
			t.Errorf("%s: %.1f allocs per render", s.Name, allocs)
		}
		if err := c.Err(); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
}
