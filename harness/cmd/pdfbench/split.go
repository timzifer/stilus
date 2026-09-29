package main

import (
	"strings"

	"github.com/google/pprof/profile"
)

// Time split of a CPU profile into the spec's buckets.
//
// A sample belongs to the first domain bucket (Text, Images, Shadings,
// Transparency) found walking its stack from the root, so glyph
// rasterization counts as text and image decoding through stream filters
// counts as images. Otherwise the leaf-most frame decides between Parser,
// Paths, Color and Interpreter; runtime-only stacks are GC/runtime.
var buckets = []string{"Parser", "Interpreter", "Paths", "Text", "Images", "Shadings", "Transparency", "Color", "GC/runtime", "Other"}

func domain(fn, file string) string {
	base := file[strings.LastIndexByte(file, '/')+1:]
	switch {
	case strings.Contains(fn, "go-pdfkit/render."):
		// By function name: go-pdfkit keeps its general painting routine
		// (paintCoverage) in pattern.go, so file names mislead.
		n := strings.ToLower(fn[strings.LastIndex(fn, "render.")+len("render."):])
		switch {
		case strings.Contains(n, "paintcoverage"), strings.Contains(n, "paintpath"):
			return ""
		case base == "text.go", base == "font.go", base == "substitute.go",
			strings.Contains(n, "glyph"), strings.Contains(n, "text"), strings.Contains(n, "font"):
			return "Text"
		case base == "image.go", base == "images.go", base == "jbig2.go", strings.Contains(n, "image"):
			return "Images"
		case base == "shading.go", base == "mesh.go", base == "patch.go", base == "function.go",
			base == "funckinds.go", base == "calculator.go", strings.Contains(n, "shading"), strings.Contains(n, "tiling"):
			return "Shadings"
		case base == "softmask.go", strings.Contains(n, "group"), strings.Contains(n, "softmask"):
			return "Transparency"
		}
	case strings.Contains(fn, "go-opentype/"), strings.Contains(fn, "go-pdfkit/pdffont"):
		return "Text"
	case strings.Contains(fn, "image/jpeg"), strings.Contains(fn, "go-images/"), strings.Contains(fn, "gobig2"),
		strings.Contains(fn, "x/image/ccitt"), strings.Contains(fn, "go-gfx/gfx/codec"), strings.Contains(fn, "go-gfx/gfx/resample"):
		return "Images"
	}
	return ""
}

func generic(fn, file string) string {
	base := file[strings.LastIndexByte(file, '/')+1:]
	switch {
	case strings.Contains(fn, "go-pdfkit/reader."), strings.HasPrefix(fn, "compress/"):
		return "Parser"
	case strings.Contains(fn, "go-gfx/gfx/color"):
		return "Color"
	case strings.Contains(fn, "go-gfx/gfx/"):
		return "Paths"
	case strings.Contains(fn, "go-pdfkit/render."):
		n := strings.ToLower(fn[strings.LastIndex(fn, "render.")+len("render."):])
		switch {
		case base == "colour.go", base == "space.go", base == "calibrated.go", base == "chroma.go", base == "tripleCache.go":
			return "Color"
		case strings.Contains(n, "paint"), strings.Contains(n, "mask"), strings.Contains(n, "clip"),
			strings.Contains(n, "fill"), strings.Contains(n, "stroke"):
			return "Paths" // rasterization and compositing driven by render
		}
		return "Interpreter"
	}
	return ""
}

// split sums CPU time per bucket (seconds).
func split(p *profile.Profile) map[string]float64 {
	out := map[string]float64{}
	vi := len(p.SampleType) - 1 // cpu nanoseconds
	for _, s := range p.Sample {
		t := float64(s.Value[vi]) / 1e9
		b := ""
		// Root to leaf for domains.
		for i := len(s.Location) - 1; i >= 0 && b == ""; i-- {
			ln := s.Location[i].Line
			for j := len(ln) - 1; j >= 0 && b == ""; j-- {
				if ln[j].Function != nil {
					b = domain(ln[j].Function.Name, ln[j].Function.Filename)
				}
			}
		}
		// Leaf to root for the rest.
		for i := 0; i < len(s.Location) && b == ""; i++ {
			for _, l := range s.Location[i].Line {
				if l.Function != nil {
					if b = generic(l.Function.Name, l.Function.Filename); b != "" {
						break
					}
				}
			}
		}
		if b == "" {
			b = "Other"
			for _, loc := range s.Location {
				for _, l := range loc.Line {
					if l.Function != nil && strings.HasPrefix(l.Function.Name, "runtime.") {
						b = "GC/runtime"
					}
				}
			}
		}
		out[b] += t
	}
	return out
}
