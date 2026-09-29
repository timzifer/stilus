// Package stilus is a sparse CPU rasterizer for 2D vector graphics.
//
// Its cost per path is the length of the path's edges in pixels plus the
// covered spans, never the area of the bounding box or the width of the
// canvas. It knows nothing about PDF, fonts or color spaces and has no
// dependencies, so it can serve as the CPU backend of a PDF renderer, a UI
// toolkit or another 2D library (for example as the CPU filler behind
// gogpu/gg).
//
// # Layers
//
// Each layer can be used on its own:
//
//   - Rasterizer turns device-space paths (Fill, or AddLine/AddPath and
//     Rasterize) into coverage spans delivered to a Blitter: BlitRun for
//     constant interior runs, BlitCoverage for antialiased edge pixels.
//     Implement Blitter to composite into any pixel format.
//   - Stroker turns a stroke (width, caps, joins, miter limit, dashes; all
//     in user space, exact under anisotropic transforms) into fill geometry
//     for a LineSink, such as a Rasterizer or a PathSink.
//   - Canvas combines both with hairlines, a clip stack and compositing
//     onto a caller-owned *image.RGBA. Its methods map one to one onto a
//     display-list device: Fill, Stroke, ClipPath, ClipRect, PopClip.
//
// # Performance model
//
// Edges are accumulated as signed area and cover into a band of cells
// (32 rows × clip width) in 24.8 fixed point. Per row, the touched cell
// range is integrated directly when it is narrow; wide rows are swept with
// a dirty bitset that skips untouched cells, and the constant stretches
// between edges are emitted as runs that blitters fill without a
// per-pixel loop. Nothing is cleared as a whole: the sweep zeroes what it
// reads.
//
// Strokes thinner than one device pixel take an analytic hairline path
// that writes coverage rows directly. Rectangle clips cost nothing per
// pixel; other clips are rasterized once into a mask over their bounds.
//
// All buffers grow on demand and are retained: after warm-up, drawing
// performs no allocations. Rasterizer, Stroker and Canvas are not safe for
// concurrent use; use one per worker and give each worker its own band or
// tile of the destination (Canvas.Reset takes a region).
//
// # Robustness
//
// NaN and infinite coordinates are rejected, huge ones are clipped
// analytically, curve and arc subdivision and dash counts are bounded, and
// the edge count per path is budgeted (Rasterizer.MaxEdges). Canvas never
// panics; it reports problems through Err and keeps drawing.
package stilus
