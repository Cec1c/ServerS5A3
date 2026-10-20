package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 真实 PVF 回归：「礼包选择属性 → 发放行携带的属性」逐下标对齐。
//
// 现场（2026-09-25，香港 packet_log.txt / conn_id=game-000006 / test2=char3 /
// dnf:10001，2018兽人守护神礼包 490701976）：操作者选了**精神**的头发时装，
// 客户端 op518 发上来的下标确实是 1，服务端也把 1 存进了 extra `ext_data0`，
// 但发回客户端的 0x77 行把它写在 +0x0A（强化字节），而客户端读的是 +0x0B 的
// u16 ⇒ 每件装扮都显示下标 0，头发/帽子下标 0 就是「智力」。
//
// 这个文件把"下标 i ⇒ 发放行携带的属性 == PVF 在该 item 上声明的第 i 项"
// 逐项钉死，并逐字节钉住"选精神 ⇒ 发精神"。
const (
	realPVFAvatarAbilityStrengthStat     = "[PHYSICAL_ATTACK]"  // 力量
	realPVFAvatarAbilityVitalityStat     = "[PHYSICAL_DEFENSE]" // 体力
	realPVFAvatarAbilityIntelligenceStat = "[MAGICAL_ATTACK]"   // 智力
	realPVFAvatarAbilitySpiritStat       = "[MAGICAL_DEFENSE]"  // 精神
)

// TestCurrentItemListAvatarAbilityIsProjectedIntoTheDurabilityWord 是不依赖真实
// PVF 的行投影回归：背包行与穿戴行都必须把装扮的选定属性写进 +0x0B 的 u16，并把
// +0x0A（强化字节）留 0；普通装备（0x0A=强化、0x0B=耐久）一个字节都不能变。
func TestCurrentItemListAvatarAbilityIsProjectedIntoTheDurabilityWord(t *testing.T) {
	avatarExtra := map[string]string{
		"item_kind":      "avatar",
		"equipment_type": "[hair avatar]",
		"pvf_path":       "equipment/character/priest/at_avatar/hair/hair.equ",
		"ext_data0":      "1",
	}
	bag := currentItemListEntryFromStack(currentExeInventoryAvatarListType, 7, dnfrepo.ItemStack{
		ItemID: 415560044, Count: 1, Extra: avatarExtra,
	})
	if got := realPVFAvatarAbilityWord(bag.data[:]); got != 1 {
		t.Fatalf("avatar bag row ability word=%d want 1 row=%x", got, bag.data[:])
	}
	if got := bag.data[currentItemListExtDataByteOffset]; got != 0 {
		t.Fatalf("avatar bag row +0x0A=%d want 0", got)
	}

	worn, ok := currentItemListEntryFromEquipment(dnfrepo.EquipmentEntry{
		SlotIndex: 1, ItemID: 415560044, Extra: avatarExtra,
	})
	if !ok {
		t.Fatal("worn avatar row was rejected")
	}
	if got := realPVFAvatarAbilityWord(worn.data[:]); got != 1 {
		t.Fatalf("worn avatar row ability word=%d want 1 row=%x", got, worn.data[:])
	}
	if got := worn.data[currentItemListExtDataByteOffset]; got != 0 {
		t.Fatalf("worn avatar row +0x0A=%d want 0", got)
	}
	update := currentEquipmentUpdateEntryFromEquipment(dnfrepo.EquipmentEntry{
		SlotIndex: 1, ItemID: 415560044, Extra: avatarExtra,
	})
	if got := binary.LittleEndian.Uint16(update.data[currentItemListDurabilityOffset : currentItemListDurabilityOffset+2]); got != 1 {
		t.Fatalf("worn avatar update row ability word=%d want 1 row=%x", got, update.data[:])
	}
	if got := update.data[currentItemListExtDataByteOffset]; got != 0 {
		t.Fatalf("worn avatar update row +0x0A=%d want 0", got)
	}

	// 普通装备：+0x0A 仍是强化（packed），+0x0B 仍是耐久。
	weaponExtra := map[string]string{
		"item_kind":        "equipment",
		"equipment_type":   "[weapon]",
		"ext_data0":        "12",
		"packed_flag_byte": "12",
		"durability":       "45",
	}
	weapon, _ := currentItemListEntryFromEquipment(dnfrepo.EquipmentEntry{
		SlotIndex: 12, ItemID: 101030741, Extra: weaponExtra,
	})
	if weapon.data[currentItemListExtDataByteOffset] != 12 ||
		binary.LittleEndian.Uint16(weapon.data[currentItemListDurabilityOffset:currentItemListDurabilityOffset+2]) != 45 {
		t.Fatalf("weapon row = %x want +0x0A=12 +0x0B=45", weapon.data[:0x10])
	}
	weaponUpdate := currentEquipmentUpdateEntryFromEquipment(dnfrepo.EquipmentEntry{
		SlotIndex: 12, ItemID: 101030741, Extra: weaponExtra,
	})
	if weaponUpdate.data[currentItemListExtDataByteOffset] != 12 ||
		binary.LittleEndian.Uint16(weaponUpdate.data[currentItemListDurabilityOffset:currentItemListDurabilityOffset+2]) != 45 {
		t.Fatalf("weapon update row = %x want +0x0A=12 +0x0B=45", weaponUpdate.data[:0x10])
	}
}

func TestRealPVFCeraPackageAvatarAttributeFollowsTheSelectedIndex(t *testing.T) {
	fixture, archive := newRealPVFAvatarAttributeFixture(t, 490701976, 8, 84)
	definition, err := resolveCurrentCeraPackageDefinition(fixture.catalog, 490701976)
	if err != nil {
		t.Fatal(err)
	}
	// 下装 415510038 的 [avatar select ability] 是整档里少见的"四项齐全"清单：
	// 0 [HP MAX] 1 [MP MAX] 2 [EQUIPMENT_PHYSICAL_DEFENSE]
	// 3 力量 4 体力 5 智力 6 精神 —— 一个 item 就能覆盖四种属性。
	const pantsItemID = 415510038
	pantsAbilities := realPVFAvatarSelectAbilityList(t, archive, fixture.catalog, pantsItemID)
	if len(pantsAbilities) < 7 {
		t.Fatalf("pants avatar=%d ability list=%v want the 7-entry list", pantsItemID, pantsAbilities)
	}
	wantStats := map[int]string{
		3: realPVFAvatarAbilityStrengthStat,
		4: realPVFAvatarAbilityVitalityStat,
		5: realPVFAvatarAbilityIntelligenceStat,
		6: realPVFAvatarAbilitySpiritStat,
	}
	for index, stat := range wantStats {
		if pantsAbilities[index] != stat {
			t.Fatalf("pants avatar=%d ability[%d]=%q want %q (list=%v)", pantsItemID, index, pantsAbilities[index], stat, pantsAbilities)
		}
	}

	for index := 0; index < len(pantsAbilities); index++ {
		choices := make([]currentCeraPackageChoice, 0, len(definition.AvatarItemIDs))
		for _, itemID := range definition.AvatarItemIDs {
			option := byte(0)
			if itemID == pantsItemID {
				option = byte(index)
			}
			choices = append(choices, currentCeraPackageChoice{ItemID: itemID, Option: option})
		}
		before := realPVFAvatarSlotKeys(fixture.inventory(t))
		if _, err := fixture.service.commitCurrentCeraPackage(
			context.Background(),
			&gameSession{selectedCharacterID: 19},
			fixture.catalog,
			definition,
			currentCeraPackageOpenRequest{SourceSlot: fixture.sourceSlot, Choices: choices},
		); err != nil {
			t.Fatalf("index=%d commit: %v", index, err)
		}
		// 每次开包都会新增一行同 item 的装扮，所以只认这一次新增的那个槽位。
		stack := requireCurrentPVFNewAvatarRow(t, fixture, pantsItemID, before)
		if got := realPVFAvatarAbilityWord(stack.RawEntry); got != uint16(index) {
			t.Fatalf("index=%d raw ability word=%d want=%d raw=%x", index, got, index, stack.RawEntry)
		}
		if got := stack.Extra["ext_data0"]; got != itoaUint(index) {
			t.Fatalf("index=%d persisted ext_data0=%q want=%d", index, got, index)
		}
		t.Logf("index=%d → PVF stat %s → wire ability word %d", index, pantsAbilities[index], realPVFAvatarAbilityWord(stack.RawEntry))
	}
}

// TestRealPVFCeraPackageHairSpiritSelectionIsGrantedAsSpirit 逐字节钉住操作者的
// 那一次选择：头发 415560044 的清单是
// 0 智力([MAGICAL_ATTACK]) 1 精神([MAGICAL_DEFENSE]) 2 每分钟HP恢复 3 每分钟MP恢复 4 施放速度，
// 客户端发 1 ⇒ 发放行的属性字必须是 1，而 1 在 PVF 与
// etc/avatarabilitystringtable.etc 两侧都必须解释成「精神」。
func TestRealPVFCeraPackageHairSpiritSelectionIsGrantedAsSpirit(t *testing.T) {
	fixture, archive := newRealPVFAvatarAttributeFixture(t, 490701976, 1, 84)
	definition, err := resolveCurrentCeraPackageDefinition(fixture.catalog, 490701976)
	if err != nil {
		t.Fatal(err)
	}
	const hairItemID = 415560044 // 兽人英雄金色信仰长发（2018兽人守护神礼包 女圣职者 1）
	hairAbilities := realPVFAvatarSelectAbilityList(t, archive, fixture.catalog, hairItemID)
	if len(hairAbilities) != 5 ||
		hairAbilities[0] != realPVFAvatarAbilityIntelligenceStat ||
		hairAbilities[1] != realPVFAvatarAbilitySpiritStat {
		t.Fatalf("hair avatar=%d ability list=%v want [智力 精神 HP恢复 MP恢复 施放速度]", hairItemID, hairAbilities)
	}
	displayNames := realPVFAvatarAbilityDisplayNames(t, archive)
	if got := displayNames[realPVFAvatarAbilitySpiritStat]; got != "精神" {
		t.Fatalf("display name of %s = %q want 精神", realPVFAvatarAbilitySpiritStat, got)
	}
	if got := displayNames[realPVFAvatarAbilityIntelligenceStat]; got != "智力" {
		t.Fatalf("display name of %s = %q want 智力", realPVFAvatarAbilityIntelligenceStat, got)
	}

	choices := make([]currentCeraPackageChoice, 0, len(definition.AvatarItemIDs))
	for _, itemID := range definition.AvatarItemIDs {
		option := byte(0)
		if itemID == hairItemID {
			option = 1 // 精神
		}
		choices = append(choices, currentCeraPackageChoice{ItemID: itemID, Option: option})
	}
	if _, err := fixture.service.commitCurrentCeraPackage(
		context.Background(),
		&gameSession{selectedCharacterID: 19},
		fixture.catalog,
		definition,
		currentCeraPackageOpenRequest{SourceSlot: fixture.sourceSlot, Choices: choices},
	); err != nil {
		t.Fatal(err)
	}
	stack := requireCurrentPVFAvatarRow(t, fixture, hairItemID, 1)
	if got := realPVFAvatarAbilityWord(stack.RawEntry); got != 1 {
		t.Fatalf("选精神 ⇒ 属性字=%d want=1 raw=%x", got, stack.RawEntry)
	}
	if got := stack.RawEntry[currentItemListExtDataByteOffset]; got != 0 {
		t.Fatalf("装扮行的 +0x0A 强化字节=%d want=0 raw=%x", got, stack.RawEntry)
	}
	if got := stack.Extra["ext_data0"]; got != "1" {
		t.Fatalf("persisted ext_data0=%q want 1", got)
	}
	if got := hairAbilities[realPVFAvatarAbilityWord(stack.RawEntry)]; got != realPVFAvatarAbilitySpiritStat {
		t.Fatalf("发放行属性字=1 在 PVF 里解释成 %q want %q", got, realPVFAvatarAbilitySpiritStat)
	}
}

// TestRealPVFOperatorOp518BodyReplaysToTheSelectedAttributes 用现场报文逐字节回放
// （body 来自 packet_log.txt 的 `RECV cmd=1 type=518`），确认客户端发上来的下标
// 与服务端发放行携带的属性逐项一致。
func TestRealPVFOperatorOp518BodyReplaysToTheSelectedAttributes(t *testing.T) {
	const liveBodyHex = "5400085dcac418016cf1c418017618c518022955c418020e07c4181a162ec41800367cc4180449a3c41800"
	body, err := hex.DecodeString(liveBodyHex)
	if err != nil {
		t.Fatal(err)
	}
	request, err := parseCurrentCeraPackageOpenRequest(body)
	if err != nil {
		t.Fatalf("parse live op518 body: %v", err)
	}
	if request.SourceSlot != 84 || len(request.Choices) != 8 {
		t.Fatalf("live body slot=%d choices=%d", request.SourceSlot, len(request.Choices))
	}
	fixture, _ := newRealPVFAvatarAttributeFixture(t, 490701976, 1, request.SourceSlot)
	definition, err := resolveCurrentCeraPackageDefinition(fixture.catalog, 490701976)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.commitCurrentCeraPackage(
		context.Background(),
		&gameSession{selectedCharacterID: 19},
		fixture.catalog,
		definition,
		request,
	); err != nil {
		t.Fatal(err)
	}
	for _, choice := range request.Choices {
		stack := requireCurrentPVFAvatarRow(t, fixture, choice.ItemID, uint16(choice.Option))
		if got := realPVFAvatarAbilityWord(stack.RawEntry); got != uint16(choice.Option) {
			t.Fatalf("avatar=%d option=%d wire ability word=%d raw=%x", choice.ItemID, choice.Option, got, stack.RawEntry)
		}
		t.Logf("avatar=%d option=%d wire ability word=%d", choice.ItemID, choice.Option, realPVFAvatarAbilityWord(stack.RawEntry))
	}
}

// realPVFAvatarAbilityWord 读 0x77 行里客户端真正读的那个 u16（+0x0B）。
func realPVFAvatarAbilityWord(raw []byte) uint16 {
	if len(raw) < currentItemListDurabilityOffset+2 {
		return 0
	}
	return binary.LittleEndian.Uint16(raw[currentItemListDurabilityOffset : currentItemListDurabilityOffset+2])
}

func itoaUint(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// newRealPVFAvatarAttributeFixture 复用 cera_package_selection_real_pvf_test.go 的
// 真实 PVF 夹具，同时把 archive 交出来给断言读文档。
func newRealPVFAvatarAttributeFixture(t *testing.T, sourceItemID uint32, sourceCount int, sourceSlot int16) (realCeraPackageFixture, *platformpvf.Archive) {
	t.Helper()
	pvfPath := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to verify the real avatar attribute chain")
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: pvfPath})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newRealCeraPackageFixture(t, sourceItemID, sourceCount, sourceSlot)
	return fixture, archive
}

// realPVFAvatarSlotKeys 记录装扮栏当前占用的槽位，用于区分"这一次开包新增的行"。
func realPVFAvatarSlotKeys(inventory dnfrepo.InventoryRecord) map[string]struct{} {
	out := make(map[string]struct{}, len(inventory.Slots))
	for key := range inventory.Slots {
		out[key] = struct{}{}
	}
	return out
}

// requireCurrentPVFNewAvatarRow 取这一次开包新增的那一行装扮（item id 匹配且槽位
// 不在 before 里）。
func requireCurrentPVFNewAvatarRow(t *testing.T, fixture realCeraPackageFixture, itemID uint32, before map[string]struct{}) dnfrepo.ItemStack {
	t.Helper()
	inventory := fixture.inventory(t)
	found := false
	var stack dnfrepo.ItemStack
	for key, candidate := range inventory.Slots {
		if _, existed := before[key]; existed {
			continue
		}
		listType, _, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != currentExeInventoryAvatarListType || uint32(candidate.ItemID) != itemID {
			continue
		}
		if found {
			t.Fatalf("avatar=%d was granted more than once in one open", itemID)
		}
		found = true
		stack = candidate
	}
	if !found {
		t.Fatalf("avatar=%d was not granted into a new avatar slot", itemID)
	}
	return stack
}

// requireCurrentPVFAvatarRow 在装扮栏（list 1）里按 item id 找那一行；找不到直接失败。
func requireCurrentPVFAvatarRow(t *testing.T, fixture realCeraPackageFixture, itemID uint32, wantAbility uint16) dnfrepo.ItemStack {
	t.Helper()
	inventory := fixture.inventory(t)
	found := false
	var stack dnfrepo.ItemStack
	for key, candidate := range inventory.Slots {
		listType, _, ok := parseSceneInventorySlotKey(key)
		if !ok || listType != currentExeInventoryAvatarListType || uint32(candidate.ItemID) != itemID {
			continue
		}
		found = true
		stack = candidate
	}
	if !found {
		t.Fatalf("avatar=%d is missing from the avatar list (want ability=%d)", itemID, wantAbility)
	}
	return stack
}

// realPVFAvatarSelectAbilityList 读一件装扮自己的 [avatar select ability]，返回
// "下标 → 属性名"的有序清单（PVF 原文声明顺序）。
func realPVFAvatarSelectAbilityList(t *testing.T, archive *platformpvf.Archive, catalog *pvfDungeonDropCatalog, itemID uint32) []string {
	t.Helper()
	definition, err := catalog.ResolveItem(itemID)
	if err != nil {
		t.Fatalf("resolve avatar=%d: %v", itemID, err)
	}
	text, err := archive.ReadText(definition.PVFPath)
	if err != nil {
		t.Fatalf("read avatar=%d %s: %v", itemID, definition.PVFPath, err)
	}
	document, err := dnfpvf.Parse(definition.PVFPath, text)
	if err != nil {
		t.Fatalf("parse avatar=%d %s: %v", itemID, definition.PVFPath, err)
	}
	section, ok := document.Section("avatar select ability")
	if !ok {
		t.Fatalf("avatar=%d %s has no [avatar select ability]", itemID, definition.PVFPath)
	}
	out := []string{}
	for offset := 0; offset+1 < len(section); {
		if section[offset].Kind != dnfpvf.TokenInt || section[offset].Int != int64(len(out)) {
			offset++
			continue
		}
		if section[offset+1].Kind != dnfpvf.TokenString {
			offset++
			continue
		}
		out = append(out, strings.TrimSpace(section[offset+1].Value))
		offset += 4
	}
	return out
}

// realPVFAvatarAbilityDisplayNames 读 etc/avatarabilitystringtable.etc 的
// `[avatar ability string table]`：属性名 → 客户端中文显示名。
func realPVFAvatarAbilityDisplayNames(t *testing.T, archive *platformpvf.Archive) map[string]string {
	t.Helper()
	text, err := archive.ReadText("etc/avatarabilitystringtable.etc")
	if err != nil {
		t.Fatal(err)
	}
	document, err := dnfpvf.Parse("etc/avatarabilitystringtable.etc", text)
	if err != nil {
		t.Fatal(err)
	}
	section, ok := document.Section("avatar ability string table")
	if !ok {
		t.Fatal("etc/avatarabilitystringtable.etc has no [avatar ability string table]")
	}
	out := map[string]string{}
	for offset := 0; offset+1 < len(section); offset += 2 {
		if section[offset].Kind != dnfpvf.TokenString || section[offset+1].Kind != dnfpvf.TokenString {
			continue
		}
		out[strings.TrimSpace(section[offset].Value)] = strings.TrimSpace(section[offset+1].Value)
	}
	return out
}
