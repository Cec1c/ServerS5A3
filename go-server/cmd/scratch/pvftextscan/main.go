package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// pvftextscan reads every text document in a Script.pvf archive and reports the
// archive paths whose decoded text contains one of the requested needles.
// It exists only as a forensic scratch tool for the 90CN channel/event work.
func main() {
	pvfPath := flag.String("pvf", "/opt/dnf90/data/dnf/Script.pvf", "Script.pvf path")
	prefix := flag.String("prefix", "", "only scan archive paths with this lowercase prefix")
	workers := flag.Int("workers", 16, "parallel readers")
	flag.Parse()
	needles := make([]string, 0, flag.NArg())
	for _, needle := range flag.Args() {
		if needle = strings.TrimSpace(needle); needle != "" {
			needles = append(needles, needle)
		}
	}
	if len(needles) == 0 {
		fmt.Fprintln(os.Stderr, "need at least one needle")
		os.Exit(2)
	}
	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		panic(err)
	}
	type job struct{ path string }
	jobs := make(chan job, 256)
	var scanned, matched int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				text, readErr := archive.ReadText(item.path)
				if readErr != nil {
					continue
				}
				atomic.AddInt64(&scanned, 1)
				for _, needle := range needles {
					index := strings.Index(text, needle)
					if index < 0 {
						continue
					}
					start := index - 60
					if start < 0 {
						start = 0
					}
					end := index + len(needle) + 120
					if end > len(text) {
						end = len(text)
					}
					snippet := strings.NewReplacer("\r", " ", "\n", " | ").Replace(text[start:end])
					mu.Lock()
					fmt.Printf("MATCH needle=%s path=%s off=%d :: %s\n", needle, item.path, index, snippet)
					mu.Unlock()
					atomic.AddInt64(&matched, 1)
					break
				}
			}
		}()
	}
	count := 0
	for _, file := range archive.Files() {
		lower := strings.ToLower(file.ArchivePath)
		if *prefix != "" && !strings.HasPrefix(lower, *prefix) {
			continue
		}
		count++
		jobs <- job{path: file.ArchivePath}
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintf(os.Stderr, "scanned=%d queued=%d matched=%d\n", atomic.LoadInt64(&scanned), count, atomic.LoadInt64(&matched))
}
