package quest

import (
	"context"
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// TestRealScriptPVFMainStoryLevelCeilingBacksTheCouponCutoff 用真档 Script.pvf
// 固定完成券的等级上限与"PVF 里到底有没有更高的主线"这两件事的关系。
//
// 结论（2026-09-25，真档 2996 条任务、其中 698 条 `[epic]`）：
//
//   - 698 条 `[epic]` 的 `[level]` **上限全是 99**（开放式），**下限最高只有 89**；
//   - 券的上限取券自己 PVF 说明里的 90（「完成Lv1至Lv90的主线任务」+
//     `[minimum level] 90`，客户端右键时补发的也是 `//lvclear 90`）；
//   - 因此 `CouponClearCutoff(90) == 90` 与 89 在这份 PVF 上选中的主线集合
//     **逐条相同**（见 TestRealScriptPVFCouponCutoffIsTheCopyCeilingAndCoversTheManualSweep
//     的日志：616 = 616）。真正修掉"父已完成、子仍可接取"的是 `[sub]` 闭包，
//     不是这 1 级的差别。
//
// 这条测试在 PVF 更新后立刻失败的条件是"出现了 `[level]` 下限高于券上限的史诗"，
// 那时必须重新推导 CouponMainQuestClearMaxLevel（否则券会静默漏掉内容）。
func TestRealScriptPVFMainStoryLevelCeilingBacksTheCouponCutoff(t *testing.T) {
	catalog := loadRealPVFQuestCatalog(t)
	epic := 0
	maxLevel := 0
	openEnded := 0
	for _, definition := range catalog.ordered {
		if normalizeQuestTag(definition.Grade) != "epic" {
			continue
		}
		epic++
		if definition.LevelMin > maxLevel {
			maxLevel = definition.LevelMin
		}
		if definition.LevelMax >= 99 {
			openEnded++
		}
	}
	if epic == 0 {
		t.Fatal("runtime PVF has no [epic] quest")
	}
	if CouponMainQuestClearMaxLevel != 90 {
		t.Fatalf("coupon ceiling=%d, want the coupon PVF text ceiling 90", CouponMainQuestClearMaxLevel)
	}
	if maxLevel > CouponMainQuestClearMaxLevel {
		t.Fatalf("runtime PVF has an [epic] at [level] floor %d, above the coupon ceiling %d; re-derive CouponMainQuestClearMaxLevel",
			maxLevel, CouponMainQuestClearMaxLevel)
	}
	if maxLevel != 89 || openEnded != epic {
		t.Fatalf("runtime PVF [epic] level floors max=%d (want 89) and open-ended=%d/%d (want all); re-derive the coupon ceiling",
			maxLevel, openEnded, epic)
	}
}

// TestRealScriptPVFCouponSweepNeverDanglesOutsideTheMainStory 固定"整段主线"
// 口径的安全边界。
//
// 完成券会替角色写下它从没接过的主线任务行，所以入选集合必须满足：
//
//  1. 只包含 `[epic]`（主线）任务——支线/成就/活动/职业任务一行都不会被造出来；
//  2. 集合里任何一条任务的 `[pre required quest]` 引用，如果不在集合内，
//     那条前置**本身也必须是一条主线任务**（不会把落点丢给一条永远不会被完成
//     的支线/成就任务）；
//  3. 覆盖率：集合覆盖角色能看到的整段主线（≥500 条）。
//
// 已知边界（本测试用 t.Logf 记录，不断言）：少数主线的前置组只由**其它职业分支**
// 的任务构成（例如 3427 的 OR 组里全是各职业专属前置）。这类任务被标记完成后
// 不会引起任何重新接取判定——整段主线都已经 completed——所以它不构成悬空状态，
// 但报告里要如实记录条数。
func TestRealScriptPVFCouponSweepNeverDanglesOutsideTheMainStory(t *testing.T) {
	catalog := loadRealPVFQuestCatalog(t)
	for _, character := range []CharacterEligibility{
		{Level: 90, Job: 0, GrowType: 0},
		{Level: 90, Job: 1, GrowType: 1},
		{Level: 90, Job: 2, GrowType: 0},
		{Level: 90, Job: 3, GrowType: 0x11},
		{Level: 90, Job: 7, GrowType: 0x21},
		{Level: 90, Job: 14, GrowType: 33},
		{Level: 90, Job: 15, GrowType: 0},
		{Level: 90, Job: 17, GrowType: 0x12},
	} {
		chosen := make(map[int64]struct{}, 700)
		for _, definition := range catalog.ordered {
			if mainStoryQuestAutoClearable(definition, character, CouponMainQuestClearMaxLevel) {
				chosen[definition.ID] = struct{}{}
			}
		}
		if len(chosen) < 500 {
			t.Fatalf("character=%+v sweep set=%d, expected the whole main story", character, len(chosen))
		}
		groupOutside := 0
		for id := range chosen {
			definition, known := catalog.Find(id)
			if !known {
				t.Fatalf("chosen quest %d is not in the catalog", id)
			}
			if normalizeQuestTag(definition.Grade) != "epic" {
				t.Fatalf("non-main-story quest %d (%q) entered the sweep", id, definition.Grade)
			}
			for _, group := range definition.PreRequiredGroups {
				complete := len(group) > 0
				for _, required := range group {
					if _, in := chosen[required]; in {
						continue
					}
					complete = false
					other, known := catalog.Find(required)
					if !known || normalizeQuestTag(other.Grade) != "epic" {
						t.Fatalf("quest %d requires %d which is outside the sweep set and not main story (known=%t)",
							id, required, known)
					}
				}
				if !complete {
					groupOutside++
				}
			}
		}
		t.Logf("character=%+v sweep set=%d pre-required-groups-completed-only-by-other-class-branches=%d",
			character, len(chosen), groupOutside)
	}
}

func loadRealPVFQuestCatalog(t *testing.T) *Catalog {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the quest completion coupon against the runtime PVF")
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
	return catalog
}
