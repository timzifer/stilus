package corpus

import (
	"io"
	"os"
	"testing"

	"github.com/timzifer/stilus/internal/scenes"
)

func TestWriteScenes(t *testing.T) {
	names, err := WriteScenes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no scene written")
	}
	for _, n := range names {
		if fi, err := os.Stat(n); err != nil || fi.Size() == 0 {
			t.Errorf("%s: %v, empty or missing", n, err)
		}
	}
}

func TestScenePDFRefusesDrawOps(t *testing.T) {
	for _, s := range scenes.All() {
		if Writable(s) {
			continue
		}
		if err := ScenePDF(io.Discard, s); err == nil {
			t.Errorf("%s: ScenePDF accepted a scene it cannot write", s.Name)
		}
	}
}
