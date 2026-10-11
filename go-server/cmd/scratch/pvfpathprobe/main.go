// Command pvfpathprobe prints, for the first entries matching a path prefix, the
// File fields and which of them resolves through the archive path index.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	pvfPath := flag.String("pvf", "", "Script.pvf to inspect (read only)")
	prefix := flag.String("prefix", "ui/pvp02", "path prefix")
	limit := flag.Int("limit", 8, "max entries to print")
	flag.Parse()
	if *pvfPath == "" {
		fmt.Fprintln(os.Stderr, "usage: pvfpathprobe -pvf <Script.pvf> [-prefix ui/pvp02]")
		os.Exit(2)
	}
	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	shown := 0
	for _, f := range archive.Files() {
		if !strings.HasPrefix(strings.ToLower(f.Path), strings.ToLower(*prefix)) {
			continue
		}
		fmt.Printf("index=%d\n  Path=%q\n  ArchivePath=%q\n  Name=%q\n  DataType=%d Size=%d\n  FindFileIndex(Path)=%d  FindFileIndex(ArchivePath)=%d\n",
			f.Index, f.Path, f.ArchivePath, f.Name, f.DataType, f.Size,
			archive.FindFileIndex(f.Path), archive.FindFileIndex(f.ArchivePath))
		if raw, err := archive.FileRaw(f.Index); err == nil {
			fmt.Printf("  FileRaw(idx) ok bytes=%d\n", len(raw))
		} else {
			fmt.Printf("  FileRaw(idx) err=%v\n", err)
		}
		if raw, err := archive.ReadRaw(f.ArchivePath); err == nil {
			fmt.Printf("  ReadRaw(ArchivePath) ok bytes=%d\n", len(raw))
		} else {
			fmt.Printf("  ReadRaw(ArchivePath) err=%v\n", err)
		}
		shown++
		if shown >= *limit {
			break
		}
	}
	fmt.Printf("printed=%d\n", shown)
}
