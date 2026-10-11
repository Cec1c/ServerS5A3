package gmtool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

type itemCacheTestSource struct {
	readonlyTestSource
	checksum string
	reads    int
}

func newItemCacheTestSource(documents readonlyTestSource) *itemCacheTestSource {
	keys := make([]string, 0, len(documents))
	for key := range documents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(hash, "%s\x00%s\x00", key, documents[key])
	}
	return &itemCacheTestSource{readonlyTestSource: documents, checksum: hex.EncodeToString(hash.Sum(nil))}
}

func (s *itemCacheTestSource) Snapshot() platformpvf.ArchiveSnapshot {
	return platformpvf.ArchiveSnapshot{Checksum: s.checksum}
}

func (s *itemCacheTestSource) ReadText(path string) (string, error) {
	s.reads++
	return s.readonlyTestSource.ReadText(path)
}

func itemCacheTestIndex(path string, source dnfpvf.Source) *itemIndex {
	server := &Server{
		cfg:  Config{Logger: func(string, ...any) {}},
		deps: Deps{PVF: func() (dnfpvf.Source, error) { return source, nil }},
	}
	server.items = newItemIndex(path, server, server.cfg.Logger)
	return server.items
}

func itemCacheTestDocuments(name string, withContract bool) readonlyTestSource {
	documents := readonlyTestSource{
		itemListStackable:   "1 `old.stk`\n",
		itemListEquipment:   "",
		"stackable/old.stk": "[name]\n`" + name + "`\n[stackable type]\n`[material]` 0\n",
	}
	if withContract {
		documents[itemListStackable] += "490709930 `blackdiamond.stk`\n"
		documents["stackable/blackdiamond.stk"] = "[name]\n`黑钻贵族契约(30天)`\n[stackable type]\n`[waste]` 0\n"
	}
	return documents
}

func TestItemIndexCacheTracksPVFReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.json")
	first := itemCacheTestIndex(path, newItemCacheTestSource(itemCacheTestDocuments("旧名称", false)))
	first.start(context.Background())
	if !first.isReady() {
		t.Fatal("initial build failed", first.failure())
	}
	if _, ok := first.lookup(490709930); ok {
		t.Fatal("fixture should not contain contract")
	}

	changedSource := newItemCacheTestSource(itemCacheTestDocuments("新名称", true))
	changed := itemCacheTestIndex(path, changedSource)
	changed.start(context.Background())
	if got := changed.name(1); got != "新名称" {
		t.Fatalf("retained old name after PVF replacement: %q", got)
	}
	entry, ok := changed.lookup(490709930)
	if !ok || entry.Name != "黑钻贵族契约(30天)" {
		t.Fatalf("newly registered contract missing: %+v", entry)
	}
	var saved itemIndexFile
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.PVFChecksum != itemIndexSourceChecksum(changedSource) {
		t.Fatal("cache not bound to new archive")
	}

	sameSource := newItemCacheTestSource(itemCacheTestDocuments("新名称", true))
	reused := itemCacheTestIndex(path, sameSource)
	reused.start(context.Background())
	if !reused.isReady() || reused.name(490709930) != entry.Name {
		t.Fatal("matching cache not loaded")
	}
	if sameSource.reads != 0 {
		t.Fatalf("matching archive unnecessarily rebuilt: %d document reads", sameSource.reads)
	}
}

func TestItemIndexCacheRejectsLegacyAndUnverifiedArchives(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		version        int
		cacheChecksum  string
		sourceChecksum string
	}{
		{"legacy-v3", 3, "", strings.Repeat("a", 64)},
		{"missing-cache-checksum", itemIndexVersion, "", strings.Repeat("a", 64)},
		{"missing-source-checksum", itemIndexVersion, strings.Repeat("a", 64), ""},
		{"invalid-source-checksum", itemIndexVersion, "invalid", "invalid"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "items.json")
			stale := itemIndexFile{Version: testCase.version, PVFChecksum: testCase.cacheChecksum, Entries: []itemEntry{{ID: 1, Name: "缓存旧道具"}}}
			raw, err := json.Marshal(stale)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			source := newItemCacheTestSource(itemCacheTestDocuments("当前道具", true))
			source.checksum = testCase.sourceChecksum
			index := itemCacheTestIndex(path, source)
			index.start(context.Background())
			if index.name(1) != "当前道具" || index.name(490709930) == "" || source.reads == 0 {
				t.Fatal("stale cache reused")
			}
		})
	}
}

func TestItemIndexCacheDoesNotHideUnavailablePVF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.json")
	source := newItemCacheTestSource(itemCacheTestDocuments("旧道具", false))
	itemCacheTestIndex(path, source).start(context.Background())
	index := itemCacheTestIndex(path, source)
	index.srv.deps.PVF = func() (dnfpvf.Source, error) { return nil, fmt.Errorf("PVF unavailable") }
	index.start(context.Background())
	if index.isReady() || index.failure() == nil {
		t.Fatal("stale cache concealed missing source")
	}
}

func TestItemIndexBlackDiamondRealPVF(t *testing.T) {
	path := os.Getenv("GM_ITEM_INDEX_REAL_PVF")
	if path == "" {
		t.Skip("set GM_ITEM_INDEX_REAL_PVF to audit the installed archive")
	}
	source, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "items.json")
	index := itemCacheTestIndex(cache, source)
	index.start(context.Background())
	if !index.isReady() {
		t.Fatal(index.failure())
	}
	entry, ok := index.lookup(490709930)
	if !ok || entry.Name != "黑钻贵族契约 (30天)" || entry.Invalid {
		t.Fatalf("registered contract unavailable: %+v", entry)
	}
	recorder := httptest.NewRecorder()
	index.srv.handleItemBrowse(recorder, httptest.NewRequest("GET", "/api/items/browse?q=490709930", nil))
	var result struct {
		Success bool `json:"success"`
		Total   int  `json:"total"`
		Results []struct {
			ID   uint32 `json:"itemId"`
			Name string `json:"name"`
		} `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Total != 1 || len(result.Results) != 1 || result.Results[0].ID != entry.ID {
		t.Fatalf("GM browse failed: %s", recorder.Body.String())
	}
	reloaded := itemCacheTestIndex(cache, source)
	if err := reloaded.loadCache(); err != nil {
		t.Fatal(err)
	}
	if reloaded.name(entry.ID) != entry.Name {
		t.Fatal("restart lost contract")
	}
	t.Logf("archive=%s indexed=%d contract=%d name=%s", source.Snapshot().Checksum, len(index.snapshot()), entry.ID, entry.Name)
}
