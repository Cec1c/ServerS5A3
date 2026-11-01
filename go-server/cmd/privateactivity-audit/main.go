// Read-only inventory of the current PVF's activity-relevant dungeon definitions.
package main

import (
	"context"
	"encoding/json"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"strings"
)

func main() {
	a, e := platformpvf.Open(os.Args[1])
	if e != nil {
		panic(e)
	}
	t, e := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if e != nil {
		panic(e)
	}
	rows := []any{}
	for _, d := range t.Dungeons() {
		p := strings.ToLower(d.Path)
		if d.ID == 316 || d.ID == 3570 || d.ID == 3571 || d.ID == 5097 || d.ID == 5098 || d.ID == 7539 || strings.Contains(p, "epciroad") || strings.Contains(p, "epicroad") || strings.Contains(p, "ekon") || strings.Contains(p, "aiken") || d.Metadata.Flags["dimension dungeon"] {
			rows = append(rows, map[string]any{"id": d.ID, "path": d.Path, "metadata": d.Metadata})
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if e = enc.Encode(rows); e != nil {
		panic(e)
	}
}
