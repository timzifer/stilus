package gpu

import (
	"errors"
	"fmt"
	"image"
	"slices"

	"github.com/timzifer/stilus"
)

// Recorder records what a canvas draws onto its target, with buffers of
// its own, so that several canvases can draw onto one target at once: one
// per band or tile, each on its own goroutine, through
// Canvas.ResetCompositor(recorder, band). The target's Flush then draws
// the target's own spans and every recorder's, in the order the recorders
// were made, in one render pass.
//
// While canvases record, nothing may use the target: a recorder touches
// neither the GPU nor its target, so recording is safe on any goroutine,
// one canvas per recorder. Recorders draw disjoint parts of the target,
// as bands do; where they overlap, the order between them is the order of
// NewRecorder. A recorder grows its buffers instead of flushing, up to the
// device's storage binding (ErrRecorderFull beyond, Err); a texture that
// does not fit its store is shaded on the CPU. A layer operation, which
// reads what was drawn before it, is a barrier in the recorder's spans:
// the target's Flush draws the spans before it, copies the part of the
// layer the operation reaches, then draws on, in rounds shared by all
// recorders.
type Recorder struct{ t *Target }

// ErrRecorderFull is reported by Recorder.Err for spans dropped because
// the recorder's buffers reached the device's storage binding.
var ErrRecorderFull = errors.New("gpu: recorder full")

// storeWrite is a write into a recorder's store, made at its target's
// Flush.
type storeWrite struct {
	off int
	b   []byte
}

// NewRecorder makes a recorder drawing onto t.
func (t *Target) NewRecorder() (*Recorder, error) {
	if t.parent != nil {
		return nil, errors.New("gpu: a recorder has no recorders")
	}
	r := &Target{
		d: t.d, w: t.w, h: t.h, tex: t.tex, view: t.view, parent: t,
		masks: map[uint64]int{}, planes: map[*stilus.Plane]int{},
		meshes: t.meshes,
	}
	r.limit = int(t.d.maxBind)
	r.solid.t, r.grad.t, r.shaded.t = r, r, r
	r.grad.flag = gradFlag
	if err := r.initBuffers(); err != nil {
		r.Close()
		return nil, err
	}
	t.recorders = append(t.recorders, r)
	return &Recorder{r}, nil
}

// Blitter implements stilus.Compositor.
func (r *Recorder) Blitter(p *stilus.Paint) stilus.Blitter { return r.t.Blitter(p) }

// Bounds is the target's rectangle.
func (r *Recorder) Bounds() image.Rectangle { return r.t.Bounds() }

// Err returns the first error the recorder met, nil for none.
func (r *Recorder) Err() error { return r.t.err }

// Close releases the recorder's buffers and detaches it from its target,
// dropping what it recorded since the target's last Flush.
func (r *Recorder) Close() {
	t := r.t
	if p := t.parent; p != nil {
		p.recorders = slices.DeleteFunc(p.recorders, func(x *Target) bool { return x == t })
	}
	t.Close()
}

// uploadStore makes a recorder's store writes. When its store had to
// grow, the new store gets every plane the recorder keeps.
func (t *Target) uploadStore(grown bool) error {
	if t.parent == nil {
		return nil
	}
	defer func() { t.pending = t.pending[:0] }()
	if grown {
		for p, off := range t.planes {
			if err := t.d.queue.WriteBuffer(t.store, uint64(off*4), t.planeRGBA(p)); err != nil {
				return fmt.Errorf("gpu: write texture: %w", err)
			}
		}
		return nil
	}
	for _, w := range t.pending {
		if err := t.d.queue.WriteBuffer(t.store, uint64(w.off), w.b); err != nil {
			return fmt.Errorf("gpu: write texture: %w", err)
		}
	}
	return nil
}
