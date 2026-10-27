package worldmap

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// LoadSourceCached 校验每份地图后仅保留路由及教学动作索引。
// 重型地图按现有评分和预算选择后读回；普通完整定义仍由同一PVF按需读取。
// source在加载及运行期间必须保持不变。
func LoadSourceCached(ctx context.Context, source dnfpvf.Source, options Options, cacheOptions MapCacheOptions) (*Table, error) {
	return loadSourceWithCache(ctx, source, options, &cacheOptions)
}

type streamingMapWeight struct {
	position int
	bytes    int64
	score    int
}

type streamingMapBuilder struct {
	options MapCacheOptions
	source  dnfpvf.Source
	heavy   []streamingMapWeight
}

func newStreamingMapBuilder(source dnfpvf.Source, options MapCacheOptions) (*streamingMapBuilder, error) {
	if options.Capacity < 1 || options.MaxBytes < 1 || options.PinnedBytes < 0 || options.HeavySections < 1 || options.HeavyBytes < 1 {
		return nil, errors.New("地图按需缓存预算无效")
	}
	return &streamingMapBuilder{source: source, options: options}, nil
}

func (b *streamingMapBuilder) observe(t *Table, position int) {
	m := t.maps[position]
	weight := mapCacheWeight(m)
	if len(m.SourceSections) >= b.options.HeavySections || len(m.Summons) >= 32 || weight >= b.options.HeavyBytes {
		b.heavy = append(b.heavy, streamingMapWeight{position, weight, len(m.SourceSections) + 8*len(m.Summons)})
	}
	t.maps[position] = mapRoutingMetadata(m)
}

func (b *streamingMapBuilder) finish(ctx context.Context, t *Table) error {
	c := &mapDefinitionCache{source: b.source, options: b.options, refs: t.maps, pinned: make(map[int]Map),
		lru: list.New(), entries: make(map[int]*list.Element), flights: make(map[int]*mapCacheFlight)}
	c.snapshot = MapCacheSnapshot{Enabled: true, Capacity: b.options.Capacity}
	sort.SliceStable(b.heavy, func(i, j int) bool { return b.heavy[i].score > b.heavy[j].score })
	for _, item := range b.heavy {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.bytes > b.options.PinnedBytes-c.snapshot.PinnedEstimatedBytes {
			c.snapshot.UnpinnedHeavy++
			continue
		}
		value, err := readCompleteMap(b.source, t.maps[item.position])
		if err != nil {
			return fmt.Errorf("启动保留复杂地图失败: %w", err)
		}
		if actual := mapCacheWeight(value); actual != item.bytes {
			return fmt.Errorf("复杂地图读回估算发生变化: %s，首次%d，读回%d", value.Path, item.bytes, actual)
		}
		c.pinned[item.position] = value
		c.snapshot.PinnedEstimatedBytes += item.bytes
	}
	c.snapshot.Pinned = len(c.pinned)
	t.mapCache = c
	return nil
}

// MapActionReferences只返回教学索引所需的字段，不表示完整地图定义。
// 原表位置顺序保持；归属及怪物切片是独立副本。
func (t *Table) MapActionReferences() []Map {
	if t == nil {
		return nil
	}
	var out []Map
	for _, m := range t.maps {
		if strings.TrimSpace(m.BasicAction) == "" {
			continue
		}
		out = append(out, cloneMap(Map{ID: m.ID, Path: m.Path, BasicAction: m.BasicAction,
			DungeonID: m.DungeonID, DungeonIDs: m.DungeonIDs, Monsters: m.Monsters}))
	}
	return out
}
