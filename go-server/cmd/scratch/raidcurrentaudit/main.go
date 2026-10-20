// Command raidcurrentaudit reads raid counters and admission settings from the actual PVF.
package main

import (
	"encoding/json"
	"flag"
	"os"
	"strings"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	path := flag.String("pvf", "", "actual PVF archive (read only)")
	flag.Parse()
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: *path})
	if err != nil {
		panic(err)
	}
	configs := map[string]string{}
	for _, file := range archive.Files() {
		name := strings.ToLower(file.ArchivePath)
		if !strings.HasPrefix(name, "etc/") || file.Size > 500000 {
			continue
		}
		if !strings.Contains(name, "raid") && !strings.Contains(name, "anton") && !strings.Contains(name, "luke") {
			continue
		}
		text, err := archive.FileText(file.Index)
		if err != nil {
			panic(err)
		}
		if name == "etc/raid/anton.etc" || name == "etc/raid/luke/luke.etc" || strings.Contains(strings.ToLower(text), "limit") || strings.Contains(strings.ToLower(text), "count") {
			configs[file.ArchivePath] = text
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"read_only": true, "configs": configs}); err != nil {
		panic(err)
	}
}
