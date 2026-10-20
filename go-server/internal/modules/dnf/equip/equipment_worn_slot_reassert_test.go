package equip

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住 2026-09-28 第五轮的**方向判定本身**：equipmentWornSlotReassert
// （worn_slot_reassert_gate.go）。
//
// 判据（只用端点角色，不看时间、不看窗口）：目的端是装备槽、且客户端在它上面指名的
// 实例正是**那一格此刻穿着的那件**；来源端是主背包/装扮栏、客户端声明实例为 0、且
// 该槽在权威状态里为空 ⇒ 这条 op19 是"这件物品属于这一格"的复述 ⇒ **noop（一个字节
// 都不写、moveValue=0）**。
//
// 本文件的 golden set 全部是**线上原文**（`/opt/dnf90/logs/packet_log.txt`，
// 13 字节 legacy 头 + 28 字节 op19 body）：
//
//	:93098   t=13:24:21.46511482Z     光剑在身上那次重登的复述
//	:91884   t=13:20:21.733523941Z    同一次重登的更早一条（包体逐字节相同）
//	:76912   入站 t=10:37:20.89653374Z 同一形状在旧口径下**真的执行**成 unequip
//	:93127   t=13:27:33.985022792Z    操作者手动把背包 11 格的钝器拖上武器格 12（真穿上）
const (
	wornSlotReassertBeamswordItemID int64 = 101040493 // 圣耀救赎光剑（槽 12）
	wornSlotReassertClubItemID      int64 = 101020668 // 无惧者钝器（操作者手动穿上的那件）
	wornSlotReassertWornSlot        int16 = 12
	wornSlotReassertRelogBagSlot    int16 = 16
	wornSlotReassertEquipBagSlot    int16 = 11

	wornSlotReassertLiveRelogHex132421 = "01130029000000c97de1fe22000010000000000001000000030c006dc1050600000000ffffffff0000"
	wornSlotReassertLiveRelogHex132021 = "01130029000000805fff851f000010000000000001000000030c006dc1050600000000ffffffff0000"
	wornSlotReassertLivePreGateHex     = "01130029000000162069b12b00000a000000000001000000030c006dc1050600000000ffffffff0000"
	wornSlotReassertLiveEquipHex       = "0113002900000043e7c0e34700000b00fc73050601000000030c000000000000000000ffffffff0000"

	// wornSlotReassertLoginWearSwitch / wornSlotReassertLoginWearWindowRefresh 是登录复述
	// 那一族既有开关的名字（只在本文件里用来构造"窗口闸门全关"的档位）。
	wornSlotReassertLoginWearSwitch = "DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP"

	wornSlotReassertBeamswordPath = "equipment/character/swordman/weapon/beamsword/101040493.equ"
	wornSlotReassertClubPath      = "equipment/character/swordman/weapon/club/101020668.equ"
)

// wornSlotReassertLiveCommand 按 dnfinventory.DecodeMoveItemspaceRequest 的同一份偏移量
// 解出域层命令。本包**不能** import inventory（inventory 依赖 equip，会成环），
// 所以这里就地解 28 字节；dnfbridge 侧的用例用真解码器交叉核对同一批原文。
func wornSlotReassertLiveCommand(t *testing.T, frameHex string) MoveCommand {
	t.Helper()
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("decode live frame %q: %v", frameHex, err)
	}
	const currentMoveItemspaceFrameHeaderSize = 13
	if len(frame) != currentMoveItemspaceFrameHeaderSize+28 {
		t.Fatalf("live frame %q = %d bytes, want header+28", frameHex, len(frame))
	}
	body := frame[currentMoveItemspaceFrameHeaderSize:]
	return MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           body[0],
		SourceSlotIndex:          int16(binary.LittleEndian.Uint16(body[1:3])),
		SourceInstanceValue:      int32(binary.LittleEndian.Uint32(body[3:7])),
		MoveCount:                int32(binary.LittleEndian.Uint32(body[7:11])),
		DestinationListType:      body[11],
		DestinationSlotIndex:     int16(binary.LittleEndian.Uint16(body[12:14])),
		DestinationInstanceValue: int32(binary.LittleEndian.Uint32(body[14:18])),
	}
}

// wornSlotReassertFixture 造角色 6：装备槽 12 穿着光剑（现场行逐字节复刻），主背包按 slots 给定。
func wornSlotReassertFixture(t *testing.T, slots map[string]dnfrepo.ItemStack) (context.Context, dnfrepo.Group, *Owner) {
	t.Helper()
	return wornSlotReassertFixtureWith(t, map[string]dnfrepo.EquipmentEntry{"12": reloginBeamswordEntry(t)}, slots)
}

func wornSlotReassertFixtureWith(
	t *testing.T,
	entries map[string]dnfrepo.EquipmentEntry,
	slots map[string]dnfrepo.ItemStack,
) (context.Context, dnfrepo.Group, *Owner) {
	t.Helper()
	ctx := context.Background()
	repos := dnfrepomemory.NewMemoryGroup()
	if slots == nil {
		slots = map[string]dnfrepo.ItemStack{}
	}
	if entries == nil {
		entries = map[string]dnfrepo.EquipmentEntry{}
	}
	saveTestInventory(t, ctx, repos, dnfrepo.InventoryRecord{CharacterID: "6", Slots: slots})
	saveTestEquipment(t, ctx, repos, dnfrepo.EquipmentRecord{CharacterID: "6", Entries: entries})
	owner, err := newValidatedTestOwner(repos)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repos, owner
}

// wornSlotReassertBagWeapon 造一件"只有身份+耐久证据"的背包武器（现场 GM 发放那一族：
// quality_seed 与 durability 齐备，raw entry 由 op19 源实例重建）。
func wornSlotReassertBagWeapon(itemID int64, itemPath string) dnfrepo.ItemStack {
	return dnfrepo.ItemStack{
		ItemID: itemID,
		Count:  1,
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       itemPath,
			"equipment_type": "[weapon]",
			"durability":     "45",
			"max_durability": "45",
			"quality_seed":   "70238363",
		},
	}
}

// wornSlotReassertBagWeaponWithRaw 造一件"刚从身上脱下来、落在背包里"的武器：背包行带
// 0x77 原始行（正是 unequip 路径写回背包的那种行，见 stackFromEntry）。旧口径下这条
// 声明会走 o.equip 并**成功**把它穿回去 —— 这就是操作者报的那半边。
func wornSlotReassertBagWeaponWithRaw(slot int16, itemID int64, itemPath string) dnfrepo.ItemStack {
	stack := wornSlotReassertBagWeapon(itemID, itemPath)
	stack.RawEntry = duplicateIdentityRawEntry(slot, itemID)
	return stack
}

func wornSlotReassertWornItemID(t *testing.T, ctx context.Context, repos dnfrepo.Group) int64 {
	t.Helper()
	return loadTestEquipment(t, ctx, repos, "6").Entries["12"].ItemID
}

// 判读事件（现场 op19 日志那一行）要能一眼读出"这条包为什么没把武器卸下来"，
// 所以这里把四个字段一起钉住：judgement（哪条规则）、endpoints（两个端点）、
// worn item（客户端点名/权威穿着的那件）、outcome（noop、changed=false）。
func wornSlotReassertAssertNoop(
	t *testing.T,
	result MoveResult,
	wantGate string,
	wantItemID int64,
) {
	t.Helper()
	if result.Mode != "noop" || result.Changed {
		t.Fatalf("result = %+v, want Mode=noop Changed=false (%s)", result, wantGate)
	}
	if result.NoopGate != wantGate {
		t.Fatalf("noopGate = %q, want %q", result.NoopGate, wantGate)
	}
	if result.ItemID != wantItemID {
		t.Fatalf("result.ItemID = %d, want the worn item %d", result.ItemID, wantItemID)
	}
}

// 现场两条复述原文：**每一条窗口闸门都强制关闭**时也必须判 noop、一个字节都不写。
//
// 三个档位分别关掉不同的窗口闸门：
//   - 主开关 =1、窗口标记 false：会话窗口/锚点/每槽/轮次全部拿不到窗口（窗口未开）；
//   - 主开关 =0：整个登录复述族连窗口函数都不会被调用；
//   - 主开关 =0、窗口标记 true：即使有谁把窗口标记写成真，开关关闭时窗口闸门同样惰性。
func TestWornSlotReassertJudgementIsWindowIndependentOnTheLiveRelogFrames(t *testing.T) {
	for name, frameHex := range map[string]string{
		"live :93098 t=13:24:21.46511482Z":  wornSlotReassertLiveRelogHex132421,
		"live :91884 t=13:20:21.733523941Z": wornSlotReassertLiveRelogHex132021,
	} {
		t.Run(name, func(t *testing.T) {
			base := wornSlotReassertLiveCommand(t, frameHex)
			if base.SourceListType != wireListMain || base.SourceSlotIndex != wornSlotReassertRelogBagSlot ||
				base.SourceInstanceValue != 0 || base.MoveCount != 1 ||
				base.DestinationListType != wireListEquipment || base.DestinationSlotIndex != wornSlotReassertWornSlot ||
				base.DestinationInstanceValue != int32(wornSlotReassertBeamswordItemID) {
				t.Fatalf("live frame decoded to %+v, want src=(0,16,0) dst=(3,12,101040493)", base)
			}
			for _, tier := range []struct {
				name       string
				loginWear  string
				windowFlag bool
				wantGate   string
			}{
				{"login switch on, window flag closed", "1", false, WornSlotReassertGate},
				{"login switch off, window flag closed", "0", false, WornSlotReassertGate},
				{"login switch off, window flag open", "0", true, WornSlotReassertGate},
				// 反方向：窗口真的开着时，判读口径一个字都不变 —— 仍然由既有的
				// login_wear_reassert_noop 具名（现场操作者熟悉的那个名字）。
				{"login switch on, window flag open", "1", true, LoginWearReassertNoopGate},
			} {
				t.Run(tier.name, func(t *testing.T) {
					t.Setenv(wornSlotReassertLoginWearSwitch, tier.loginWear)
					ctx, repos, owner := wornSlotReassertFixture(t, nil)
					before := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
					cmd := base
					cmd.LoginWearWindowOpen = tier.windowFlag

					result, err := owner.Move(ctx, cmd)
					if err != nil {
						t.Fatalf("Move error = %v", err)
					}
					wornSlotReassertAssertNoop(t, result, tier.wantGate, wornSlotReassertBeamswordItemID)

					after, still := loadTestEquipment(t, ctx, repos, "6").Entries["12"]
					if !still {
						t.Fatal("the worn weapon left slot 12")
					}
					if after.ItemID != before.ItemID || string(after.RawEntry) != string(before.RawEntry) {
						t.Fatalf("the worn row changed: %+v vs %+v", after, before)
					}
					if slots := loadTestInventory(t, ctx, repos, "6").Slots; len(slots) != 0 {
						t.Fatalf("a noop wrote inventory rows: %+v", slots)
					}
				})
			}
		})
	}
}

// :76912（旧口径下真的卸下了操作者武器的那一条）现在必须是 noop。
func TestWornSlotReassertJudgementAbsorbsThePreGateLiveUnequipFrame(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "0")
	ctx, repos, owner := wornSlotReassertFixture(t, nil)

	cmd := wornSlotReassertLiveCommand(t, wornSlotReassertLivePreGateHex)
	if cmd.SourceSlotIndex != 10 || cmd.DestinationInstanceValue != int32(wornSlotReassertBeamswordItemID) {
		t.Fatalf("pre-gate frame decoded to %+v, want src=(0,10,0) dst=(3,12,101040493)", cmd)
	}
	result, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	wornSlotReassertAssertNoop(t, result, WornSlotReassertGate, wornSlotReassertBeamswordItemID)
	if got := wornSlotReassertWornItemID(t, ctx, repos); got != wornSlotReassertBeamswordItemID {
		t.Fatalf("slot 12 item = %d, want %d still worn (this frame used to really unequip it)", got, wornSlotReassertBeamswordItemID)
	}
	if slots := loadTestInventory(t, ctx, repos, "6").Slots; len(slots) != 0 {
		t.Fatalf("the pre-gate frame still wrote inventory rows: %+v", slots)
	}
}

// :93127（操作者手动把背包 11 格的钝器拖上武器格 12）是**真穿上**的 golden case：
// 源端点写身份、目的端为空 —— 方向判定不许碰它。
func TestWornSlotReassertJudgementKeepsTheLiveGenuineEquip(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "1")
	club := wornSlotReassertBagWeapon(wornSlotReassertClubItemID,
		"equipment/character/swordman/weapon/club/101020668.equ")
	ctx, repos, owner := wornSlotReassertFixtureWith(t, map[string]dnfrepo.EquipmentEntry{},
		map[string]dnfrepo.ItemStack{inventoryKey(wireListMain, wornSlotReassertEquipBagSlot): club})

	cmd := wornSlotReassertLiveCommand(t, wornSlotReassertLiveEquipHex)
	if cmd.SourceListType != wireListMain || cmd.SourceSlotIndex != wornSlotReassertEquipBagSlot ||
		cmd.SourceInstanceValue != int32(wornSlotReassertClubItemID) ||
		cmd.DestinationListType != wireListEquipment || cmd.DestinationSlotIndex != wornSlotReassertWornSlot ||
		cmd.DestinationInstanceValue != 0 {
		t.Fatalf("live equip frame decoded to %+v, want src=(0,11,101020668) dst=(3,12,0)", cmd)
	}
	result, err := owner.Move(ctx, cmd)
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "equip" || !result.Changed || result.NoopGate != "" || result.ItemID != wornSlotReassertClubItemID {
		t.Fatalf("result = %+v, want the live genuine equip (Mode=equip, no noop gate)", result)
	}
	if got := wornSlotReassertWornItemID(t, ctx, repos); got != wornSlotReassertClubItemID {
		t.Fatalf("slot 12 item = %d, want the club %d worn", got, wornSlotReassertClubItemID)
	}
	if _, still := loadTestInventory(t, ctx, repos, "6").Slots[inventoryKey(wireListMain, wornSlotReassertEquipBagSlot)]; still {
		t.Fatal("the club is still in the bag after a successful equip")
	}
}

// 真正的脱下（穿着的那件在**源端装备槽**、目的端背包槽）必须照旧生效 ——
// 这条约束对新判据是绝对的。
func TestWornSlotReassertJudgementKeepsTheGenuineUnequipShape(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "1")
	ctx, repos, owner := wornSlotReassertFixture(t, nil)

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListEquipment,
		SourceSlotIndex:          wornSlotReassertWornSlot,
		SourceInstanceValue:      int32(wornSlotReassertBeamswordItemID),
		MoveCount:                1,
		DestinationListType:      wireListMain,
		DestinationSlotIndex:     wornSlotReassertRelogBagSlot,
		DestinationInstanceValue: 0,
	})
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "unequip" || !result.Changed || result.ItemID != wornSlotReassertBeamswordItemID {
		t.Fatalf("result = %+v, want the genuine unequip of the beamsword", result)
	}
	if _, still := loadTestEquipment(t, ctx, repos, "6").Entries["12"]; still {
		t.Fatal("the genuine unequip left the weapon worn")
	}
	bag, ok := loadTestInventory(t, ctx, repos, "6").Slots[inventoryKey(wireListMain, wornSlotReassertRelogBagSlot)]
	if !ok || bag.ItemID != wornSlotReassertBeamswordItemID {
		t.Fatalf("bag slot 16 = %+v (present=%t), want the unequipped beamsword", bag, ok)
	}
}

// 目的端被**另一件**占着（真换装）不许被本判据吞掉：源端点写了身份、且它自己占着
// 背包格 ⇒ 判据两条前提都不成立。
func TestWornSlotReassertJudgementDoesNotSwallowASwap(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "1")
	club := wornSlotReassertBagWeapon(wornSlotReassertClubItemID,
		"equipment/character/swordman/weapon/club/101020668.equ")
	ctx, repos, owner := wornSlotReassertFixture(t,
		map[string]dnfrepo.ItemStack{inventoryKey(wireListMain, wornSlotReassertEquipBagSlot): club})

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListMain,
		SourceSlotIndex:          wornSlotReassertEquipBagSlot,
		SourceInstanceValue:      int32(wornSlotReassertClubItemID),
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     wornSlotReassertWornSlot,
		DestinationInstanceValue: int32(wornSlotReassertBeamswordItemID),
	})
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "equip_swap" || !result.Changed || result.NoopGate != "" ||
		result.ItemID != wornSlotReassertClubItemID || result.SwappedItemID != wornSlotReassertBeamswordItemID {
		t.Fatalf("result = %+v, want today's equip_swap (the bag weapon replaces the worn one)", result)
	}
	if got := wornSlotReassertWornItemID(t, ctx, repos); got != wornSlotReassertClubItemID {
		t.Fatalf("slot 12 item = %d, want the club", got)
	}
	swapped, ok := loadTestInventory(t, ctx, repos, "6").Slots[inventoryKey(wireListMain, wornSlotReassertEquipBagSlot)]
	if !ok || swapped.ItemID != wornSlotReassertBeamswordItemID {
		t.Fatalf("bag slot 11 = %+v (present=%t), want the swapped-out beamsword", swapped, ok)
	}
}

// 目的端被占、但客户端在它上面点名的**不是**穿着的那件（`owner_test.go` 的 pinned
// stale-destination 形状：客户端的槽位视图过期）⇒ 本判据完全惰性，仍然走反转兜底真卸下、
// 并把落点改到下一个真空槽。这条与
// TestOwnerMoveUnequipUsesNextEmptyMainSlotFromStaleClientDestination 同一形状，
// 在本文件里只作为"新判据不吞真移动"的边界钉子。
func TestWornSlotReassertJudgementLeavesTheStaleClientDestinationAlone(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "1")
	slots := map[string]dnfrepo.ItemStack{}
	for slot := int16(10); slot <= 12; slot++ {
		slots[inventoryKey(wireListMain, slot)] = dnfrepo.ItemStack{ItemID: int64(800 + slot), Count: 1, RawEntry: []byte{byte(slot), 0xA5}}
	}
	const wornItemID int64 = 700
	ctx, repos, owner := wornSlotReassertFixtureWith(t, map[string]dnfrepo.EquipmentEntry{
		"17": {
			SlotIndex: 17,
			ItemID:    wornItemID,
			RawEntry:  []byte{0x11, 0x22},
			Extra:     map[string]string{"source": "equipped"},
		},
	}, slots)

	result, err := owner.Move(ctx, MoveCommand{
		SelectedCharacterID:      6,
		SourceListType:           wireListMain,
		SourceSlotIndex:          10,
		SourceInstanceValue:      0,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     17,
		DestinationInstanceValue: 0x47E0, // 客户端那一格视图过期：槽 17 上其实是 700
	})
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "unequip" || !result.Changed || result.NoopGate != "" || result.ItemID != wornItemID {
		t.Fatalf("result = %+v, want today's unequip of the worn 700", result)
	}
	if got := loadTestInventory(t, ctx, repos, "6").Slots[inventoryKey(wireListMain, 13)]; got.ItemID != wornItemID {
		t.Fatalf("redirected stack = %+v, want the worn item relocated to bag 0:13", got)
	}
}

// 双端点同名（srcIV == dstIV != 0）与厂商同源复述（srcIV > 0、dstIV == 0）两条形状
// 一个字都不许变：前者仍由 duplicate_endpoint_identity 具名、后者仍由 identity_echo 具名。
func TestWornSlotReassertJudgementLeavesTheOtherShapesToTheirOwnGates(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "1")

	t.Run("double endpoint identity", func(t *testing.T) {
		ctx, repos, owner := wornSlotReassertFixture(t, nil)
		result, err := owner.Move(ctx, MoveCommand{
			SelectedCharacterID:      6,
			SourceListType:           wireListMain,
			SourceSlotIndex:          wornSlotReassertEquipBagSlot,
			SourceInstanceValue:      int32(wornSlotReassertBeamswordItemID),
			MoveCount:                1,
			DestinationListType:      wireListEquipment,
			DestinationSlotIndex:     wornSlotReassertWornSlot,
			DestinationInstanceValue: int32(wornSlotReassertBeamswordItemID),
		})
		if err != nil {
			t.Fatalf("Move error = %v", err)
		}
		wornSlotReassertAssertNoop(t, result, equipmentDuplicateEndpointIdentityGate, wornSlotReassertBeamswordItemID)
		if got := wornSlotReassertWornItemID(t, ctx, repos); got != wornSlotReassertBeamswordItemID {
			t.Fatalf("slot 12 item = %d, want %d", got, wornSlotReassertBeamswordItemID)
		}
	})

	t.Run("vendor identity echo", func(t *testing.T) {
		ctx, repos, owner := wornSlotReassertFixture(t, nil)
		result, err := owner.Move(ctx, MoveCommand{
			SelectedCharacterID:      6,
			SourceListType:           wireListMain,
			SourceSlotIndex:          wornSlotReassertRelogBagSlot,
			SourceInstanceValue:      int32(wornSlotReassertBeamswordItemID),
			MoveCount:                1,
			DestinationListType:      wireListEquipment,
			DestinationSlotIndex:     wornSlotReassertWornSlot,
			DestinationInstanceValue: 0,
		})
		if err != nil {
			t.Fatalf("Move error = %v", err)
		}
		wornSlotReassertAssertNoop(t, result, "identity_echo", wornSlotReassertBeamswordItemID)
		if got := wornSlotReassertWornItemID(t, ctx, repos); got != wornSlotReassertBeamswordItemID {
			t.Fatalf("slot 12 item = %d, want %d", got, wornSlotReassertBeamswordItemID)
		}
	})
}

// 回退字面量：显式写 off（及别名）⇒ 逐字节回到今天 —— 同一条 :76912 原文再次真的卸下。
func TestWornSlotReassertJudgementRollbackReproducesTodaysUnequip(t *testing.T) {
	t.Setenv(wornSlotReassertLoginWearSwitch, "0")
	t.Setenv(wornSlotReassertSwitchEnv, "off")
	ctx, repos, owner := wornSlotReassertFixture(t, nil)

	result, err := owner.Move(ctx, wornSlotReassertLiveCommand(t, wornSlotReassertLivePreGateHex))
	if err != nil {
		t.Fatalf("Move error = %v", err)
	}
	if result.Mode != "unequip" || !result.Changed || result.NoopGate != "" || result.ItemID != wornSlotReassertBeamswordItemID {
		t.Fatalf("result = %+v, want today's unequip under the rollback literal", result)
	}
	if _, still := loadTestEquipment(t, ctx, repos, "6").Entries["12"]; still {
		t.Fatal("the rollback literal must leave slot 12 empty (today's behaviour)")
	}
	if _, inBag := loadTestInventory(t, ctx, repos, "6").Slots[inventoryKey(wireListMain, 10)]; !inBag {
		t.Fatal("the rollback literal must land the beamsword in bag 0:10 (today's behaviour)")
	}
}

// 方向 (b)（操作者 2026-09-28 更正的那一半）：玩家**故意把武器脱下来**（槽 12 空着、
// 武器在背包里），随后选角再进角色时客户端照旧发那条登录复述 —— 权威状态必须赢：
// 穿戴集**保持为空**、一个字节都不写，绝不用这条陈述把背包里那件穿回去。
//
// 三个子形状都钉（回退相位同时证明旧口径下它们真的会改写穿戴状态）：
//   - 客户端点名的背包格**正是**武器所在那一格（live 症状：旧口径走到 `if aOccupied`
//     ⇒ o.equip ⇒ 刚脱下来的武器又被穿回去）；
//   - 客户端点名的背包格是空的、武器在背包别处（旧口径：两端都空 ⇒ ErrMoveSlotNotFound）；
//   - 武器压根不在背包里（旧口径同上）。
func TestWornSlotStaleClaimNeverReEquipsWhatThePlayerJustTookOff(t *testing.T) {
	bagWithBeamsword := func(slot int16) map[string]dnfrepo.ItemStack {
		return map[string]dnfrepo.ItemStack{
			inventoryKey(wireListMain, slot): wornSlotReassertBagWeapon(wornSlotReassertBeamswordItemID, wornSlotReassertBeamswordPath),
		}
	}
	// 脱下来落袋的那一件：背包行带 0x77 原始行 ⇒ 旧口径的 o.equip 会成功把它穿回去。
	bagWithUnequippedBeamsword := func(slot int16) map[string]dnfrepo.ItemStack {
		return map[string]dnfrepo.ItemStack{
			inventoryKey(wireListMain, slot): wornSlotReassertBagWeaponWithRaw(slot, wornSlotReassertBeamswordItemID, wornSlotReassertBeamswordPath),
		}
	}
	cases := []struct {
		name      string
		slots     map[string]dnfrepo.ItemStack
		srcSlot   int16
		oldTier   string // 回退档下这条包在旧口径里的结局（= 今天）
		oldReason string
	}{
		{"named bag slot holds the weapon", bagWithUnequippedBeamsword(10), 10, "equip", "旧口径真的把它穿了回去"},
		{"named bag slot is empty, weapon elsewhere in the bag", bagWithBeamsword(20), wornSlotReassertRelogBagSlot, "error", "旧口径两端皆空 ⇒ ErrMoveSlotNotFound"},
		{"weapon is not in the bag at all", nil, wornSlotReassertRelogBagSlot, "error", "旧口径两端皆空 ⇒ ErrMoveSlotNotFound"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, tier := range []struct {
				name      string
				loginWear string
			}{
				{"login wear switch on, window flag closed", "1"},
				{"login wear family switched off", "0"},
			} {
				t.Run(tier.name, func(t *testing.T) {
					t.Setenv(wornSlotReassertLoginWearSwitch, tier.loginWear)
					t.Setenv(wornSlotReassertSwitchEnv, "")
					ctx, repos, owner := wornSlotReassertFixtureWith(t, map[string]dnfrepo.EquipmentEntry{}, testCase.slots)
					before := loadTestInventory(t, ctx, repos, "6").Slots

					result, err := owner.Move(ctx, MoveCommand{
						SelectedCharacterID:      6,
						SourceListType:           wireListMain,
						SourceSlotIndex:          testCase.srcSlot,
						SourceInstanceValue:      0,
						MoveCount:                1,
						DestinationListType:      wireListEquipment,
						DestinationSlotIndex:     wornSlotReassertWornSlot,
						DestinationInstanceValue: int32(wornSlotReassertBeamswordItemID),
					})
					if err != nil {
						t.Fatalf("Move error = %v, want a noop (the claim must never mutate)", err)
					}
					wornSlotReassertAssertNoop(t, result, WornSlotStaleClaimGate, wornSlotReassertBeamswordItemID)
					if entries := loadTestEquipment(t, ctx, repos, "6").Entries; len(entries) != 0 {
						t.Fatalf("the stale claim wrote the worn set: %+v", entries)
					}
					after := loadTestInventory(t, ctx, repos, "6").Slots
					if len(after) != len(before) {
						t.Fatalf("the stale claim wrote inventory rows: %+v (before %+v)", after, before)
					}
					for key, stack := range before {
						if got := after[key]; got.ItemID != stack.ItemID || got.Count != stack.Count {
							t.Fatalf("inventory row %s changed: %+v vs %+v", key, got, stack)
						}
					}
				})
			}

			// 证伪相位：把方向判定写回回退字面量 ⇒ 逐字节回到今天
			// （复现操作者报的那半边："脱下武器后选角，武器又被穿上了"）。
			t.Run("rollback reproduces today/"+testCase.oldTier, func(t *testing.T) {
				t.Setenv(wornSlotReassertLoginWearSwitch, "0")
				t.Setenv(wornSlotReassertSwitchEnv, "off")
				ctx, repos, owner := wornSlotReassertFixtureWith(t, map[string]dnfrepo.EquipmentEntry{}, testCase.slots)

				result, err := owner.Move(ctx, MoveCommand{
					SelectedCharacterID:      6,
					SourceListType:           wireListMain,
					SourceSlotIndex:          testCase.srcSlot,
					SourceInstanceValue:      0,
					MoveCount:                1,
					DestinationListType:      wireListEquipment,
					DestinationSlotIndex:     wornSlotReassertWornSlot,
					DestinationInstanceValue: int32(wornSlotReassertBeamswordItemID),
				})
				switch testCase.oldTier {
				case "equip":
					if err != nil {
						t.Fatalf("rollback Move error = %v", err)
					}
					if result.Mode != "equip" || !result.Changed {
						t.Fatalf("rollback result = %+v, want today's re-equip (%s)", result, testCase.oldReason)
					}
					if got := wornSlotReassertWornItemID(t, ctx, repos); got != wornSlotReassertBeamswordItemID {
						t.Fatalf("rollback slot 12 item = %d, want today's re-equip of %d", got, wornSlotReassertBeamswordItemID)
					}
				default:
					if !errors.Is(err, ErrMoveSlotNotFound) {
						t.Fatalf("rollback error = %v, want %v (%s)", err, ErrMoveSlotNotFound, testCase.oldReason)
					}
					if entries := loadTestEquipment(t, ctx, repos, "6").Entries; len(entries) != 0 {
						t.Fatalf("rollback wrote the worn set: %+v", entries)
					}
				}
			})
		})
	}
}

// 判据边界表（两侧都钉）：形状 = "源端 list0/list1 且实例 0 + 目的端 list3 且实例 != 0"；
// 是否判 noop 由**权威状态**决定，具名闸门随之二选一。任何形变都必须落回旧口径。
func TestWornSlotReassertJudgementBoundary(t *testing.T) {
	t.Setenv(wornSlotReassertSwitchEnv, "")
	worn := dnfrepo.EquipmentEntry{SlotIndex: wornSlotReassertWornSlot, ItemID: wornSlotReassertBeamswordItemID}
	live := MoveCommand{
		SourceListType:           wireListMain,
		SourceSlotIndex:          wornSlotReassertRelogBagSlot,
		SourceInstanceValue:      0,
		MoveCount:                1,
		DestinationListType:      wireListEquipment,
		DestinationSlotIndex:     wornSlotReassertWornSlot,
		DestinationInstanceValue: int32(wornSlotReassertBeamswordItemID),
	}
	mutate := func(f func(*MoveCommand)) MoveCommand {
		cmd := live
		f(&cmd)
		return cmd
	}
	for name, testCase := range map[string]struct {
		cmd        MoveCommand
		equipped   dnfrepo.EquipmentEntry
		occupied   bool
		wantNoop   bool
		wantGate   string
		whyChanged string
	}{
		"live re-assert (that slot holds the named item)": {live, worn, true, true, WornSlotReassertGate, ""},
		"that slot is empty (stale claim)":                {live, dnfrepo.EquipmentEntry{SlotIndex: wornSlotReassertWornSlot}, false, true, WornSlotStaleClaimGate, "玩家刚把那件脱下来：声明与权威状态矛盾，也不许穿回去"},
		"source endpoint occupied":                        {live, worn, true, true, WornSlotReassertGate, "源端有没有东西都不改变'这条包是声明'这件事"},
		"source declares an identity": {mutate(func(c *MoveCommand) { c.SourceInstanceValue = int32(wornSlotReassertBeamswordItemID) }),
			worn, true, false, "", "身份写在源端 = 真搬运（identity_echo / equip）那一族"},
		"destination names another item": {mutate(func(c *MoveCommand) { c.DestinationInstanceValue = 3037 }),
			worn, true, false, "", "点名的不是穿着的那件（stale view）⇒ 仍走反转端点兜底"},
		"destination names nothing": {mutate(func(c *MoveCommand) { c.DestinationInstanceValue = 0 }),
			worn, true, false, "", "两个端点都没有身份"},
		"occupied but worn item id missing": {live, dnfrepo.EquipmentEntry{SlotIndex: wornSlotReassertWornSlot}, true, false, "", "权威行没有 item id，无法确认声明"},
		"source is the worn container": {mutate(func(c *MoveCommand) { c.SourceListType = wireListEquipment }),
			worn, true, false, "", "两端都是穿戴端点=真的脱下/互换"},
		"destination is not the worn list": {mutate(func(c *MoveCommand) { c.DestinationListType = wireListMain }),
			worn, true, false, "", "同容器搬运"},
		"source is the cargo list": {mutate(func(c *MoveCommand) { c.SourceListType = wireListPersonalCargo }),
			worn, true, false, "", "仓库不是穿戴来源"},
		"batch move count": {mutate(func(c *MoveCommand) { c.MoveCount = 2 }),
			worn, true, false, "", "op19 搬运件数恒为 1"},
		"avatar list re-assert": {mutate(func(c *MoveCommand) {
			c.SourceListType = wireListAvatar
			c.SourceSlotIndex = 8
			c.DestinationSlotIndex = 8
		}), dnfrepo.EquipmentEntry{SlotIndex: 8, ItemID: wornSlotReassertBeamswordItemID}, true, true, WornSlotReassertGate, "装扮栏同族形状（list1 → list3）"},
	} {
		t.Run(name, func(t *testing.T) {
			gotNoop, gotGate := equipmentWornSlotReassert(testCase.cmd, testCase.equipped, testCase.occupied)
			if gotNoop != testCase.wantNoop || gotGate != testCase.wantGate {
				t.Fatalf("judgement = (%t,%q), want (%t,%q) (%s)",
					gotNoop, gotGate, testCase.wantNoop, testCase.wantGate, testCase.whyChanged)
			}
		})
	}

	// 档位字面量的两端：默认（含未设置/写错）都是修复档，只有显式回退字面量才惰性。
	for _, raw := range []string{"", "on", "1", "true", "yes", "typo", " "} {
		t.Setenv(wornSlotReassertSwitchEnv, raw)
		if !equipmentWornSlotReassertEnabled() {
			t.Fatalf("%s=%q must be the fixed tier", wornSlotReassertSwitchEnv, raw)
		}
	}
	for _, raw := range []string{"off", "0", "false", "no", "legacy", "disable", "disabled", "none", "OFF ", "Off"} {
		t.Setenv(wornSlotReassertSwitchEnv, raw)
		if equipmentWornSlotReassertEnabled() {
			t.Fatalf("%s=%q must be the rollback tier", wornSlotReassertSwitchEnv, raw)
		}
		if noop, gate := equipmentWornSlotReassert(live, worn, true); noop || gate != "" {
			t.Fatalf("%s=%q must make the judgement inert (today's behaviour)", wornSlotReassertSwitchEnv, raw)
		}
		if noop, gate := equipmentWornSlotReassert(live, dnfrepo.EquipmentEntry{}, false); noop || gate != "" {
			t.Fatalf("%s=%q must make the stale-claim half inert too", wornSlotReassertSwitchEnv, raw)
		}
	}
}

// 事件名与判据名必须一致：dnfbridge 的那行 `EVENT kind=game-equip-direction-judgement`
// 用 domain_gate= 字段引用它，`equipMove=noop noopGate=` 那一行也用它。
func TestWornSlotReassertGateNamesStayGreppable(t *testing.T) {
	if WornSlotReassertGate != "worn_slot_reassert" {
		t.Fatalf("WornSlotReassertGate = %q, want the shipped literal", WornSlotReassertGate)
	}
	if WornSlotStaleClaimGate != "worn_slot_stale_claim" {
		t.Fatalf("WornSlotStaleClaimGate = %q, want the shipped literal", WornSlotStaleClaimGate)
	}
	if wornSlotReassertSwitchEnv != "DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT" {
		t.Fatalf("switch env = %q, want the shipped literal", wornSlotReassertSwitchEnv)
	}
	for _, name := range []string{WornSlotReassertGate, WornSlotStaleClaimGate} {
		switch name {
		case LoginWearReassertNoopGate, equipmentDuplicateEndpointIdentityGate, equipmentLoginWearUndeclaredReversalGate, "identity_echo":
			t.Fatalf("the judgement name %q collides with a sibling gate (field reading counts noopGate=)", name)
		}
	}
}
