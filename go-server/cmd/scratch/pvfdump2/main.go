// Command pvfdump prints the decoded text of archive entries whose path matches
// a prefix. Read-only diagnostic.
//
//	go run ./cmd/scratch/pvfdump -pvf Script.pvf -prefix ui/pvp02/mission
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	pvfPath := flag.String("pvf", "", "Script.pvf to read (read only)")
	prefix := flag.String("prefix", "", "ArchivePath prefix to dump")
	limit := flag.Int("limit", 8, "max entries to dump")
	maxBytes := flag.Int("max", 4000, "max bytes of text per entry")
	flag.Parse()
	if *pvfPath == "" || *prefix == "" {
		fmt.Fprintln(os.Stderr, "usage: pvfdump -pvf <Script.pvf> -prefix <archive/path> [-limit n]")
		os.Exit(2)
	}
	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	lower := strings.ToLower(*prefix)
	shown := 0
	for _, f := range archive.Files() {
		if !strings.HasPrefix(strings.ToLower(f.ArchivePath), lower) {
			continue
		}
		fmt.Printf("\n=== %s  (raw %d B, type %d)\n", f.ArchivePath, f.Size, f.DataType)
		text, err := archive.FileText(f.Index)
		if err != nil {
			fmt.Printf("  FileText err: %v\n", err)
		} else {
			if len(text) > *maxBytes {
				text = text[:*maxBytes] + "\n…(truncated)"
			}
			fmt.Println(text)
		}
		shown++
		if shown >= *limit {
			break
		}
	}
	fmt.Printf("\nprinted=%d\n", shown)
}
