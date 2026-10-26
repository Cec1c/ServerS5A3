package pvf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
)

// Build only the parsed index and two encrypted chunks in memory. This tests
// actual Archive reads/decoding/caches, not the archive header parser or real PVF.
func r195CacheMultiChunkFixture(t *testing.T) (*Archive, []string, []string, [][]byte) {
	t.Helper()
	a := &Archive{format: FormatNKPI, pathIdx: make(map[string]int)}
	paths := []string{"cache/a.txt", "cache/b.txt", "cache/c.txt", "cache/d.txt"}
	texts := make([]string, len(paths))
	raws := make([][]byte, len(paths))
	for group := 0; group < 2; group++ {
		var chunk []byte
		for n := 0; n < 2; n++ {
			i := group*2 + n
			texts[i] = strings.Repeat(fmt.Sprintf("key-%d-地图;", i), 16)
			units := utf16.Encode([]rune(texts[i]))
			raws[i] = make([]byte, len(units)*2)
			for j, unit := range units {
				binary.LittleEndian.PutUint16(raws[i][j*2:], unit)
			}
			a.items = append(a.items, fileItem{chunkIndex: group, dataOffset: len(chunk), dataSize: len(raws[i]), dataType: 3})
			a.files = append(a.files, File{Index: i, Path: "cache", Name: paths[i][6:], ArchivePath: paths[i], DataType: 3, Size: len(raws[i])})
			a.pathIdx[pathKey(paths[i])] = i
			chunk = append(chunk, raws[i]...)
		}
		encrypted, err := a.encryptChunk(chunk)
		if err != nil {
			t.Fatal(err)
		}
		a.data = append(a.data, encrypted...)
		a.groups = append(a.groups, groupItem{compressedSize: len(a.data), originalSize: len(chunk)})
	}
	return a, paths, texts, raws
}

// Inspect bookkeeping under its own mutex; do not implement replacement policy.
// The sum must match actual retained payloads, including each entry's overhead.
func r195CacheCheckRetainedBytes(c *byteReadCache) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) != c.order.Len() {
		return fmt.Errorf("index/list sizes differ: %d/%d", len(c.entries), c.order.Len())
	}
	var sum int64
	for node := c.order.Front(); node != nil; node = node.Next() {
		entry := node.Value.(byteReadEntry)
		if c.entries[entry.key] != node {
			return fmt.Errorf("entry %d has inconsistent index", entry.key)
		}
		var payload int
		switch value := entry.value.(type) {
		case string:
			payload = len(value)
		case []byte:
			payload = cap(value)
		default:
			return fmt.Errorf("unexpected retained payload %T", entry.value)
		}
		if entry.weight != int64(payload)+64 {
			return fmt.Errorf("entry %d weight=%d payload=%d", entry.key, entry.weight, payload)
		}
		sum += entry.weight
	}
	if c.bytes != sum || sum < 0 || sum > c.budget {
		return fmt.Errorf("retained bytes=%d sum=%d budget=%d", c.bytes, sum, c.budget)
	}
	return nil
}

func TestR195CacheMultiTextMultiChunkConcurrentReadAndClearConservesBudget(t *testing.T) {
	a, paths, wantText, wantRaw := r195CacheMultiChunkFixture(t)
	// Compare with both independently supplied plaintext and the unbounded path.
	for i, p := range paths {
		text, err := a.ReadText(p)
		if err != nil || text != wantText[i] {
			t.Fatalf("unbounded text %s: %q err=%v", p, text, err)
		}
		raw, err := a.ReadRaw(p)
		if err != nil || !bytes.Equal(raw, wantRaw[i]) {
			t.Fatalf("unbounded raw %s: err=%v", p, err)
		}
	}
	// Measure decoded allocation capacity, not compressed length. Both equal-size
	// fixture chunks must fit individually; their sum must require eviction.
	var chunkBudget, chunkTotal int64
	for i := range a.groups {
		decoded, err := a.decodeChunkIndex(i)
		if err != nil {
			t.Fatal(err)
		}
		weight := int64(cap(decoded)) + 64
		chunkTotal += weight
		if weight > chunkBudget {
			chunkBudget = weight
		}
	}
	textBudget := int64(len(wantText[0])) + 64
	if chunkTotal <= chunkBudget {
		t.Fatal("fixture does not exercise chunk eviction")
	}
	if err := a.EnableBoundedReadCaches(textBudget, chunkBudget); err != nil {
		t.Fatal(err)
	}
	cache := a.readBudget.Load()
	check := func() error {
		if err := r195CacheCheckRetainedBytes(&cache.texts); err != nil {
			return fmt.Errorf("texts: %w", err)
		}
		if err := r195CacheCheckRetainedBytes(&cache.chunks); err != nil {
			return fmt.Errorf("chunks: %w", err)
		}
		return nil
	}
	read := func(i int) error {
		text, err := a.ReadText(paths[i])
		if err != nil || text != wantText[i] {
			return fmt.Errorf("text %s differs: %v", paths[i], err)
		}
		raw, err := a.ReadRaw(paths[i])
		if err != nil || !bytes.Equal(raw, wantRaw[i]) {
			return fmt.Errorf("raw %s differs: %v", paths[i], err)
		}
		// ReadRaw returns a caller-owned copy, even across clears/evictions.
		raw[0] ^= 0xff
		again, err := a.ReadRaw(paths[i])
		if err != nil || !bytes.Equal(again, wantRaw[i]) {
			return fmt.Errorf("raw copy alias for %s: %v", paths[i], err)
		}
		return check()
	}
	// Establish that both caches really retain and evict, rather than passing
	// because all entries are too large to store under the selected budgets.
	for _, i := range []int{0, 2, 1, 3} {
		if err := read(i); err != nil {
			t.Fatal(err)
		}
		snap := a.BoundedReadCacheSnapshot()
		if snap.Texts != 1 || snap.Chunks != 1 || snap.TextBytes != textBudget || snap.ChunkBytes != chunkBudget {
			t.Fatalf("single retained entry required after eviction: %+v", snap)
		}
		if _, ok := cache.texts.get(i); !ok {
			t.Fatalf("current text %d missing", i)
		}
		if _, ok := cache.chunks.get(i / 2); !ok {
			t.Fatalf("current chunk %d missing", i/2)
		}
	}
	heldText, err := a.ReadText(paths[3])
	if err != nil {
		t.Fatal(err)
	}
	heldRaw, err := a.ReadRaw(paths[3])
	if err != nil {
		t.Fatal(err)
	}
	// Each round releases four distinct-key readers and a clearer together.
	// Completion channels, not sleeps, separate rounds; exact scheduling within
	// a round is intentionally unspecified.
	for round := 0; round < 16; round++ {
		start := make(chan struct{})
		done := make(chan error, len(paths)+1)
		for i := range paths {
			go func(i int) {
				<-start
				done <- read(i)
			}(i)
		}
		go func() {
			<-start
			a.ReleaseReadCaches()
			done <- check()
		}()
		close(start)
		for i := 0; i < len(paths)+1; i++ {
			if err := <-done; err != nil {
				t.Errorf("round %d: %v", round, err)
			}
		}
	}
	a.ReleaseReadCaches()
	if snap := a.BoundedReadCacheSnapshot(); snap.Texts != 0 || snap.Chunks != 0 || snap.TextBytes != 0 || snap.ChunkBytes != 0 {
		t.Fatalf("quiescent clear left retained entries: %+v", snap)
	}
	if heldText != wantText[3] || !bytes.Equal(heldRaw, wantRaw[3]) {
		t.Fatal("previously returned values changed across eviction/clear")
	}
	for i := range paths {
		if err := read(i); err != nil {
			t.Fatalf("refill after final clear: %v", err)
		}
	}
}
