package quest

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 2026-10-02 (V21) 差异分析：把**线上真实 op21 包体**里的任务 ID 列表（由
// `selftest/v20-decode-live-quest-packets.py --dump-ids` 落盘）与本地 PVF 目录 join，
// 看"服务端确实下发、客户端却不显示"的那几条（10019/2901）与其它每日任务差在哪里。
//
// 用法（PVF 与 dump 文件都在 _dist/selftest 下）：
//
//	DNFBRIDGE_REAL_PVF_SMOKE=<Script.pvf> \
//	DNFBRIDGE_LIVE_OP21_IDS=<v20-op21-ids-*.txt> \
//	go test ./internal/modules/dnf/quest -run TestLiveOp21DailyMissionJoin -v
func TestLiveOp21DailyMissionJoin(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	idsPath := os.Getenv("DNFBRIDGE_LIVE_OP21_IDS")
	if pvfPath == "" || idsPath == "" {
		t.Skip("real PVF + live op21 id dump required")
	}
	raw, err := os.ReadFile(idsPath)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 256)
	for _, field := range strings.Fields(string(raw)) {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			t.Fatalf("bad id %q in %s", field, idsPath)
		}
		ids = append(ids, id)
	}

	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	index, err := dnfpvf.Build(context.Background(), archive, dnfpvf.BuildOptions{Lists: []string{DefaultList}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(context.Background(), index)
	if err != nil {
		t.Fatal(err)
	}

	grades := map[string]int{}
	daily := make([]Definition, 0, 32)
	for _, id := range ids {
		definition, found := catalog.Find(id)
		if !found {
			grades["<not-in-catalog>"]++
			continue
		}
		grade := normalizeQuestTag(definition.Grade)
		grades[grade]++
		if strings.HasPrefix(grade, "daily") || strings.HasPrefix(grade, "special daily") {
			daily = append(daily, definition)
		}
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].ID < daily[j].ID })

	t.Logf("live op21 list = %d ids from %s", len(ids), idsPath)
	keys := make([]string, 0, len(grades))
	for grade := range grades {
		keys = append(keys, grade)
	}
	sort.Slice(keys, func(i, j int) bool {
		if grades[keys[i]] != grades[keys[j]] {
			return grades[keys[i]] > grades[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, grade := range keys {
		t.Logf("  grade=%-22s count=%d", grade, grades[grade])
	}
	t.Logf("--- 每日类任务（在列表里的）共 %d 条 ---", len(daily))
	for _, definition := range daily {
		t.Logf("  id=%-6d grade=%-16s type=%-12s lv=[%d..%d] job=%q exposedByNPC=%d grow=%d hasGrow=%v preReq=%v event=%v intData=%v",
			definition.ID, normalizeQuestTag(definition.Grade), normalizeQuestTag(definition.Type),
			definition.LevelMin, definition.LevelMax, definition.Job, definition.ExposedByNPC,
			definition.GrowType, definition.HasGrowType, definition.PreRequiredGroups, definition.IsEvent,
			definition.IntData)
	}
	for _, id := range []int64{10019, 2901, 12830} {
		definition, found := catalog.Find(id)
		if !found {
			t.Logf("id=%d NOT IN CATALOG", id)
			continue
		}
		fmt.Printf("WATCHED id=%d inLiveList=%v grade=%q type=%q lv=[%d..%d] job=%q exposed=%d event=%v preReq=%v\n",
			id, containsID(ids, id), normalizeQuestTag(definition.Grade), normalizeQuestTag(definition.Type),
			definition.LevelMin, definition.LevelMax, definition.Job, definition.ExposedByNPC,
			definition.IsEvent, definition.PreRequiredGroups)
	}
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
