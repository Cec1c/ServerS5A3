package dnfbridge

import (
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealPVFLevelBasedQuestClearCouponFamilyIsExact 用真档 Script.pvf 固定
// "右键的那张券到底是哪一件"这个契约。
//
// 现场道具（操作者截图 + item 10164630）：
//
//	stackable/10164001/10164630.stk  [name] Lv1~90主线任务完成券
//	  [action type] `[gm quest clear]`   [minimum level] 90   [stack limit] 1
//	stackable/10301001/10301410.stk  [name] Lv1~90主线/成就任务完成券
//	  [action type] `[lv clear]` 90      [stack limit] 1
//
// 全档 stackable 里带 `[gm quest clear]` 的**只有** 10164630，带 `[lv clear]` 的
// **只有** 10301410，所以 op515 action 167 + "运行时 PVF 券族" 两级判定不会
// 误伤别的道具。
func TestRealPVFLevelBasedQuestClearCouponFamilyIsExact(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the quest completion coupon family")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	listText, err := archive.ReadText(dungeonDropStackableList)
	if err != nil {
		t.Fatalf("read stackable list: %v", err)
	}
	listDocument, err := dnfpvf.Parse(dungeonDropStackableList, listText)
	if err != nil {
		t.Fatalf("parse stackable list: %v", err)
	}

	type coupon struct {
		id         int64
		path       string
		actionType string
		actionInts []int64
		stackLimit int64
		minLevel   int64
	}
	found := make([]coupon, 0, 2)
	for _, entry := range dnfpvf.ParseList(listDocument) {
		text, readErr := archive.ReadText("stackable/" + strings.TrimPrefix(entry.Path, "stackable/"))
		if readErr != nil {
			continue
		}
		document, parseErr := dnfpvf.Parse(entry.Path, text)
		if parseErr != nil {
			continue
		}
		actionType, foundAction := document.Text("action type")
		if !foundAction {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(actionType))
		if normalized != currentQuestClearCouponActionGmQuestClear &&
			normalized != currentQuestClearCouponActionLevelClear {
			continue
		}
		stackLimit, _ := document.Int("stack limit")
		minLevel, _ := document.Int("minimum level")
		found = append(found, coupon{
			id:         entry.ID,
			path:       entry.Path,
			actionType: normalized,
			actionInts: document.Ints("action type"),
			stackLimit: stackLimit,
			minLevel:   minLevel,
		})
	}
	if len(found) != 2 {
		t.Fatalf("runtime PVF level-based quest clear coupons=%+v, want exactly 10164630 and 10301410", found)
	}
	byID := make(map[int64]coupon, len(found))
	for _, item := range found {
		byID[item.id] = item
	}
	gmClear, ok := byID[10164630]
	if !ok {
		t.Fatalf("10164630 (Lv1~90主线任务完成券) left the [gm quest clear] family: %+v", found)
	}
	if gmClear.actionType != currentQuestClearCouponActionGmQuestClear ||
		!strings.HasSuffix(
			strings.TrimPrefix(testQuestClearCouponPath, "stackable/"),
			strings.TrimPrefix(strings.ReplaceAll(gmClear.path, "\\", "/"), "stackable/"),
		) ||
		gmClear.stackLimit != 1 || gmClear.minLevel != 90 {
		t.Fatalf("10164630 shape changed: %+v (want path suffix %q)", gmClear, testQuestClearCouponPath)
	}
	levelClear, ok := byID[10301410]
	if !ok {
		t.Fatalf("10301410 (Lv1~90主线/成就任务完成券) left the [lv clear] family: %+v", found)
	}
	if levelClear.actionType != currentQuestClearCouponActionLevelClear || levelClear.stackLimit != 1 {
		t.Fatalf("10301410 shape changed: %+v", levelClear)
	}
	// [lv clear] 的等级参数就是客户端 `//lvclear N` 的 N：真档只有这一个成员，
	// 它的 N 必须不小于完成券的 89 上限，否则按 89 处理会多完成主线任务。
	if len(levelClear.actionInts) != 1 || levelClear.actionInts[0] < 89 {
		t.Fatalf("10301410 [lv clear] parameter=%v, the 89 cutoff is only equivalent when it is >= 89", levelClear.actionInts)
	}
}
