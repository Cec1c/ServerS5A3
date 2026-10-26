package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// round12：process 事务里的目标校验（真实 v106 PVF + 真实 handler）。
//
// 现场口径：start 已经把目标限制进本职业 catalog（validateCurrentEpicProductionTarget
// -> catalog.allows(jobTag, id)），而 process 之前只校验 Stats target > 0 —— 一个不在
// catalog 任何真实 production item 列表里的损坏目标（越界值/普通武器）照样能扣催化剂。
// 本用例经 handleCurrentEpicProductionProcess + 真档 Script.pvf 钉三件事：
//
//  1. target = -1 / 0 / MaxInt64 / MaxUint32+1 / MaxUint32+1+108000520（低 32 位截断
//     回一个合法目标号）/ 普通武器（真实 [weapon]，但不属于任何职业的 production item）
//     全部拒绝，且角色 Stats、背包、周计数逐值不变 —— 不允许"先扣料再报错"的部分提交。
//  2. 固定资源样本 108000520 在临界加工（charge = maxChargePoint-1）仍成功：
//     ACK charge = maxChargePoint、周计数 +1、材料各扣 1。
//  3. 目标校验不重新按当前职业筛选：同一把真档 production 武器换成别的职业也放行。
func TestRealPVFEpicProductionProcessTargetRound12(t *testing.T) {
	service, session, connection := newCurrentEpicProductionRealPVFService(
		t, epicProductionSessionAccount, epicProductionAccountScopeSession, epicProductionInfoPushAlways)
	service.options.currentClientVersion = "official-24.9"
	ctx := context.Background()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatal("repository group missing")
	}
	catalog, err := parseCurrentEpicProductionCatalog(service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	// 真档锚点：固定资源样本 108000520 确实在真档 [at priest] 的 production 列表里。
	if !catalog.allows("[at priest]", epicProductionLiveTargetItemID) {
		t.Fatalf("real PVF [at priest] production list lacks sample target %d", epicProductionLiveTargetItemID)
	}
	// 测试侧自己枚举 catalog.targetsByJob 的并集（不依赖被测代码的方法），
	// 旧码 overlay 下这份测试仍能编译，红必须是"真实 handler 拒绝/接受行为"差异。
	productionTargets := round12ProductionTargetUnion(catalog)
	if _, ok := productionTargets[epicProductionLiveTargetItemID]; !ok {
		t.Fatalf("real PVF production union lacks sample target %d", epicProductionLiveTargetItemID)
	}
	items, err := newPVFDungeonDropCatalog(service.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryWeapon := round12OrdinaryWeaponItemID(t, items, productionTargets)
	t.Logf("round12 ordinary weapon outside every production list: %d", ordinaryWeapon)

	seed := func(t *testing.T, target, charge, weekly int64, job string) {
		t.Helper()
		character := dnfrepo.CharacterRecord{
			CharacterID: "3",
			AccountID:   epicProductionSessionAccount,
			Job:         job,
			Level:       90,
			Stats: map[string]int64{
				epicProductionTestStatSeed:           1,
				currentEpicProductionTargetStat:      target,
				currentEpicProductionChargeStat:      charge,
				currentEpicProductionWeeklyCountStat: weekly,
				currentEpicProductionWeekStartStat:   currentEpicProductionWeekStart(time.Now().UTC()),
			},
		}
		inventory := dnfrepo.InventoryRecord{CharacterID: "3", Slots: map[string]dnfrepo.ItemStack{
			"0:122": {ItemID: 10163130, Count: 10},
			"0:127": {ItemID: 10163135, Count: 10},
		}}
		if err := repositories.Character.Save(ctx, character); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory.Save(ctx, inventory); err != nil {
			t.Fatal(err)
		}
	}
	// 一行材料（催化剂 10163130 在 0:122）＝现场那条 process 请求形状。
	processBody := func() []byte {
		body := make([]byte, 2+currentEpicProductionProcessRowSize)
		binary.LittleEndian.PutUint16(body[0:2], 1)
		binary.LittleEndian.PutUint32(body[2:6], 10163130)
		binary.LittleEndian.PutUint32(body[6:10], 122)
		return body
	}
	processMsgID := service.currentEpicProductionCommandID(dnfenum.CmdPacketEpicProductionProcess)

	for _, test := range []struct {
		name        string
		target      int64
		truncatesTo uint32
	}{
		{"negative_target", -1, 0},
		{"zero_target", 0, 0},
		{"max_int64_target", math.MaxInt64, 0},
		{"above_max_uint32_target", int64(math.MaxUint32) + 1, 0},
		// 越界值低 32 位正好截断回一个合法 production 武器号：按截断后的口径它"合法"，
		// 但按持久化 int64 值仍然越界，必须在扣料前拒绝。
		{"above_max_uint32_truncating_to_sample", int64(math.MaxUint32) + 1 + int64(epicProductionLiveTargetItemID), epicProductionLiveTargetItemID},
		{"ordinary_weapon_target", int64(ordinaryWeapon), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.truncatesTo != 0 && uint32(test.target) != test.truncatesTo {
				t.Fatalf("fixture drift: %d truncates to %d want %d", test.target, uint32(test.target), test.truncatesTo)
			}
			seed(t, test.target, 12345, 2, "14")
			beforeCharacter, beforeFound, err := repositories.Character.Load(ctx, "3")
			if err != nil || !beforeFound {
				t.Fatalf("character before found=%t err=%v", beforeFound, err)
			}
			beforeInventory, inventoryFound, err := repositories.Inventory.Load(ctx, "3")
			if err != nil || !inventoryFound {
				t.Fatalf("inventory before found=%t err=%v", inventoryFound, err)
			}
			connection.write.Reset()
			if err := service.handleCurrentEpicProductionProcess(session, processBody()); err != nil {
				t.Fatal(err)
			}
			packet, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
			// 回包形状用 Errorf：旧码接受损坏目标时会提交扣料并回成功 ACK，
			// 后面的逐值断言必须照样跑完并把"旧码真的扣了料"打出来。
			if len(rest) != 0 {
				t.Errorf("rejected target wrote trailing bytes=%d", len(rest))
			}
			if packet.Header.MsgID != processMsgID || packet.Header.Classification != 1 ||
				!bytes.Equal(packet.Body, []byte{0, currentEpicProductionGenericError}) {
				t.Errorf("rejected target reply msg=%d class=%d body=%x want failure 00%02x",
					packet.Header.MsgID, packet.Header.Classification, packet.Body, currentEpicProductionGenericError)
			}
			afterCharacter, afterFound, err := repositories.Character.Load(ctx, "3")
			if err != nil || !afterFound {
				t.Fatalf("character after found=%t err=%v", afterFound, err)
			}
			afterInventory, _, err := repositories.Inventory.Load(ctx, "3")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeCharacter, afterCharacter) || !reflect.DeepEqual(beforeInventory, afterInventory) {
				t.Errorf("rejected target changed persisted state: statsBefore=%v statsAfter=%v itemsBefore=%v itemsAfter=%v",
					beforeCharacter.Stats, afterCharacter.Stats, beforeInventory.Slots, afterInventory.Slots)
			}
		})
	}

	for _, test := range []struct {
		name string
		job  string
	}{
		{"live_target_last_charge", "14"},
		{"live_target_other_job", "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			maxCharge := int64(catalog.maxChargePoint)
			seed(t, int64(epicProductionLiveTargetItemID), maxCharge-1, 2, test.job)
			connection.write.Reset()
			if err := service.handleCurrentEpicProductionProcess(session, processBody()); err != nil {
				t.Fatal(err)
			}
			ack, rest := splitLongHengGameServerUpperPacket(t, connection.write.Bytes())
			if ack.Header.MsgID != processMsgID || ack.Header.Classification != 1 {
				t.Fatalf("process ACK msg=%d class=%d", ack.Header.MsgID, ack.Header.Classification)
			}
			// official-24.9：body = u8 成功 + u32 charge + u8 已用次数 + u8 大成功。
			if len(ack.Body) != 7 || ack.Body[0] != 1 || binary.LittleEndian.Uint32(ack.Body[1:5]) != uint32(maxCharge) || ack.Body[5] != 3 {
				t.Fatalf("process ACK body=%x want charge=%d count=3", ack.Body, maxCharge)
			}
			update, rest := splitLongHengGameServerUpperPacket(t, rest)
			if update.Header.MsgID != 14 {
				t.Fatalf("material update msg=%d", update.Header.MsgID)
			}
			info, trailing := splitLongHengGameServerUpperPacket(t, rest)
			if info.Header.MsgID != service.epicProductionInfoMsgID() || info.Header.Classification != 0 || len(trailing) != 0 {
				t.Fatalf("info push msg=%d class=%d trailing=%d", info.Header.MsgID, info.Header.Classification, len(trailing))
			}
			character, found, err := repositories.Character.Load(ctx, "3")
			if err != nil || !found {
				t.Fatalf("character found=%t err=%v", found, err)
			}
			if character.Stats[currentEpicProductionTargetStat] != int64(epicProductionLiveTargetItemID) ||
				character.Stats[currentEpicProductionChargeStat] != maxCharge ||
				character.Stats[currentEpicProductionWeeklyCountStat] != 3 {
				t.Fatalf("persisted stats=%v", character.Stats)
			}
			inventory, _, err := repositories.Inventory.Load(ctx, "3")
			if err != nil {
				t.Fatal(err)
			}
			if inventory.Slots["0:122"].Count != 9 || inventory.Slots["0:127"].Count != 9 {
				t.Fatalf("consumed items=%v", inventory.Slots)
			}
		})
	}
}

// round12ProductionTargetUnion 在测试侧枚举 catalog.targetsByJob 的并集。
// 刻意不调用被测代码里新增的查询方法：旧码 overlay 下这份测试必须能编译，
// 红绿差异必须来自真实 handler 的拒绝/接受行为。
func round12ProductionTargetUnion(catalog *currentEpicProductionCatalog) map[uint32]struct{} {
	targets := make(map[uint32]struct{})
	for _, byJob := range catalog.targetsByJob {
		for itemID := range byJob {
			targets[itemID] = struct{}{}
		}
	}
	return targets
}

// round12OrdinaryWeaponItemID 在真档装备表里挑一把**真实** [weapon]（普通武器），
// 且它不属于任何职业的 production item 列表。按装备号升序、只解析路径里带 weapon/
// 的前若干条（界内、确定），取第一条命中；界内挑不到就直接失败并列出候选数，
// 不用"看起来像武器"的写死号糊过去。
func round12OrdinaryWeaponItemID(t *testing.T, items *pvfDungeonDropCatalog, productionTargets map[uint32]struct{}) uint32 {
	t.Helper()
	candidates := make([]uint32, 0, 128)
	for itemID, reference := range items.itemRefs {
		if reference.kind != dungeonDropItemEquipment || !strings.Contains(reference.path, "weapon/") {
			continue
		}
		if _, isProductionTarget := productionTargets[itemID]; isProductionTarget {
			continue
		}
		candidates = append(candidates, itemID)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	const inspectedCap = 64
	inspected := 0
	for _, itemID := range candidates {
		if inspected == inspectedCap {
			break
		}
		inspected++
		definition, err := items.ResolveItem(itemID)
		if err != nil || definition.Kind != dungeonDropItemEquipment ||
			currentRentalJobTag(definition.EquipmentType) != "[weapon]" {
			continue
		}
		return itemID
	}
	t.Fatalf("no resolvable ordinary [weapon] outside every production list within %d of %d weapon candidates",
		inspectedCap, len(candidates))
	return 0
}
