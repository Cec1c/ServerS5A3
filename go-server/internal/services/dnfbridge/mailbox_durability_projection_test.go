package dnfbridge

import (
	"os"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/itemquality"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场根因（操作者 2026-09-24 确认"0 耐久的只发现了装备"）：
// GM 面板发放的邮件附件只有 {ItemID, Count}，装备类道具落地后既没有 durability
// 也没有 max_durability —— 客户端按 0 耐久显示，服务端修理路径按"缺证据失败关闭"，
// 于是"领到背包也修不了"。
//
// 这条钉子覆盖领取端的投影：运行时 PVF 的 [durability] 必须变成 DefaultExtra 里的
// durability + max_durability（mail 模块领取时写进堆栈），而堆叠物绝不允许带上耐久。
func TestMailboxItemResolutionProjectsPVFDurabilityForEquipmentOnly(t *testing.T) {
	service := &Service{pvfItemCatalog: mustMailboxDurabilityCatalog(t)}
	resolver, err := service.alignedMailboxItemResolverForCommand(dnfenum.CmdPacketMailboxExtractItem)
	if err != nil {
		t.Fatalf("mailbox resolver: %v", err)
	}
	if resolver == nil {
		t.Fatal("mailbox extract must have a runtime-PVF resolver")
	}

	equipment, err := resolver(mailboxDurabilityTestEquipmentID)
	if err != nil {
		t.Fatalf("resolve equipment: %v", err)
	}
	if equipment.Kind != string(dungeonDropItemEquipment) {
		t.Fatalf("equipment kind = %q", equipment.Kind)
	}
	if got := equipment.DefaultExtra["durability"]; got != "57" {
		t.Fatalf("equipment durability projection = %q, want the runtime PVF [durability] 57 (%+v)", got, equipment.DefaultExtra)
	}
	if got := equipment.DefaultExtra["max_durability"]; got != "57" {
		t.Fatalf("equipment max_durability projection = %q, want 57", got)
	}
	// 实例身份：没有合法的 quality_seed，op19 穿上时会因为 raw 行重建失败被静默拒绝
	// （客户端本地穿上 → 重登掉下来）。
	seedRaw := equipment.DefaultExtra["quality_seed"]
	seed, seedErr := strconv.ParseUint(seedRaw, 10, 32)
	if seedErr != nil || !itemquality.Valid(uint32(seed)) {
		t.Fatalf("equipment quality seed = %q (err=%v), want a valid instance seed", seedRaw, seedErr)
	}

	// 堆叠物没有耐久语义：PVF 里就算写了 [durability]，也不许流进堆栈。
	stackable, err := resolver(mailboxDurabilityTestStackableID)
	if err != nil {
		t.Fatalf("resolve stackable: %v", err)
	}
	if _, ok := stackable.DefaultExtra["durability"]; ok {
		t.Fatalf("stackable must not carry durability: %+v", stackable.DefaultExtra)
	}
	if _, ok := stackable.DefaultExtra["max_durability"]; ok {
		t.Fatalf("stackable must not carry max_durability: %+v", stackable.DefaultExtra)
	}
	if _, ok := stackable.DefaultExtra["quality_seed"]; ok {
		t.Fatalf("stackable must not carry a quality seed: %+v", stackable.DefaultExtra)
	}
}

// 附件自己带了实例种子（玩家邮寄的装备）时绝不覆盖；非装备一律不写。
func TestCurrentMailAttachmentQualitySeedExtraKeepsInstanceSeed(t *testing.T) {
	equipment := dungeonDropItemDefinition{Kind: dungeonDropItemEquipment}
	kept, err := currentMailAttachmentQualitySeedExtra(map[string]string{"quality_seed": "42"}, equipment)
	if err != nil || kept["quality_seed"] != "42" {
		t.Fatalf("existing instance seed overwritten: %+v err=%v", kept, err)
	}

	fresh, err := currentMailAttachmentQualitySeedExtra(nil, equipment)
	if err != nil {
		t.Fatalf("generate fresh seed: %v", err)
	}
	parsed, parseErr := strconv.ParseUint(fresh["quality_seed"], 10, 32)
	if parseErr != nil || !itemquality.Valid(uint32(parsed)) {
		t.Fatalf("fresh seed %q is not a valid instance identity (err=%v)", fresh["quality_seed"], parseErr)
	}

	stackable, err := currentMailAttachmentQualitySeedExtra(nil, dungeonDropItemDefinition{Kind: dungeonDropItemStackable})
	if err != nil || len(stackable) != 0 {
		t.Fatalf("stackable gained a quality seed: %+v err=%v", stackable, err)
	}
}

// 合并语义：缺键才补（玩家邮寄的旧装备不能被拉满），堆叠物与非装备类一律不写。
func TestMergeCurrentMailAttachmentDurabilityExtraKeepsInstanceEvidence(t *testing.T) {
	equipment := dungeonDropItemDefinition{Kind: dungeonDropItemEquipment, Durability: 57}

	fresh := mergeCurrentMailAttachmentDurabilityExtra(nil, equipment)
	if fresh["durability"] != "57" || fresh["max_durability"] != "57" {
		t.Fatalf("fresh equipment durability = %+v, want both keys 57", fresh)
	}

	worn := mergeCurrentMailAttachmentDurabilityExtra(
		map[string]string{"durability": "12", "max_durability": "57"},
		equipment,
	)
	if worn["durability"] != "12" {
		t.Fatalf("existing instance durability overwritten: %+v", worn)
	}

	if stackable := mergeCurrentMailAttachmentDurabilityExtra(nil, dungeonDropItemDefinition{
		Kind:       dungeonDropItemStackable,
		Durability: 57,
	}); len(stackable) != 0 {
		t.Fatalf("stackable durability must never be projected: %+v", stackable)
	}
	if noDurability := mergeCurrentMailAttachmentDurabilityExtra(nil, dungeonDropItemDefinition{
		Kind: dungeonDropItemEquipment,
	}); len(noDurability) != 0 {
		t.Fatalf("equipment without a PVF [durability] must not invent one: %+v", noDurability)
	}

	if !currentMailAttachmentDurabilityExtraMissing(nil) {
		t.Fatal("nil Extra must read as missing durability")
	}
	if currentMailAttachmentDurabilityExtraMissing(map[string]string{"durability": "12", "max_durability": "57"}) {
		t.Fatal("complete evidence must read as present")
	}
	if !currentMailAttachmentDurabilityExtraMissing(map[string]string{"durability": "12"}) {
		t.Fatal("half evidence must read as missing: the repair path needs both keys")
	}
}

const (
	mailboxDurabilityTestEquipmentID uint32 = 9001
	mailboxDurabilityTestStackableID uint32 = 490701731
)

// 真实运行时 PVF 的一致性抽查：修理定价读的是 [durability]
// （repair_cost_pvf.go 的 resolve），而附件耐久投影读的是
// [durability] / [maximum durability] / [max durability]（dungeon_drop_catalog）。
// 抽样装备文档确认两者同源，并统计"只写了备用键"的比例 —— 那类道具在修理定价里
// MaxDurability 仍是 -1（不可修），属于同族的另一处口径问题，这里如实报告。
//
//	DNFBRIDGE_REAL_PVF_SMOKE=/opt/dnf90/data/dnf/Script.pvf \
//	  go test -buildvcs=false -count=1 -run TestMailboxDurabilityRealRuntimePVF ./internal/services/dnfbridge/
func TestMailboxDurabilityRealRuntimePVF(t *testing.T) {
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatalf("load real Script.pvf: %v", err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatalf("build real runtime-PVF catalog: %v", err)
	}

	const sampleLimit = 200
	sampled, withDurability, alternateKeyOnly := 0, 0, 0
	for itemID, reference := range catalog.itemRefs {
		if reference.kind != dungeonDropItemEquipment {
			continue
		}
		if sampled >= sampleLimit {
			break
		}
		sampled++
		definition, resolveErr := catalog.ResolveItem(itemID)
		if resolveErr != nil || definition.Durability == 0 {
			continue
		}
		withDurability++
		document, docErr := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if docErr != nil {
			t.Fatalf("read real PVF item %d at %s: %v", itemID, definition.PVFPath, docErr)
		}
		if _, found := document.Int("durability"); !found {
			alternateKeyOnly++
		}
		projected := mergeCurrentMailAttachmentDurabilityExtra(nil, definition)
		want := strconv.FormatUint(uint64(definition.Durability), 10)
		if projected["durability"] != want || projected["max_durability"] != want {
			t.Fatalf("item %d durability projection = %+v, want both keys %s", itemID, projected, want)
		}
	}
	t.Logf("real runtime PVF equipment sample: sampled=%d with_durability=%d alternate_key_only=%d",
		sampled, withDurability, alternateKeyOnly)
	if sampled == 0 {
		t.Fatal("real runtime PVF sample is empty: the equipment list did not resolve")
	}
	if withDurability == 0 {
		t.Fatalf("none of the %d sampled equipment documents declare durability; the mailbox projection would have nothing to project", sampled)
	}
}

// mustMailboxDurabilityCatalog 用运行时 PVF 的形状造一份最小目录：
// 装备 9001 声明 [durability] 57，堆叠物 490701731 声明堆叠语义（无耐久）。
func mustMailboxDurabilityCatalog(t *testing.T) *pvfDungeonDropCatalog {
	t.Helper()
	catalog, err := newPVFDungeonDropCatalog(dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "9001 `weapon/test_sword.equ`\n",
		"stackable/stackable.lst": "490701731 `material/chn_490701731.stk`\n",
		"equipment/weapon/test_sword.equ": "[name] `Test Sword`\n" +
			"[equipment type] `[weapon]`\n" +
			"[durability] 57\n" +
			"[repair price] 6400\n" +
			"[grade] 20\n",
		"stackable/material/chn_490701731.stk": "[name] `Test Material`\n" +
			"[stackable type] `[material]`\n" +
			"[stack limit] 1000\n",
	})
	if err != nil {
		t.Fatalf("build mailbox durability catalog: %v", err)
	}
	return catalog
}
