package dnfbridge

import (
	"os"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 摧毁补偿道具（强化/增幅失败把装备摧毁）的事件字段：操作者复测时全靠这一行判读
// "并进既有行了还是又开了一格"。字段名与顺序是验收口径的一部分，所以单独钉住。
func TestUpgradeDestroyBonusMergeEventFieldsNameItemSlotAndSwitch(t *testing.T) {
	grant := &alignedcmd.UpgradeDestroyBonusMerge{
		ItemID:       3243,
		GrantedCount: 7,
		PlacedCount:  7,
		Slot:         129,
		Slots:        []int16{129},
		Merged:       true,
		Created:      false,
		Identity: alignedcmd.StackGrantIdentity{
			PVFPath:       "stackable/material/vestiges_intensive.stk",
			ItemKind:      "stackable",
			StackableType: "[material]",
		},
		Container:     alignedcmd.StackGrantContainer{ListType: 0, SlotStart: 121, SlotEnd: 176},
		PlacementMode: "merge_existing_stack",
	}
	fields := upgradeDestroyBonusMergeEventFields(grant)
	if len(fields)%2 != 0 {
		t.Fatalf("event fields must be key/value pairs, got %d entries", len(fields))
	}
	got := make(map[string]any, len(fields)/2)
	for index := 0; index < len(fields); index += 2 {
		key, ok := fields[index].(string)
		if !ok {
			t.Fatalf("field %d key is not a string: %v", index, fields[index])
		}
		got[key] = fields[index+1]
	}
	want := map[string]any{
		"item_id":                             int64(3243),
		"amount":                              int64(7),
		"placed_amount":                       int64(7),
		"slot":                                int16(129),
		"slots":                               "[129]",
		"merged_into_existing_stack":          true,
		"created_new_slot":                    false,
		"stack_limit":                         int64(0),
		"stack_pvf_path":                      "stackable/material/vestiges_intensive.stk",
		"stack_item_kind":                     "stackable",
		"stack_stackable_type":                "[material]",
		"container_list":                      byte(0),
		"container_slot_start":                int16(121),
		"container_slot_end":                  int16(176),
		"placement_switch":                    "merge_existing_stack",
		"placement_switch_env":                "DNFBRIDGE_UPGRADE_DESTROY_BONUS_PLACEMENT",
		"placement_switch_default":            "merge_existing_stack",
		"placement_switch_legacy_row_literal": "merge_existing_stack_legacy_row",
		"placement_switch_rollback_literal":   "first_empty_slot",
	}
	for key, wantValue := range want {
		value, ok := got[key]
		if !ok {
			t.Fatalf("event field %q missing (got %v)", key, got)
		}
		if value != wantValue {
			t.Fatalf("event field %q = %#v, want %#v", key, value, wantValue)
		}
	}
	if upgradeDestroyBonusMergeEventName != "game-upgrade-destroy-bonus-stack-merge" {
		t.Fatalf("event name = %q", upgradeDestroyBonusMergeEventName)
	}
	// 非摧毁档必须一条都不打（nil ⇒ nil）。
	if extra := upgradeDestroyBonusMergeEventFields(nil); extra != nil {
		t.Fatalf("nil grant produced %v, want no fields", extra)
	}
}

// 真实运行时 PVF：两个升级表的 [disjoint bonus item] 及其身份必须解析成线上那两件道具
// （GM 索引：3243 强烈之痕迹、3244 浓缩的纯洁之骸；目录给出的 pvf_path 带 `stackable/`
// 前缀 —— GM 索引把这段前缀裁掉了，两处指的是同一个文件。
// 两件都 kind=stackable / stackLimit=0 = PVF 没写 [stack limit] = 无上限）。
// 这份身份就是 inventory owner 判"能不能并进既有行"的 incoming 一侧。
func TestRealPVFUpgradeDestroyBonusIdentityMatchesLiveItems(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to inspect the runtime upgrade tables")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open PVF: %v", err)
	}
	service := &Service{initialEquipmentArchive: archive}
	cases := []struct {
		mode    string
		path    string
		itemID  int
		pvfPath string
	}{
		{mode: "reinforce", path: currentUpgradePVFPath, itemID: 3243, pvfPath: "stackable/material/vestiges_intensive.stk"},
		{mode: "amplify", path: currentAmplifyUpgradePVFPath, itemID: 3244, pvfPath: "stackable/material/crystal_pure.stk"},
	}
	// 生产解析器本体（`alignedUpgradePolicyResolver`）跑一遍：只有惩罚档真的是摧毁时
	// 才会带出补偿道具与身份，这里逐档找到那个档位再断言。
	// 它内部会 logPacketEvent，而 `&Service{}` 的 packetLog 是 nil ——
	// packetLogger.Event 对 nil 接收者是 no-op，所以这里不需要日志夹具。
	resolver := service.alignedUpgradePolicyResolver()
	for _, testCase := range cases {
		text, err := archive.ReadText(testCase.path)
		if err != nil {
			t.Fatalf("read %s: %v", testCase.path, err)
		}
		doc, err := dnfpvf.Parse(testCase.path, text)
		if err != nil {
			t.Fatalf("parse %s: %v", testCase.path, err)
		}
		table := parseCurrentUpgradeTable(doc, text)
		if table.DisjointBonusItemID != testCase.itemID {
			t.Fatalf("%s [disjoint bonus item] = %d, want %d", testCase.path, table.DisjointBonusItemID, testCase.itemID)
		}
		resolved := false
		for level := 0; level <= 31; level++ {
			policy, err := resolver(testCase.mode, level)
			if err != nil {
				t.Fatalf("resolve %s level %d: %v", testCase.mode, level, err)
			}
			if policy.PenaltyType != 3 {
				continue
			}
			if policy.DestroyBonusItemID != testCase.itemID {
				t.Fatalf("%s level %d destroy bonus item = %d, want %d",
					testCase.path, level, policy.DestroyBonusItemID, testCase.itemID)
			}
			if policy.DestroyBonusCount <= 0 {
				t.Fatalf("%s level %d destroy bonus count = %d, want > 0",
					testCase.path, level, policy.DestroyBonusCount)
			}
			identity := policy.DestroyBonusIdentity
			if identity.PVFPath != testCase.pvfPath {
				t.Fatalf("%s identity pvf_path = %q, want %q", testCase.path, identity.PVFPath, testCase.pvfPath)
			}
			if identity.ItemKind != "stackable" || identity.StackableType != "[material]" {
				t.Fatalf("%s identity kind/type = %q/%q, want stackable/[material]",
					testCase.path, identity.ItemKind, identity.StackableType)
			}
			if identity.StackLimit != 0 {
				t.Fatalf("%s identity stack limit = %d, want 0 (PVF writes no [stack limit] = unlimited)",
					testCase.path, identity.StackLimit)
			}
			// 落位容器必须来自唯一真源 currentPVFItemTargetContainer：`[material]`
			// 属于 list 0 121..176。2026-09-28 现场那版把它放进 0:12（装备页 9..64），
			// 玩家在材料页里那一堆旁边永远看不到它。
			container := policy.DestroyBonusContainer
			if container.ListType != 0 || container.SlotStart != 121 || container.SlotEnd != 176 {
				t.Fatalf("%s container = %+v, want list 0 slots 121..176 (material segment)",
					testCase.path, container)
			}
			resolved = true
			t.Logf("%s: level=%d penalty=3 bonus_item=%d bonus_count=%d pvf=%s kind=%s type=%s stack_limit=%d container=list%d %d..%d",
				testCase.path, level, policy.DestroyBonusItemID, policy.DestroyBonusCount,
				identity.PVFPath, identity.ItemKind, identity.StackableType, identity.StackLimit,
				container.ListType, container.SlotStart, container.SlotEnd)
			break
		}
		if !resolved {
			t.Fatalf("%s: no level 0..31 resolves to penalty_type=3 (compensation never granted)", testCase.path)
		}
	}
}
