package worldmap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 路由指纹使用候选位置/类型/来源/错误，保留既有候选顺序，避免完整定义副本影响口径。
func mapCacheRouteGoldens(r *Resolver) map[string][32]byte {
	result := make(map[string][32]byte)
	for dungeonID, index := range r.byDungeon {
		for mazeIndex, maze := range index.mazes {
			keys := map[coordinateKey]bool{}
			for key := range maze.bySpecification {
				keys[key] = true
			}
			for key := range maze.byOwnership {
				keys[key] = true
			}
			for key := range maze.byCoordinate {
				keys[key] = true
			}
			for key := range keys {
				candidates, source, err := r.resolveCandidateSet(ResolveRequest{DungeonID: dungeonID, MazeIndex: mazeIndex, X: key.x, Y: key.y})
				message := ""
				if err != nil {
					message = err.Error()
				}
				rows := make([]any, 0, len(candidates))
				for _, candidate := range candidates {
					rows = append(rows, []any{candidate.mapPos, candidate.fileType, candidate.specificationType})
				}
				encoded, err := json.Marshal([]any{source, message, rows})
				if err != nil {
					panic(err)
				}
				result[fmt.Sprintf("%d:%d:%d:%d", dungeonID, mazeIndex, key.x, key.y)] = sha256.Sum256(encoded)
			}
		}
	}
	return result
}

func TestRealPVFMapCacheWholeArchiveFieldsAndRouting(t *testing.T) {
	path := os.Getenv("DNF_WORLDMAP_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("未指定真实PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	table, err := LoadSource(context.Background(), archive, Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(table)
	if err != nil {
		t.Fatal(err)
	}
	routes := mapCacheRouteGoldens(resolver)
	snapshot := table.Snapshot()
	hashes := make([][32]byte, len(table.maps))
	for i := range table.maps {
		value, err := table.readMap(i)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		hashes[i] = sha256.Sum256(encoded)
	}
	if len(hashes) == 0 || len(routes) == 0 {
		t.Fatal("真实资源没有地图或路由样本")
	}
	if err := table.EnableMapCache(archive, DefaultMapCacheOptions()); err != nil {
		t.Fatal(err)
	}
	for i, want := range hashes {
		value, err := table.readMap(i)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(encoded) != want {
			t.Fatalf("位置%d地图%d/%s全部公开字段不一致", i, value.ID, value.Path)
		}
		if i%128 == 0 {
			archive.ReleaseReadCaches()
		}
	}
	if !reflect.DeepEqual(snapshot, table.Snapshot()) {
		t.Fatalf("缓存启用后表计数发生变化：%+v/%+v", snapshot, table.Snapshot())
	}
	gotRoutes := mapCacheRouteGoldens(resolver)
	if !reflect.DeepEqual(routes, gotRoutes) {
		for key, want := range routes {
			if got, ok := gotRoutes[key]; !ok || got != want {
				t.Fatalf("缓存启用后路由来源/错误/候选顺序变化：%s", key)
			}
		}
		t.Fatal("缓存启用后路由条数发生变化")
	}
	cached := table.MapCacheSnapshot()
	if cached.Entries > 128 || cached.EstimatedBytes > 16<<20 || cached.PinnedEstimatedBytes > 64<<20 {
		t.Fatalf("地图缓存超过估算预算：%+v", cached)
	}
	t.Logf("完整地图字段一致数=%d 路由来源/错误/候选顺序一致数=%d 缓存=%+v", len(hashes), len(routes), cached)
}
