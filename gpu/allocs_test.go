package gpu_test

import (
	"testing"

	"github.com/timzifer/stilus"
	"github.com/timzifer/stilus/internal/scenes"
)

// TestAllocs: recording a warm scene onto a target does not allocate,
// whatever its paints, layers included. A Flush allocates only in wgpu;
// its count is logged, not gated (issue #63).
func TestAllocs(t *testing.T) {
	d := device(t)
	const dpi = 72
	size := scenes.Size(dpi)
	tg, err := d.NewTarget(size.Dx(), size.Dy())
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	var c stilus.Canvas
	for _, s := range scenes.All() {
		draw := func() {
			c.ResetCompositor(tg, tg.Bounds())
			s.Draw(&c, dpi)
		}
		for range 3 {
			draw()
			tg.Flush()
		}
		rec := testing.AllocsPerRun(50, func() {
			draw()
			tg.Clear(white) // drops what was recorded, without the GPU
		})
		flush := testing.AllocsPerRun(5, func() {
			draw()
			tg.Flush()
		})
		t.Logf("%-24s record %6.0f  record+flush %6.0f", s.Name, rec, flush)
		if rec != 0 {
			t.Errorf("%s: recording allocates %.0f times", s.Name, rec)
		}
	}
}
