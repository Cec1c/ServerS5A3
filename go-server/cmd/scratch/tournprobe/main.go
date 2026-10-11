// tournprobe 是只读取证工具：打印真档 DGN/MAP 的 section 与 token 归属，
// 用来确认 `[result card]` 这类 block 段的 token 落在哪一段。
//
//	go run ./cmd/scratch/tournprobe -pvf <Script.pvf> -path dungeon/shonantournament/yellowdragon.dgn
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	pvfPath := flag.String("pvf", "", "Script.pvf path")
	path := flag.String("path", "", "archive path to inspect")
	flag.Parse()
	if *pvfPath == "" || *path == "" {
		fmt.Fprintln(os.Stderr, "-pvf and -path are required")
		os.Exit(2)
	}
	archive, err := platformpvf.Open(*pvfPath)
	if err != nil {
		panic(err)
	}
	text, err := archive.ReadText(*path)
	if err != nil {
		panic(err)
	}
	_ = context.Background()
	doc, err := dnfpvf.Parse(*path, text)
	if err != nil {
		panic(err)
	}
	for _, section := range doc.Sections {
		tokens := doc.Tokens[section.Start:section.End]
		var parts []string
		for _, token := range tokens {
			parts = append(parts, token.Value)
		}
		fmt.Printf("%-28s start=%5d end=%5d n=%2d | %s\n",
			section.Name, section.Start, section.End, len(tokens),
			strings.Join(parts, " "))
	}
}
