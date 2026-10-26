package worldmap

import (
	"container/list"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

var ErrMapDefinitionRead = errors.New("地图完整定义读取失败")

// 字节预算是保守估计，不是 Go 进程内存上限；不改变地图及奖励规则。
type MapCacheOptions struct {
	Capacity      int
	MaxBytes      int64
	PinnedBytes   int64
	HeavySections int
	HeavyBytes    int64
}

func DefaultMapCacheOptions() MapCacheOptions {
	return MapCacheOptions{Capacity: 128, MaxBytes: 16 << 20, PinnedBytes: 64 << 20, HeavySections: 512, HeavyBytes: 1 << 20}
}

type MapCacheSnapshot struct {
	Enabled                                           bool
	Capacity, Entries, Pinned, UnpinnedHeavy          int
	EstimatedBytes, PinnedEstimatedBytes              int64
	Hits, PinnedHits, Loads, Waits, Evictions, Errors uint64
}

type mapCacheEntry struct {
	position int
	value    Map
	bytes    int64
}
type mapCacheFlight struct {
	done  chan struct{}
	value Map
	err   error
}
type mapDefinitionCache struct {
	mu       sync.Mutex
	source   dnfpvf.Source
	refs     []Map
	options  MapCacheOptions
	pinned   map[int]Map
	lru      *list.List
	entries  map[int]*list.Element
	flights  map[int]*mapCacheFlight
	snapshot MapCacheSnapshot
}

// 仅允许在发布给运行时之前调用。现有 Resolver 持有的索引及候选顺序不重建。
func (t *Table) EnableMapCache(source dnfpvf.Source, options MapCacheOptions) error {
	if t == nil || source == nil {
		return ErrSourceRequired
	}
	if t.mapCache != nil {
		return errors.New("地图按需缓存已经启用")
	}
	if options.Capacity < 1 || options.MaxBytes < 1 || options.PinnedBytes < 0 || options.HeavySections < 1 || options.HeavyBytes < 1 {
		return errors.New("地图按需缓存预算无效")
	}
	c := &mapDefinitionCache{source: source, options: options, pinned: make(map[int]Map), lru: list.New(), entries: make(map[int]*list.Element), flights: make(map[int]*mapCacheFlight)}
	c.snapshot = MapCacheSnapshot{Enabled: true, Capacity: options.Capacity}
	type weighted struct {
		position int
		bytes    int64
		score    int
	}
	var heavy []weighted
	for i, m := range t.maps {
		bytes := mapCacheWeight(m)
		if len(m.SourceSections) >= options.HeavySections || len(m.Summons) >= 32 || bytes >= options.HeavyBytes {
			heavy = append(heavy, weighted{i, bytes, len(m.SourceSections) + 8*len(m.Summons)})
		}
	}
	sort.SliceStable(heavy, func(i, j int) bool { return heavy[i].score > heavy[j].score })
	for _, item := range heavy {
		if item.bytes > options.PinnedBytes-c.snapshot.PinnedEstimatedBytes {
			c.snapshot.UnpinnedHeavy++
			continue
		}
		c.pinned[item.position] = t.maps[item.position]
		c.snapshot.PinnedEstimatedBytes += item.bytes
	}
	c.snapshot.Pinned = len(c.pinned)
	for i, m := range t.maps {
		t.maps[i] = mapRoutingMetadata(m)
	}
	c.refs = t.maps
	t.mapCache = c
	return nil
}

func mapRoutingMetadata(m Map) Map {
	return Map{ID: m.ID, Path: strings.Clone(m.Path), Name: strings.Clone(m.Name), DungeonID: m.DungeonID, DungeonIDs: append([]int64(nil), m.DungeonIDs...), DungeonStartArea: append([]int64(nil), m.DungeonStartArea...), Type: strings.Clone(m.Type), Greed: strings.Clone(m.Greed), Flags: m.Flags, PathgateRecognizeRange: m.PathgateRecognizeRange}
}

func (t *Table) MapCacheSnapshot() MapCacheSnapshot {
	if t == nil || t.mapCache == nil {
		return MapCacheSnapshot{}
	}
	c := t.mapCache
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.snapshot
	result.Entries = c.lru.Len()
	return result
}

func (t *Table) readMap(position int) (Map, error) {
	if t == nil || position < 0 || position >= len(t.maps) {
		return Map{}, fmt.Errorf("%w: index=%d", ErrMapDefinitionRead, position)
	}
	if t.mapCache == nil {
		return cloneMap(t.maps[position]), nil
	}
	return t.mapCache.get(position)
}

func (c *mapDefinitionCache) get(position int) (Map, error) {
	c.mu.Lock()
	if value, ok := c.pinned[position]; ok {
		c.snapshot.PinnedHits++
		c.mu.Unlock()
		return cloneMap(value), nil
	}
	if element := c.entries[position]; element != nil {
		c.snapshot.Hits++
		c.lru.MoveToFront(element)
		value := element.Value.(mapCacheEntry).value
		c.mu.Unlock()
		return cloneMap(value), nil
	}
	if flight := c.flights[position]; flight != nil {
		c.snapshot.Waits++
		c.mu.Unlock()
		<-flight.done
		if flight.err != nil {
			return Map{}, flight.err
		}
		return cloneMap(flight.value), nil
	}
	flight := &mapCacheFlight{done: make(chan struct{})}
	c.flights[position] = flight
	c.snapshot.Loads++
	c.mu.Unlock()
	value, err := readCompleteMap(c.source, c.refs[position])
	var bytes int64
	if err == nil {
		bytes = mapCacheWeight(value)
	}
	c.mu.Lock()
	if err == nil && bytes <= c.options.MaxBytes {
		c.entries[position] = c.lru.PushFront(mapCacheEntry{position, value, bytes})
		c.snapshot.EstimatedBytes += bytes
		for c.lru.Len() > c.options.Capacity || c.snapshot.EstimatedBytes > c.options.MaxBytes {
			last := c.lru.Back()
			entry := last.Value.(mapCacheEntry)
			delete(c.entries, entry.position)
			c.lru.Remove(last)
			c.snapshot.EstimatedBytes -= entry.bytes
			c.snapshot.Evictions++
		}
	} else if err != nil {
		c.snapshot.Errors++
	}
	flight.value, flight.err = value, err
	delete(c.flights, position)
	close(flight.done)
	c.mu.Unlock()
	if err != nil {
		return Map{}, err
	}
	return cloneMap(value), nil
}

func readCompleteMap(source dnfpvf.Source, ref Map) (Map, error) {
	load := func(p string) (string, *dnfpvf.Document, error) {
		text, err := source.ReadText(p)
		if err != nil {
			return "", nil, err
		}
		doc, err := ParseDocument(p, text)
		return p, doc, err
	}
	_, doc, err := load(ref.Path)
	if err == nil {
		var value Map
		value, err = parseMapWithImports(ref.ID, ref.Path, doc, load)
		if err == nil {
			compactMapSourceTokens(&value)
			return value, nil
		}
	}
	return Map{}, fmt.Errorf("%w: %s: %w", ErrMapDefinitionRead, ref.Path, err)
}

// 统计容器容量及字符串长度；共享字符串重复计入，倾向于提前淘汰。
// 已压缩 Token 没有动态字段，不逐个反射，避免读取路径增加不必要的 CPU 工作。
func mapCacheWeight(m Map) int64 {
	return int64(reflect.TypeOf(m).Size()) + mapCacheDynamicBytes(reflect.ValueOf(m))
}

var compactTokenType = reflect.TypeOf(compactMapToken{})

func mapCacheDynamicBytes(v reflect.Value) int64 {
	switch v.Kind() {
	case reflect.String:
		return int64(v.Len())
	case reflect.Ptr:
		if !v.IsNil() {
			return int64(v.Type().Elem().Size()) + mapCacheDynamicBytes(v.Elem())
		}
	case reflect.Struct:
		if v.Type() == compactTokenType {
			return 0
		}
		var n int64
		for i := 0; i < v.NumField(); i++ {
			n += mapCacheDynamicBytes(v.Field(i))
		}
		return n
	case reflect.Slice, reflect.Array:
		n := int64(v.Len()) * int64(v.Type().Elem().Size())
		if v.Kind() == reflect.Slice {
			n = int64(v.Cap()) * int64(v.Type().Elem().Size())
		}
		k := v.Type().Elem().Kind()
		if v.Type().Elem() == compactTokenType || (k != reflect.String && k != reflect.Struct && k != reflect.Slice && k != reflect.Map && k != reflect.Ptr) {
			return n
		}
		for i := 0; i < v.Len(); i++ {
			n += mapCacheDynamicBytes(v.Index(i))
		}
		return n
	case reflect.Map:
		n := int64(v.Len()) * int64(v.Type().Key().Size()+v.Type().Elem().Size()+32)
		it := v.MapRange()
		for it.Next() {
			n += mapCacheDynamicBytes(it.Key()) + mapCacheDynamicBytes(it.Value())
		}
		return n
	}
	return 0
}
