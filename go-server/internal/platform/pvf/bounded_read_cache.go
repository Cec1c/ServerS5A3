package pvf

import (
	"container/list"
	"errors"
	"sync"
)

// BoundedReadCacheSnapshot 的字节数是缓存载荷及每项固定记账开销，
// 不表示进程总内存；单次解码及已返回给调用方的值不属于缓存保留量。
type BoundedReadCacheSnapshot struct {
	TextBudget  int64 `json:"text_budget_bytes"`
	ChunkBudget int64 `json:"chunk_budget_bytes"`
	TextBytes   int64 `json:"text_estimated_bytes"`
	ChunkBytes  int64 `json:"chunk_estimated_bytes"`
	Texts       int   `json:"texts"`
	Chunks      int   `json:"chunks"`
}

type boundedReadCaches struct {
	texts  byteReadCache
	chunks byteReadCache
}

type byteReadEntry struct {
	key    int
	value  any
	weight int64
}

type byteReadCache struct {
	mu      sync.Mutex
	budget  int64
	bytes   int64
	order   list.List
	entries map[int]*list.Element
}

// EnableBoundedReadCaches 在归档发布给读者之前调用一次。
// 只改变可重建的读取缓存；解码、字符串池和文件索引保持原语义。
func (a *Archive) EnableBoundedReadCaches(textBytes, chunkBytes int64) error {
	if a == nil || textBytes < 1 || chunkBytes < 1 {
		return errors.New("PVF读取缓存预算必须为正数")
	}
	if a.readBudget.Load() != nil {
		return errors.New("PVF读取缓存预算已经设置")
	}
	cache := &boundedReadCaches{texts: byteReadCache{budget: textBytes, entries: make(map[int]*list.Element)}, chunks: byteReadCache{budget: chunkBytes, entries: make(map[int]*list.Element)}}
	a.ReleaseReadCaches()
	if !a.readBudget.CompareAndSwap(nil, cache) {
		return errors.New("PVF读取缓存预算已经设置")
	}
	return nil
}

func (c *byteReadCache) get(key int) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(entry)
	return entry.Value.(byteReadEntry).value, true
}

func (c *byteReadCache) put(key int, value any, payload int) any {
	weight := int64(payload) + 64
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		c.order.MoveToFront(entry)
		return entry.Value.(byteReadEntry).value
	}
	if weight > c.budget {
		return value
	}
	for c.bytes > c.budget-weight {
		entry := c.order.Back()
		old := entry.Value.(byteReadEntry)
		delete(c.entries, old.key)
		c.bytes -= old.weight
		c.order.Remove(entry)
	}
	c.entries[key] = c.order.PushFront(byteReadEntry{key: key, value: value, weight: weight})
	c.bytes += weight
	return value
}

func (c *byteReadCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[int]*list.Element)
	c.order.Init()
	c.bytes = 0
}

func (c *byteReadCache) stats() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.bytes
}

func (a *Archive) BoundedReadCacheSnapshot() *BoundedReadCacheSnapshot {
	if a == nil {
		return nil
	}
	c := a.readBudget.Load()
	if c == nil {
		return nil
	}
	texts, textBytes := c.texts.stats()
	chunks, chunkBytes := c.chunks.stats()
	return &BoundedReadCacheSnapshot{TextBudget: c.texts.budget, ChunkBudget: c.chunks.budget, TextBytes: textBytes, ChunkBytes: chunkBytes, Texts: texts, Chunks: chunks}
}
