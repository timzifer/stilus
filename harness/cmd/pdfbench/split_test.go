package main

import (
	"testing"

	"github.com/google/pprof/profile"
)

func TestSplit(t *testing.T) {
	fn := func(name, file string) *profile.Location {
		return &profile.Location{Line: []profile.Line{{Function: &profile.Function{Name: name, Filename: file}}}}
	}
	root := fn("github.com/go-pdfkit/render.(*renderer).run", "/x/render/exec.go")
	cases := []struct {
		stack []*profile.Location // leaf first
		want  string
	}{
		{[]*profile.Location{fn("github.com/go-gfx/gfx/vector.over", "/x/vector/c.go"), fn("github.com/go-pdfkit/render.(*renderer).paintCoverage", "/x/render/pattern.go"), root}, "Paths"},
		{[]*profile.Location{fn("github.com/go-gfx/gfx/vector.over", "/x/vector/c.go"), fn("github.com/go-pdfkit/render.(*renderer).showText", "/x/render/text.go"), root}, "Text"},
		{[]*profile.Location{fn("compress/flate.(*decompressor).huffmanBlock", "/go/flate.go"), fn("github.com/go-pdfkit/render.(*renderer).drawImage", "/x/render/image.go"), root}, "Images"},
		{[]*profile.Location{fn("runtime.mallocgc", "/go/malloc.go"), fn("github.com/go-pdfkit/reader.(*ContentScanner).Next", "/x/reader/content.go"), root}, "Parser"},
		{[]*profile.Location{fn("github.com/go-pdfkit/render.(*renderer).axialShading", "/x/render/shading.go"), root}, "Shadings"},
		{[]*profile.Location{fn("github.com/go-pdfkit/render.(*clip).at", "/x/render/state.go"), root}, "Paths"},
		{[]*profile.Location{fn("runtime.gcDrain", "/go/mgc.go"), fn("runtime.gcBgMarkWorker", "/go/mgc.go")}, "GC/runtime"},
	}
	for i, c := range cases {
		p := &profile.Profile{SampleType: []*profile.ValueType{{}, {}}, Sample: []*profile.Sample{{Location: c.stack, Value: []int64{1, 1e9}}}}
		got := split(p)
		if got[c.want] != 1 {
			t.Errorf("case %d: %v, want %s", i, got, c.want)
		}
	}
}
