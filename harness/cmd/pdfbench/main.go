// Command pdfbench is the measurement harness of the PDF renderer spec (M0):
// it renders a corpus with several engines, times them, counts allocations,
// compares every page against the reference (PDFium via WebAssembly) and
// splits the CPU time of go-pdfkit into parser, paths, text, images, ….
//
//	pdfbench fetch  -dir corpus            download and verify the public corpus
//	pdfbench scenes -dir corpus            write the synthetic drawing pages
//	pdfbench run    -corpus corpus,~/zeichnungen -dpi 150 -out report
package main

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/pprof/profile"

	"github.com/timzifer/stilus/harness/corpus"
	"github.com/timzifer/stilus/harness/engine"
	"github.com/timzifer/stilus/harness/metrics"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "fetch":
		fl := flag.NewFlagSet("fetch", flag.ExitOnError)
		dir := fl.String("dir", "corpus", "corpus directory")
		fl.Parse(os.Args[2:])
		err = corpus.Fetch(*dir, os.Stdout)
	case "scenes":
		fl := flag.NewFlagSet("scenes", flag.ExitOnError)
		dir := fl.String("dir", "corpus", "corpus directory")
		fl.Parse(os.Args[2:])
		var names []string
		names, err = corpus.WriteScenes(*dir)
		for _, n := range names {
			fmt.Println("wrote", n)
		}
	case "run":
		err = run(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pdfbench:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: pdfbench fetch|scenes|run [flags]  (pdfbench run -h for details)")
	os.Exit(2)
}

type config struct {
	dirs     []string
	engines  []string
	ref      string
	dpi      float64
	runs     int
	maxPages int
	out      string
	images   bool
	split    bool
	filter   string
	profiles string
}

// row is one page rendered by one engine.
type row struct {
	File, Category, Engine string
	Page                   int
	W, H                   int
	MsMin, MsMedian        float64
	Mallocs, AllocBytes    float64 // per render; -1 when not on the Go heap
	Err                    string
	Cmp                    *metrics.Result
}

func run(args []string) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	var c config
	dirs := fl.String("corpus", "corpus", "comma-separated directories searched for *.pdf")
	engines := fl.String("engines", "pdfkit,pdfium", "engines to run")
	fl.StringVar(&c.ref, "ref", "pdfium", "reference engine for the comparison")
	fl.Float64Var(&c.dpi, "dpi", 150, "resolution (integer for pdfium)")
	fl.IntVar(&c.runs, "runs", 3, "timed renders per page (after one warm-up)")
	fl.IntVar(&c.maxPages, "pages", 5, "pages per document (0: all)")
	fl.StringVar(&c.out, "out", "report", "output directory")
	fl.BoolVar(&c.images, "images", false, "also write every rendering as PNG")
	fl.BoolVar(&c.split, "split", true, "profile go-pdfkit and split its time into buckets")
	fl.StringVar(&c.filter, "match", "", "only files whose path contains this")
	fl.StringVar(&c.profiles, "profiles", "", "directory for the raw go-pdfkit CPU profiles (optional)")
	fl.Parse(args)
	c.dirs = strings.Split(*dirs, ",")
	c.engines = strings.Split(*engines, ",")
	if !slices.Contains(c.engines, c.ref) {
		c.engines = append(c.engines, c.ref)
	}

	files, err := findPDFs(c.dirs, c.filter)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no PDFs in %v (run pdfbench fetch / scenes first)", c.dirs)
	}
	if err := os.MkdirAll(filepath.Join(c.out, "diffs"), 0o755); err != nil {
		return err
	}
	cats := categories(c.dirs)

	engs := make([]engine.Engine, len(c.engines))
	for i, n := range c.engines {
		if engs[i], err = engine.New(n); err != nil {
			return err
		}
		defer engs[i].Close()
	}

	var rows []row
	splitTotal := map[string]float64{}
	splitPerFile := map[string]map[string]float64{}
	for fi, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		name := relName(c.dirs, file)
		fmt.Printf("[%d/%d] %s\n", fi+1, len(files), name)
		imgs := map[string][]*image.Gray{} // engine -> page -> grey
		pageRows := map[string][]int{}     // engine -> row index per page
		for _, e := range engs {
			var prof bytes.Buffer
			profiling := c.split && e.Name() == "pdfkit" && pprof.StartCPUProfile(&prof) == nil
			rs, gs := renderDoc(e, data, c, name, cats[file], file)
			if profiling {
				pprof.StopCPUProfile()
				if c.profiles != "" {
					os.MkdirAll(c.profiles, 0o755)
					os.WriteFile(filepath.Join(c.profiles, flatName(name)+".pprof"), prof.Bytes(), 0o644)
				}
				if p, err := profile.Parse(&prof); err == nil {
					sp := split(p)
					splitPerFile[name] = sp
					for k, v := range sp {
						splitTotal[k] += v
					}
				}
			}
			for _, r := range rs {
				pageRows[e.Name()] = append(pageRows[e.Name()], len(rows))
				rows = append(rows, r)
			}
			imgs[e.Name()] = gs
		}
		// Compare with the reference.
		ref := imgs[c.ref]
		for _, e := range engs {
			if e.Name() == c.ref {
				continue
			}
			for p, g := range imgs[e.Name()] {
				if g == nil || p >= len(ref) || ref[p] == nil {
					continue
				}
				m := metrics.Compare(ref[p], g)
				ri := pageRows[e.Name()][p]
				rows[ri].Cmp = &m
				if !m.Pass() {
					base := fmt.Sprintf("%s_p%d_%s", flatName(name), p+1, e.Name())
					writePNG(filepath.Join(c.out, "diffs", base+"_diff.png"), metrics.Diff(ref[p], g))
				}
			}
		}
	}
	if err := writeCSV(filepath.Join(c.out, "results.csv"), rows); err != nil {
		return err
	}
	rep := report(c, rows, splitTotal, splitPerFile)
	if err := os.WriteFile(filepath.Join(c.out, "report.md"), []byte(rep), 0o644); err != nil {
		return err
	}
	fmt.Println("\n" + rep)
	return nil
}

// renderDoc renders the first pages of a document with one engine. Panics
// are recovered per page and reported as errors.
func renderDoc(e engine.Engine, data []byte, c config, name, cat, file string) ([]row, []*image.Gray) {
	doc, err := func() (d engine.Doc, err error) {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
		}()
		return e.Open(data)
	}()
	if err != nil {
		return []row{{File: name, Category: cat, Engine: e.Name(), Page: 1, Err: "open: " + err.Error()}}, nil
	}
	defer doc.Close()
	n := doc.Pages()
	if c.maxPages > 0 {
		n = min(n, c.maxPages)
	}
	var rows []row
	var grays []*image.Gray
	for p := 0; p < n; p++ {
		r := row{File: name, Category: cat, Engine: e.Name(), Page: p + 1, Mallocs: -1, AllocBytes: -1}
		render := func() (img image.Image, d time.Duration, err error) {
			defer func() {
				if v := recover(); v != nil {
					err = fmt.Errorf("panic: %v", v)
				}
			}()
			t := time.Now()
			img, err = doc.Render(p, c.dpi)
			return img, time.Since(t), err
		}
		img, _, err := render() // warm-up
		var times []float64
		if img != nil {
			for i := 0; i < c.runs; i++ {
				var ms0, ms1 runtime.MemStats
				if e.GoHeap() && i == 0 {
					runtime.ReadMemStats(&ms0)
				}
				var d time.Duration
				img, d, err = render()
				if e.GoHeap() && i == 0 {
					runtime.ReadMemStats(&ms1)
					r.Mallocs = float64(ms1.Mallocs - ms0.Mallocs)
					r.AllocBytes = float64(ms1.TotalAlloc - ms0.TotalAlloc)
				}
				times = append(times, float64(d.Microseconds())/1000)
			}
		}
		if err != nil {
			r.Err = err.Error()
		}
		var g *image.Gray
		if img != nil {
			g = metrics.Gray(img)
			r.W, r.H = g.Rect.Dx(), g.Rect.Dy()
			if c.images {
				writePNG(filepath.Join(c.out, "images", fmt.Sprintf("%s_p%d_%s.png", flatName(name), p+1, e.Name())), img)
			}
		}
		if len(times) > 0 {
			slices.Sort(times)
			r.MsMin, r.MsMedian = times[0], times[len(times)/2]
		}
		rows = append(rows, r)
		grays = append(grays, g)
	}
	return rows, grays
}

func findPDFs(dirs []string, match string) ([]string, error) {
	var out []string
	for _, d := range dirs {
		d = expand(d)
		err := filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") && strings.Contains(p, match) {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(out)
	return out, nil
}

// categories maps corpus files to their manifest category; synthetic pages
// are "drawing", anything else (customer drawings) is "local".
func categories(dirs []string) map[string]string {
	out := map[string]string{}
	entries, _ := corpus.Manifest()
	for _, d := range dirs {
		d = expand(d)
		for _, e := range entries {
			out[filepath.Join(d, filepath.FromSlash(e.Name))] = e.Category
		}
		files, _ := findPDFs([]string{d}, "")
		for _, f := range files {
			if _, ok := out[f]; !ok {
				if strings.Contains(f, string(filepath.Separator)+"synthetic"+string(filepath.Separator)) {
					out[f] = "drawing"
				} else {
					out[f] = "local"
				}
			}
		}
	}
	return out
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func relName(dirs []string, file string) string {
	for _, d := range dirs {
		if r, err := filepath.Rel(expand(d), file); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return file
}

func flatName(n string) string {
	return strings.NewReplacer("/", "_", " ", "_").Replace(strings.TrimSuffix(n, filepath.Ext(n)))
}

func writePNG(path string, img image.Image) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		return
	}
	png.Encode(f, img)
	f.Close()
}

func writeCSV(path string, rows []row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"file", "category", "page", "engine", "width", "height", "ms_min", "ms_median",
		"mallocs", "alloc_bytes", "mean_diff", "over32", "max_diff", "ssim", "pass", "error"})
	ff := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, r := range rows {
		rec := []string{r.File, r.Category, strconv.Itoa(r.Page), r.Engine, strconv.Itoa(r.W), strconv.Itoa(r.H),
			ff(r.MsMin), ff(r.MsMedian), ff(r.Mallocs), ff(r.AllocBytes), "", "", "", "", "", r.Err}
		if m := r.Cmp; m != nil {
			rec[10], rec[11], rec[12], rec[13], rec[14] = ff(m.Mean), ff(m.Over32), strconv.Itoa(m.Max), ff(m.SSIM), strconv.FormatBool(m.Pass())
		}
		w.Write(rec)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return s[min(len(s)-1, int(math.Ceil(p*float64(len(s))))-1)]
}

func report(c config, rows []row, splitTotal map[string]float64, splitPerFile map[string]map[string]float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# pdfbench report\n\n%s · %g dpi · %d timed runs/page after warm-up · max %d pages/document · %s/%s, %d CPUs · reference: %s\n\n",
		time.Now().Format("2006-01-02 15:04"), c.dpi, c.runs, c.maxPages, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), c.ref)

	b.WriteString("## Speed and allocations\n\n| engine | pages | unsupported | errors | median ms/page | p90 ms/page | total ms | median allocs/page | median MB alloc/page |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, e := range c.engines {
		var ms, al, by []float64
		var pages, errs, unsup int
		var total float64
		for _, r := range rows {
			if r.Engine != e {
				continue
			}
			if strings.Contains(r.Err, engine.ErrNoPDF.Error()) {
				unsup++
				continue
			}
			pages++
			if r.Err != "" {
				errs++
			}
			if r.MsMin > 0 {
				ms = append(ms, r.MsMin)
				total += r.MsMin
			}
			if r.Mallocs >= 0 {
				al = append(al, r.Mallocs)
				by = append(by, r.AllocBytes/(1<<20))
			}
		}
		alS, byS := "n/a (wasm)", "n/a"
		if len(al) > 0 {
			alS, byS = fmt.Sprintf("%.0f", median(al)), fmt.Sprintf("%.1f", median(by))
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.1f | %.1f | %.0f | %s | %s |\n", e, pages, unsup, errs, median(ms), pct(ms, 0.9), total, alS, byS)
	}

	b.WriteString("\n## Accuracy against " + c.ref + "\n\nTarget: mean < 1/255 and < 0.5 % of pixels deviating by more than 32/255.\n\n| engine | category | pages | pass | mean dev. (median) | >32/255 (median) | SSIM (median) |\n|---|---|---:|---:|---:|---:|---:|\n")
	for _, e := range c.engines {
		if e == c.ref {
			continue
		}
		bycat := map[string][]*metrics.Result{}
		for _, r := range rows {
			if r.Engine == e && r.Cmp != nil {
				bycat[r.Category] = append(bycat[r.Category], r.Cmp)
				bycat["(all)"] = append(bycat["(all)"], r.Cmp)
			}
		}
		for _, cat := range slices.Sorted(mapsKeys(bycat)) {
			ms := bycat[cat]
			var mean, over, ss []float64
			pass := 0
			for _, m := range ms {
				mean = append(mean, m.Mean)
				over = append(over, m.Over32*100)
				ss = append(ss, m.SSIM)
				if m.Pass() {
					pass++
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %.2f | %.2f %% | %.4f |\n", e, cat, len(ms), pass, median(mean), median(over), median(ss))
		}
	}

	if len(splitTotal) > 0 {
		var sum float64
		for _, v := range splitTotal {
			sum += v
		}
		b.WriteString("\n## Where go-pdfkit spends its time (CPU profile, all pages)\n\n| bucket | share |\n|---|---:|\n")
		for _, k := range buckets {
			if v := splitTotal[k]; v > 0 {
				fmt.Fprintf(&b, "| %s | %.1f %% |\n", k, 100*v/sum)
			}
		}
		b.WriteString("\nPer file (share of that file's CPU time):\n\n| file | " + strings.Join(buckets, " | ") + " |\n|---|" + strings.Repeat("---:|", len(buckets)) + "\n")
		for _, f := range slices.Sorted(mapsKeys(splitPerFile)) {
			sp := splitPerFile[f]
			var s float64
			for _, v := range sp {
				s += v
			}
			fmt.Fprintf(&b, "| %s |", f)
			for _, k := range buckets {
				if s > 0 && sp[k] > 0 {
					fmt.Fprintf(&b, " %.0f %% |", 100*sp[k]/s)
				} else {
					b.WriteString(" |")
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## Pages\n\n| file | page | engine | size | ms (min) | allocs | mean dev. | >32/255 | SSIM | note |\n|---|---:|---|---|---:|---:|---:|---:|---:|---|\n")
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b row) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Page, b.Page), cmp.Compare(a.Engine, b.Engine))
	})
	for _, r := range sorted {
		al := ""
		if r.Mallocs >= 0 {
			al = fmt.Sprintf("%.0f", r.Mallocs)
		}
		mean, over, ss := "", "", ""
		if m := r.Cmp; m != nil {
			mean, over, ss = fmt.Sprintf("%.2f", m.Mean), fmt.Sprintf("%.2f %%", m.Over32*100), fmt.Sprintf("%.4f", m.SSIM)
			if m.SizeA != m.SizeB {
				r.Err = strings.TrimSpace(r.Err + fmt.Sprintf(" size %v vs ref %v", m.SizeB, m.SizeA))
			}
		}
		note := strings.ReplaceAll(r.Err, "|", "/")
		if len(note) > 80 {
			note = note[:80] + "…"
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %d×%d | %.1f | %s | %s | %s | %s | %s |\n", r.File, r.Page, r.Engine, r.W, r.H, r.MsMin, al, mean, over, ss, note)
	}
	return b.String()
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
