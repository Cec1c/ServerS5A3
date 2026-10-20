// pvfdump 是一次性 PVF 取证程序（不进入生产路径）：
// 按条目路径把运行时 Script.pvf 里的脚本原文打印出来，用于核对盒子/礼包的真实产出表。
//
// 用法：
//
//	go run ./cmd/scratch/pvfdump -pvf <Script.pvf> -path <条目路径> [-path ...]
//	go run ./cmd/scratch/pvfdump -pvf <Script.pvf> -grep <子串>   # 列出归档路径命中项
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type pathList []string

func (p *pathList) String() string { return strings.Join(*p, ",") }

func (p *pathList) Set(value string) error {
	*p = append(*p, value)
	return nil
}

func lower(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

// candidates mirrors the server's readDungeonDropText fallbacks: the listed
// path, the same path under a kind root (stackable/equipment/...), and the
// 90 PVF "(r)"-prefixed revised basename.
func candidates(listed string) []string {
	clean := strings.Trim(strings.ReplaceAll(strings.TrimSpace(listed), "\\", "/"), "/")
	out := []string{clean}
	for _, root := range []string{"stackable", "equipment", "etc", "character"} {
		if !strings.HasPrefix(lower(clean), root+"/") {
			out = append(out, path.Join(root, clean))
		}
	}
	for _, candidate := range append([]string(nil), out...) {
		dir, base := path.Split(candidate)
		if base == "" || strings.HasPrefix(lower(base), "(r)") {
			continue
		}
		out = append(out, dir+"(r)"+base)
	}
	return out
}

func main() {
	var paths pathList
	pvfPath := flag.String("pvf", `/opt/dnf90/data/dnf/Script.pvf`, "pvf path")
	grep := flag.String("grep", "", "list entry archive paths containing this substring")
	scan := flag.String("scan", "", "scan every .stk containing this marker and print its child-section ints")
	out := flag.String("out", "", "write matched entry text to this file")
	flag.Var(&paths, "path", "entry path to dump (repeatable)")
	flag.Parse()

	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: *pvfPath})
	if err != nil {
		panic(err)
	}
	fmt.Printf("pvf=%s files=%d format=%s\n", *pvfPath, archive.FileCount(), archive.Format())

	if *scan != "" {
		for _, file := range archive.Files() {
			if !strings.HasSuffix(lower(file.ArchivePath), ".stk") {
				continue
			}
			text, err := archive.ReadText(file.ArchivePath)
			if err != nil || !strings.Contains(text, *scan) {
				continue
			}
			document, err := dnfpvf.Parse(file.ArchivePath, text)
			if err != nil {
				fmt.Printf("SCAN %s parse_error=%v\n", file.ArchivePath, err)
				continue
			}
			fmt.Printf("SCAN %s stackable=%q\n", file.ArchivePath, sectionText(document, "stackable type"))
			printChildSections(document, *scan)
		}
	}

	if *grep != "" {
		needle := lower(*grep)
		count := 0
		for _, file := range archive.Files() {
			if strings.Contains(lower(file.ArchivePath), needle) {
				fmt.Printf("PATH %s\n", file.ArchivePath)
				count++
				if count > 3000 {
					fmt.Println("... truncated")
					break
				}
			}
		}
		fmt.Printf("matched=%d\n", count)
	}

	var sink *bufio.Writer
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			panic(err)
		}
		defer file.Close()
		sink = bufio.NewWriter(file)
		defer sink.Flush()
	}

	for _, entry := range paths {
		text, resolved, err := read(archive, entry)
		if err != nil {
			fmt.Printf("=== %s === ERROR %v\n", entry, err)
			continue
		}
		fmt.Printf("=== %s (resolved %s) === %d bytes\n%s\n", entry, resolved, len(text), text)
		if sink != nil {
			fmt.Fprintf(sink, "=== %s (resolved %s) === %d bytes\n%s\n", entry, resolved, len(text), text)
		}
	}
}

func read(archive *platformpvf.Archive, entry string) (string, string, error) {
	var lastErr error
	for _, candidate := range candidates(entry) {
		text, err := archive.ReadText(candidate)
		if err == nil {
			return text, candidate, nil
		}
		lastErr = err
	}
	return "", "", lastErr
}

func sectionText(document *dnfpvf.Document, name string) string {
	values := document.Texts(name)
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, "|")
}

// printChildSections mirrors the server's [booster info] walk and prints every
// direct child section with its integer vector, so the on-disk shapes can be
// compared with what the parser currently assumes (draw-count + triples).
func printChildSections(document *dnfpvf.Document, parent string) {
	parent = strings.ToLower(strings.Trim(strings.TrimSpace(parent), "[]"))
	inParent := false
	for _, section := range document.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		switch name {
		case parent:
			inParent = true
			continue
		case "/" + parent:
			inParent = false
		}
		if !inParent || strings.HasPrefix(name, "/") {
			continue
		}
		ints := make([]int64, 0, 16)
		if section.Start >= 0 && section.End <= len(document.Tokens) && section.Start <= section.End {
			for _, token := range document.Tokens[section.Start:section.End] {
				if token.Kind == dnfpvf.TokenInt {
					ints = append(ints, token.Int)
				}
			}
		}
		fmt.Printf("  SECTION %s ints=%v\n", section.Name, ints)
	}
}
