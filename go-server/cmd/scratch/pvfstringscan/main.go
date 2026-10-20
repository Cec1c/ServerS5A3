// Command pvfstringscan decodes every script entry of a Script.pvf and reports
// the entries whose decoded text contains any of the requested needles.
//
// It is a read-only diagnostic: it never writes the archive back.
//
//	go run ./cmd/scratch/pvfstringscan -pvf Script.pvf -needle pvp02 -needle matching -needle 46688
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

type needleFlag []string

func (n *needleFlag) String() string { return strings.Join(*n, ",") }

func (n *needleFlag) Set(value string) error {
	*n = append(*n, value)
	return nil
}

func main() {
	pvfPath := flag.String("pvf", "", "Script.pvf to scan (read only)")
	var needles needleFlag
	flag.Var(&needles, "needle", "substring to look for (repeatable)")
	pathFilter := flag.String("path", "", "only scan entries whose path contains this substring")
	listOnly := flag.Bool("list", false, "list entry paths that match -path and contain every -needle (no context)")
	contextLines := flag.Int("context", 3, "context lines around a hit")
	maxHits := flag.Int("max", 40, "stop after this many entries with hits")
	progress := flag.Int("progress", 5000, "print progress every N entries (0 = off)")
	flag.Parse()

	if *pvfPath == "" || (len(needles) == 0 && !*listOnly) {
		fmt.Fprintln(os.Stderr, "usage: pvfstringscan -pvf <Script.pvf> -needle <text> [-needle <text>...] [-path <substr>] [-list]")
		os.Exit(2)
	}
	lowered := make([]string, 0, len(needles))
	for _, n := range needles {
		lowered = append(lowered, strings.ToLower(n))
	}

	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *pvfPath, err)
		os.Exit(1)
	}
	files := archive.Files()
	fmt.Printf("archive=%s entries=%d needles=%v path_filter=%q\n", *pvfPath, len(files), needles, *pathFilter)

	scanned, decodeErrors, hitEntries := 0, 0, 0
	for idx := range files {
		path := files[idx].Path
		if *pathFilter != "" && !strings.Contains(strings.ToLower(path), strings.ToLower(*pathFilter)) {
			continue
		}
		text, err := archive.FileText(idx)
		if err != nil {
			decodeErrors++
			continue
		}
		scanned++
		if *progress > 0 && scanned%*progress == 0 {
			fmt.Printf("  ... scanned=%d hits=%d decode_errors=%d last=%s\n", scanned, hitEntries, decodeErrors, path)
		}
		haystack := strings.ToLower(text)
		matched := make([]string, 0, len(needles))
		for i, n := range lowered {
			if strings.Contains(haystack, n) {
				matched = append(matched, needles[i])
			}
		}
		if len(matched) != len(needles) && !(*listOnly && len(needles) == 0) {
			continue
		}
		if *listOnly {
			hitEntries++
			fmt.Printf("%s\tbytes=%d\n", path, len(text))
			if hitEntries >= *maxHits {
				fmt.Printf("\n(stop: reached -max %d hit entries)\n", *maxHits)
				break
			}
			continue
		}
		if len(matched) == 0 {
			continue
		}
		hitEntries++
		fmt.Printf("\n=== %s   matched=%v   text_len=%d\n", path, matched, len(text))
		lines := strings.Split(text, "\n")
		printed := 0
		for i, line := range lines {
			lower := strings.ToLower(line)
			hit := false
			for _, n := range lowered {
				if strings.Contains(lower, n) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			start := i - *contextLines
			if start < 0 {
				start = 0
			}
			end := i + *contextLines
			if end >= len(lines) {
				end = len(lines) - 1
			}
			for j := start; j <= end; j++ {
				marker := "  "
				if j == i {
					marker = ">>"
				}
				fmt.Printf("%s [%5d] %s\n", marker, j+1, truncate(lines[j], 240))
			}
			fmt.Println("   ---")
			printed++
			if printed >= 6 {
				break
			}
		}
		if hitEntries >= *maxHits {
			fmt.Printf("\n(stop: reached -max %d hit entries)\n", *maxHits)
			break
		}
	}
	fmt.Printf("\ndone: scanned=%d decode_errors=%d hit_entries=%d\n", scanned, decodeErrors, hitEntries)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
