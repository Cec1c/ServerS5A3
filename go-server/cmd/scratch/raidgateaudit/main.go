// Command raidgateaudit reads real raid gate and entry quest definitions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/quest"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func main() {
	path := flag.String("pvf", "", "actual PVF archive, read only")
	flag.Parse()
	a, err := platformpvf.LoadArchive(platformpvf.Options{Path: *path})
	if err != nil {
		panic(err)
	}
	index, err := dnfpvf.Build(context.Background(), a, dnfpvf.BuildOptions{Lists: []string{quest.DefaultList}})
	if err != nil {
		panic(err)
	}
	catalog, err := quest.Load(context.Background(), index)
	if err != nil {
		panic(err)
	}
	rows := []quest.Definition{}
	for id := int64(1); id <= 65535; id++ {
		d, ok := catalog.Find(id)
		if !ok {
			continue
		}
		if strings.Contains(d.Name, "异次元的安徒恩") || strings.Contains(d.Name, "卢克") && (strings.Contains(d.Name, "异次元") || strings.Contains(d.Name, "进入")) || strings.Contains(d.Name, "远古") && (strings.Contains(d.Name, "进入") || strings.Contains(d.Name, "入场") || strings.Contains(d.Name, "开启")) {
			rows = append(rows, d)
		}
	}
	configs := map[string]string{}
	for _, f := range a.Files() {
		if !strings.HasPrefix(strings.ToLower(f.ArchivePath), "etc/") || f.Size > 500000 {
			continue
		}
		t, err := a.FileText(f.Index)
		if err != nil {
			continue
		}
		low := strings.ToLower(t)
		if strings.Contains(low, "raid") && strings.Contains(low, "quest") || strings.Contains(low, "anton") && strings.Contains(low, "quest") {
			configs[f.ArchivePath] = t
		}
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(map[string]any{"read_only": true, "quests": rows, "configs": configs}); err != nil {
		panic(err)
	}
}
