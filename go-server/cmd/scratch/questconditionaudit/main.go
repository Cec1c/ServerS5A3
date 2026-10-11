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
	path := flag.String("pvf", "", "exact live PVF path")
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
		if strings.Contains(d.Type, "condition under clear") || strings.Contains(strings.ToLower(d.Path), "/hell/") || strings.Contains(d.Name, "深渊") {
			rows = append(rows, d)
		}
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(rows); err != nil {
		panic(err)
	}
}
