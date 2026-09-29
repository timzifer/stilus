// Package corpus fetches the public test corpus and writes the synthetic
// drawing pages.
//
// The corpus is not committed: manifest.json lists each file with its source
// (pdf.js test suite at a pinned commit, arXiv papers at a pinned version)
// and SHA-256; Fetch downloads what is missing into a directory and refuses
// files whose content changed. Customer drawings stay local and are passed
// to the harness as a directory of their own.
package corpus

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed manifest.json
var manifest []byte

// Entry is one corpus file.
type Entry struct {
	Name     string `json:"name"`     // path below the corpus directory
	Category string `json:"category"` // text, transparency, shading, image, scan, vector, paper
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
}

// Manifest returns the corpus entries.
func Manifest() ([]Entry, error) {
	var e []Entry
	return e, json.Unmarshal(manifest, &e)
}

// Fetch downloads missing entries into dir and verifies all of them. log
// receives one line per file.
func Fetch(dir string, log io.Writer) error {
	entries, err := Manifest()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	var failed int
	for _, e := range entries {
		path := filepath.Join(dir, filepath.FromSlash(e.Name))
		data, err := os.ReadFile(path)
		if err != nil {
			data, err = download(client, e.URL)
			if err != nil {
				fmt.Fprintf(log, "FAIL %s: %v\n", e.Name, err)
				failed++
				continue
			}
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		switch {
		case e.SHA256 == "":
			fmt.Fprintf(log, "UNPINNED %s sha256 %s\n", e.Name, got)
		case got != e.SHA256:
			fmt.Fprintf(log, "FAIL %s: sha256 %s, manifest %s\n", e.Name, got, e.SHA256)
			failed++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(log, "ok   %s (%d KB)\n", e.Name, len(data)>>10)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed", failed, len(entries))
	}
	return nil
}

func download(c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stilus-harness/1 (test corpus)")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
