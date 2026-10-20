package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfinventory "longheng.io/server/internal/modules/dnf/inventory"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住 2026-09-28 第三轮的范围收口（`equip_login_wear_reassert_anchor.go`）：
// **窗口之外**到达的同族 op19 不许再走进 `equip/owner.go:1046` 的反转端点兜底把
// 玩家身上的东西真卸下来。
//
// 现场（`_dist/weapon-unequip-stutter-round4.md` §2.2/§2.3/§2.6，上线后
// 16:56:15–17:11:34 CST）：
//
//	角色 6（test78）槽 17 护甲 100270515，请求 09:09:20.143953846Z
//	  hex 0113002900000082712ab54b00000a000000000001000000031100b301fa0500000000ffffffff0000
//	  src=(0,10,IV=0) count=1 dst=(3,17,IV=100270515)
//	  ⇒ 09:09:20.875025325Z equipMove=unequip requestA=(0,10) requestB=(3,17) changed=true
//	  ⇒ DB：槽 17 至今是空的，那件装备现在躺在背包 0:10。
//	  时距：该会话窗口开启 09:08:11.816 / 09:08:34.410，即 **+46 s 窗外**。
//
//	角色 1 槽 12 武器 101010856，请求 09:01:48.891386239Z
//	  hex 011300290000004c2456f968000015000000000001000000030c00a84d0506…
//	  src=(0,21,IV=0) count=1 dst=(3,12,IV=101010856)
//	  ⇒ 09:01:49.314243779Z equipMove=unequip changed=true（1.05 s 后客户端又装回）。
//
//	同一角色 6 槽 12 武器 101040493 在**窗内**（+2.0 s / +1.5 s）到达时被判
//	  `equipMove=noop noopGate=login_wear_reassert_noop changed=false` —— 说明闸门判据没坏，
//	  坏的是范围。（这两条是"默认档必须继续保住"的对照。）
//
// 现场常量一个都不许改（item id / 槽位 / 背包端点都来自 packet_log 与 DB 复核）。
const (
	loginWearAnchorWeaponItemID int64 = 101040493 // 圣耀救赎光剑（槽 12）
	loginWearAnchorWeaponSlot   int16 = 12

	loginWearAnchorArmorItemID16 int64 = 100120544 // 槽 16
	loginWearAnchorArmorSlot16   int16 = 16
	loginWearAnchorArmorItemID17 int64 = 100270515 // 槽 17（09:09:20.875 被真卸的那件）
	loginWearAnchorArmorSlot17   int16 = 17
	loginWearAnchorArmorItemID18 int64 = 100220519 // 槽 18
	loginWearAnchorArmorSlot18   int16 = 18

	// 现场该族包用的背包端点：槽 10（空）。
	loginWearAnchorBagSlot int16 = 10

	// loginWearAnchorPlayerDropSlot 是"玩家自己把装备拖出来"时客户端点名的背包端点。
	// 依据：pinned 的真脱下样本用的就是背包槽 16（equipment_endpoint_echo_test.go:133-159
	// 的 reloginBeamswordBagSlot），而线上四条复述串点名的都是客户端自己请求里那一格
	// （0:9 / 0:10 / 0:11 / 0:12 / 0:13）。两者不同正是本档保留玩家出口的依据。
	loginWearAnchorPlayerDropSlot int16 = 16

	// 角色 1 的武器与它那条包用的背包端点（槽 21）。
	loginWearAnchorCharOneWeaponItemID int64 = 101010856
	loginWearAnchorCharOneBagSlot      int16 = 21

	// 现场时距：该族包落在窗口外 46 s（角色 6）与 68 s（角色 1）。
	loginWearAnchorLateDelay = 46 * time.Second
)

// loginWearAnchorLiveArmor 是角色 6 上线后那一段会话的穿戴表（round4 §2.2 的 DB 复核原文：
// 槽 12 = 101040493、槽 16 = 100120544、槽 18 = 100220519；槽 17 当时还在，是被这一族包卸掉的）。
var loginWearAnchorLiveArmor = map[int16]int64{
	loginWearAnchorWeaponSlot:  loginWearAnchorWeaponItemID,
	loginWearAnchorArmorSlot16: loginWearAnchorArmorItemID16,
	loginWearAnchorArmorSlot17: loginWearAnchorArmorItemID17,
	loginWearAnchorArmorSlot18: loginWearAnchorArmorItemID18,
}

// loginWearAnchorFixture 是"装备穿在身上、背包为空"的最小连接（与
// equip_login_wear_reassert_test.go 的 fixture 同构，但角色 id 与穿戴表可参数化，
// 好让本文件直接用现场角色 6 / 1 与现场 item id）。
type loginWearAnchorFixture struct {
	group        dnfrepo.Group
	service      *Service
	session      *gameSession
	buffer       *bufferConn
	characterKey string
	characterID  uint16
}

func loginWearAnchorRawEntry(slot int16, itemID int64, seed uint32, durability uint16) []byte {
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(slot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(itemID))
	binary.LittleEndian.PutUint32(raw[6:10], seed)
	raw[0x0A] = 13
	binary.LittleEndian.PutUint16(raw[0x0B:0x0D], durability)
	binary.LittleEndian.PutUint32(raw[0x0E:0x12], uint32(itemID))
	return raw
}

func newLoginWearAnchorFixture(t *testing.T, characterKey string, characterID uint16, worn map[int16]int64) *loginWearAnchorFixture {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	if err := group.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:10011", Metadata: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: characterKey, AccountID: "dnf:10011", Name: "Actor" + characterKey, Job: "0", Level: 90,
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: characterKey,
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]dnfrepo.EquipmentEntry, len(worn))
	for slot, itemID := range worn {
		// 现场只给出了 item id、槽位与（武器那一件的）PVF 路径；护甲行的 pvf_path 是
		// **占位**（只为满足 owner 对 `equipment/` 前缀的检查），不为任何判据提供依据。
		pvfPath := "equipment/character/swordman/armor/" + strconv.FormatInt(itemID, 10) + ".equ"
		extra := map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       pvfPath,
			"equipped_slot":  strconv.Itoa(int(slot)),
			"equipment_slot": strconv.Itoa(int(slot)),
			"durability":     "48",
			"max_durability": "48",
			"quality_seed":   "203004656",
		}
		if itemID == loginWearAnchorWeaponItemID {
			// 报告 §1.3 的原文（DB 复核）：圣耀救赎光剑。
			extra["pvf_path"] = "equipment/character/swordman/weapon/beamsword/101040493.equ"
			extra["equipment_type"] = "[weapon]"
		}
		entries[strconv.Itoa(int(slot))] = dnfrepo.EquipmentEntry{
			SlotIndex: slot,
			ItemID:    itemID,
			RawEntry:  loginWearAnchorRawEntry(slot, itemID, 203004656, 48),
			Extra:     extra,
		}
	}
	if err := group.Equipment.Save(ctx, dnfrepo.EquipmentRecord{CharacterID: characterKey, Entries: entries}); err != nil {
		t.Fatal(err)
	}

	buffer := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "dnf:10011",
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return group, true },
	}
	session := &gameSession{
		conn:                            buffer,
		connID:                          "login-wear-anchor-" + characterKey,
		accountID:                       "dnf:10011",
		channel:                         channelcatalog.Channel{ID: 252},
		selectedCharacterID:             characterID,
		initialTownRouteCharacterID:     characterID,
		initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
		townSceneReadyCharacterID:       characterID,
		connectionTownActorOwnerChannel: byte(characterID),
		townActorOwnerChannel:           byte(characterID),
	}
	return &loginWearAnchorFixture{
		group:        group,
		service:      service,
		session:      session,
		buffer:       buffer,
		characterKey: characterKey,
		characterID:  characterID,
	}
}

// setDefaultTier 显式清空本轮三个新开关（空值 = 未设置 = 默认修复档），
// 这样用例断言的"默认档"与操作者机器上的环境变量无关。
func setDefaultTier(t *testing.T) {
	t.Helper()
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(loginWearReassertAnchorEnv, "")
	t.Setenv(loginWearReassertPerSlotEnv, "")
	t.Setenv(loginWearReassertRepeatEnv, "")
	t.Setenv(loginWearReassertWindowModeEnv, "")
	t.Setenv(loginWearReassertWindowMillisEnv, "")
	t.Setenv(equipLoginWearReassertNoopRefreshEnv, "")
}

// setRollbackTier 把三个新开关显式写到回退字面量（= 逐字节回到今天）。
//
// 2026-09-28 第五轮：方向判定本身（equip/worn_slot_reassert_gate.go）默认开启，
// 它与窗口无关 —— 该族包在**任何**窗口档位下都会被它判 noop。因此"逐字节回到今天"
// 现在必须把它一起写回回退字面量，否则这些回退档用例复现的就不再是今天的现场 bug
// （:76912 `equipMove=unequip changed=true`）。其余三个开关的语义一个字都没变。
func setRollbackTier(t *testing.T) {
	t.Helper()
	setDefaultTier(t)
	t.Setenv(loginWearReassertAnchorEnv, loginWearReassertAnchorSceneReady)
	t.Setenv(loginWearReassertPerSlotEnv, loginWearReassertPerSlotOff)
	t.Setenv(loginWearReassertRepeatEnv, loginWearReassertRepeatGate)
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
}

// openWindowAgo 复刻现场：会话在 delay 之前收到过场景就绪 ack（窗口由
// openEquipLoginWearWindow 打开），因此这条 op19 到达时窗口已经过期。
func (f *loginWearAnchorFixture) openWindowAgo(t *testing.T, delay time.Duration) {
	t.Helper()
	f.service.openEquipLoginWearWindow(f.session, "test")
	f.session.equipLoginWearWindowOpenedAt = f.service.gameplayNow().Add(-delay)
}

func (f *loginWearAnchorFixture) runOp19(t *testing.T, body []byte) loginWearRound {
	t.Helper()
	f.buffer.write.Reset()
	windowOpen := f.service.equipLoginWearFamilyWindowOpenForOp19(f.session, body)
	result, err := dnfinventory.NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                body,
		AccountID:           "dnf:10011",
		SelectedCharacterID: f.characterID,
		Repositories:        f.group,
		EquipmentPlacement:  func(context.Context, alignedcmd.EquipmentPlacementRequest) error { return nil },
		// 与 game_aligned.go 同一个填法。
		LoginWearWindowOpen:          windowOpen,
		LoginWearReassertNoopRefresh: f.service.equipLoginWearReassertNoopRefreshRequested(),
	})
	if err != nil {
		t.Fatalf("handle op19 error = %v", err)
	}
	if !result.Handled || !result.ResponseAllowed || len(result.UpperResponses) != 1 {
		t.Fatalf("op19 result = %+v", result)
	}
	if err := f.service.sendAlignedUpperResponses(f.session, result); err != nil {
		t.Fatalf("sendAlignedUpperResponses error = %v", err)
	}
	round := loginWearRound{windowOpen: windowOpen, result: result, stream: append([]byte(nil), f.buffer.write.Bytes()...)}
	rest := round.stream
	for len(rest) > 0 {
		packet, remaining := splitLongHengGameServerUpperPacket(t, rest)
		rest = remaining
		round.msgIDs = append(round.msgIDs, packet.Header.MsgID)
		round.bodies = append(round.bodies, loginWearPacket{
			msgID:          packet.Header.MsgID,
			classification: packet.Header.Classification,
			body:           append([]byte(nil), packet.Body...),
		})
	}
	return round
}

func (f *loginWearAnchorFixture) wornItemID(t *testing.T, slot int16) int64 {
	t.Helper()
	record, ok, err := f.group.Equipment.Load(context.Background(), f.characterKey)
	if err != nil || !ok {
		t.Fatalf("equipment load ok=%t err=%v", ok, err)
	}
	return record.Entries[strconv.Itoa(int(slot))].ItemID
}

func (f *loginWearAnchorFixture) bagSlotHolding(t *testing.T, itemID int64) (string, bool) {
	t.Helper()
	record, ok, err := f.group.Inventory.Load(context.Background(), f.characterKey)
	if err != nil || !ok {
		t.Fatalf("inventory load ok=%t err=%v", ok, err)
	}
	for key, stack := range record.Slots {
		if stack.ItemID == itemID {
			return key, true
		}
	}
	return "", false
}

// 现场 A 形状（背包端点实例 0 + 装备端点实例 = 槽上那件 item id），字段全部按现场原文。
func loginWearAnchorFamilyBody(bagList byte, bagSlot int16, wornSlot int16, itemID int64) []byte {
	return loginWearBridgeBody(bagList, bagSlot, 0, currentSocketListEquipment, wornSlot, int32(itemID))
}

// 现场角色 6 槽 17 那条包（09:09:20.143953846Z，seq=305）的等价构造。
func loginWearAnchorLiveSlot17Body() []byte {
	return loginWearAnchorFamilyBody(0, loginWearAnchorBagSlot, loginWearAnchorArmorSlot17, loginWearAnchorArmorItemID17)
}

// 现场角色 1 槽 12 那条包（09:01:48.891386239Z，seq=331）的等价构造。
func loginWearAnchorLiveCharOneBody() []byte {
	return loginWearAnchorFamilyBody(0, loginWearAnchorCharOneBagSlot, loginWearAnchorWeaponSlot, loginWearAnchorCharOneWeaponItemID)
}

// TestEquipLoginWearLateFamilyReassertKeepsTheWornPiece 是**现场回归本身**：
// 角色 6 槽 17 那条 +46 s 的同族包，在默认档下必须判 noop、装备行一个字节不动。
func TestEquipLoginWearLateFamilyReassertKeepsTheWornPiece(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	round := fixture.runOp19(t, loginWearAnchorLiveSlot17Body())
	if !round.windowOpen {
		t.Fatal("the late family packet fell out of the gate: game_aligned.go would hand it to " +
			"equip/owner.go:1046, which is exactly the live 09:09:20.875025325Z unequip")
	}
	if !strings.Contains(round.result.Reason, "equipMove=noop") ||
		!strings.Contains(round.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("reason = %q, want a login_wear_reassert_noop", round.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("slot 17 item = %d, want %d still worn", id, loginWearAnchorArmorItemID17)
	}
	if slot, inBag := fixture.bagSlotHolding(t, loginWearAnchorArmorItemID17); inBag {
		t.Fatalf("the armour piece left the worn slot and landed in bag %s (the live bug)", slot)
	}
	// 槽 17（护甲，槽位 ≥14 = 无外观）在默认档下**只有 ACK**：这是既有的外观槽闸门
	// （DNFBRIDGE_EQUIP_APPEARANCE_SLOT_GATE，见 inventory/handler.go:1111 的
	// `result.Changed || noopLoginWearReassert` 之后的 appearance 追加）决定的，不是本轮引入的。
	// 后果（必须知情）：吸收一条**不可见槽**的晚到复述，客户端本地视图这一轮拿不到权威刷新
	// —— 它靠下次开背包（现场每次 op19 之前都有 type=217）自己对齐；若实测发现客户端
	// 仍反复重发，可用既有开关 DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH=full 让该 noop
	// 重放权威容器（不需要重编译）。武器槽（12）那一路见下面的子用例，它照旧带 mode0。
	if !equalMsgIDs(round.msgIDs, []uint16{uint16(dnfenum.CmdPacketMoveItemspace)}) {
		t.Fatalf("armour-slot late absorption packets = %v, want the ACK only (invisible slot has no mode0)", round.msgIDs)
	}
}

// TestEquipLoginWearLateFamilyReassertRollbackReproducesTheLiveUnequip 是本条修复的
// **证伪口径**：把两个新开关写回回退字面量（scene_ready + off）后，同一条包必须
// 逐字节复现今天的现场 bug —— 真落库卸下、装备进背包。
func TestEquipLoginWearLateFamilyReassertRollbackReproducesTheLiveUnequip(t *testing.T) {
	setRollbackTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	round := fixture.runOp19(t, loginWearAnchorLiveSlot17Body())
	if round.windowOpen {
		t.Fatal("the rollback tier must reproduce today: the late packet is outside the 15 s window")
	}
	if !strings.Contains(round.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback reason = %q, want the live unequip", round.result.Reason)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("rollback tier must reproduce the live bug: slot 17 emptied, got item %d", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorArmorItemID17); !inBag {
		t.Fatal("rollback tier must reproduce the live bug: the piece is in the bag (live: 0:10)")
	}
}

// TestEquipLoginWearLateFamilyReassertCoversTheLiveWeaponSlots 钉住操作者最初报的那一条：
// **武器槽**在窗口之外被真卸。角色 1（17:01:48.891，101010856）与角色 6
// （窗内 +1.5 s / +2.0 s 两次 101040493）两侧都要成立。
func TestEquipLoginWearLateFamilyReassertCoversTheLiveWeaponSlots(t *testing.T) {
	setDefaultTier(t)

	t.Run("character 1 weapon slot 12 at +68 s (live 09:01:49.314243779Z)", func(t *testing.T) {
		fixture := newLoginWearAnchorFixture(t, "1", 1, map[int16]int64{
			loginWearAnchorWeaponSlot: loginWearAnchorCharOneWeaponItemID,
		})
		fixture.openWindowAgo(t, 68*time.Second)
		round := fixture.runOp19(t, loginWearAnchorLiveCharOneBody())
		if !round.windowOpen || !strings.Contains(round.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("character 1 late weapon packet = %q (gate=%t)", round.result.Reason, round.windowOpen)
		}
		if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorCharOneWeaponItemID {
			t.Fatalf("character 1 weapon left slot 12: item = %d", id)
		}
		// 可见槽（武器 12）的晚到吸收必须仍然带权威 mode0 刷新：客户端本地视图靠它收敛。
		if !hasMsgID(round.msgIDs, uint16(dnfenum.CmdPacketSetUDPIPPort)) {
			t.Fatalf("late absorption on the visible weapon slot must still emit mode0, got %v", round.msgIDs)
		}
	})

	t.Run("character 6 weapon slot 12 inside the window (live noop at +1.5 s)", func(t *testing.T) {
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		fixture.service.openEquipLoginWearWindow(fixture.session, "test")
		fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-1500 * time.Millisecond)
		round := fixture.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorBagSlot, loginWearAnchorWeaponSlot, loginWearAnchorWeaponItemID))
		if !round.windowOpen || !strings.Contains(round.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("in-window weapon packet = %q (gate=%t): the fix must not lose the case the "+
				"15 s window already covered", round.result.Reason, round.windowOpen)
		}
		if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
			t.Fatalf("weapon left slot 12 in the window: item = %d", id)
		}
	})
}

// TestEquipLoginWearPerSlotGivesEachPieceItsOwnReassertWindow 钉住每槽档的正半边：
// 同一会话两件装备（槽 17 与槽 12）各拿到一次复述处理窗口，两件都不许动。
func TestEquipLoginWearPerSlotGivesEachPieceItsOwnReassertWindow(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	armour := fixture.runOp19(t, loginWearAnchorLiveSlot17Body())
	weapon := fixture.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorBagSlot, loginWearAnchorWeaponSlot, loginWearAnchorWeaponItemID))
	for name, round := range map[string]loginWearRound{"slot 17": armour, "slot 12": weapon} {
		if !round.windowOpen || !strings.Contains(round.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("%s = %q (gate=%t), want one absorbed re-assert per slot", name, round.result.Reason, round.windowOpen)
		}
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("slot 17 item = %d, want %d still worn", id, loginWearAnchorArmorItemID17)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
		t.Fatalf("slot 12 item = %d, want %d still worn", id, loginWearAnchorWeaponItemID)
	}
}

// TestEquipLoginWearPerSlotSecondAttemptDoesNotMutate 钉住每槽档的重复侧：
// 同一槽的第二次同形包（客户端同一串复述里的重发）仍然吸收、且**不改库**，
// 连下行 ACK 都与第一条逐字节相同。
func TestEquipLoginWearPerSlotSecondAttemptDoesNotMutate(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	body := loginWearAnchorLiveSlot17Body()
	first := fixture.runOp19(t, body)
	second := fixture.runOp19(t, body)
	for round, got := range map[string]loginWearRound{"first": first, "second": second} {
		if !got.windowOpen || !strings.Contains(got.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("%s attempt = %q (gate=%t), want it absorbed inside the slot's re-assert window",
				round, got.result.Reason, got.windowOpen)
		}
	}
	if got, want := firstPacketBody(t, second.stream), firstPacketBody(t, first.stream); !bytes.Equal(got, want) {
		t.Fatalf("second attempt ACK = %x, want the first attempt's bytes %x", got, want)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("the second attempt mutated the equipment row: slot 17 item = %d", id)
	}
	if slot, inBag := fixture.bagSlotHolding(t, loginWearAnchorArmorItemID17); inBag {
		t.Fatalf("the second attempt moved the piece into bag %s", slot)
	}
}

// TestEquipLoginWearPerSlotReleasesTheSlotAfterItsWindow 钉住每槽档的出口（本条是
// PER_SLOT 的**证伪靶**）：该槽的复述处理窗口过去之后，**字段不同的新陈述**必须放行到
// 域层 —— 每槽档不许把该槽之后的所有陈述都吞掉，"每槽只吃一次"是这条底的保证。
//
// 2026-09-28 第五轮：放行之后的含义由**方向判定本身**决定。A 形状（源端空背包格 +
// 装备端点点名那一格穿着的那件）现在一律判 noop（worn_slot_reassert_gate.go），
// 玩家真正的脱下形状是"穿着的那件在源端装备槽"（见 equip 包
// TestWornSlotReassertJudgementKeepsTheGenuineUnequipShape 与 dnfbridge 的
// TestEquipLoginWearRoundGenuineUnequipStillUnequips）。本用例因此钉"放行 + 由方向
// 判定具名"，并保留一个回退相位证明旧口径下的真卸仍可复现。
//
// 2026-09-28 18:32 件上线后补的口径（**同一条陈述的重复**不再受时钟限制，见
// equip_login_wear_reassert_repeat.go）：这里的放行样本必须与已吸收那条**背包端点不同**，
// 因为同一格背包端点上的逐字段重复正是操作者报的那条 bug（现场 10:37:20.896 / 10:38:29.707
// 与 10:37:14.350 逐字节同形）。同一条重复的两种档位见下一个用例。
func TestEquipLoginWearPerSlotReleasesTheSlotAfterItsWindow(t *testing.T) {
	setDefaultTier(t)
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	fixture.openWindowAgo(t, loginWearAnchorLateDelay)

	body := loginWearAnchorLiveSlot17Body()
	absorbed := fixture.runOp19(t, body)
	if !absorbed.windowOpen {
		t.Fatal("the slot's first re-assert must be absorbed")
	}
	// 模拟"客户端的复述串已经结束"：该槽的窗口起点被推到窗口时长之外。
	// 先用生产写入函数建账（这样即便每槽档被中性化，用例也给出可读断言而不是 nil map panic）。
	statement, ok := loginWearReassertStatementOf(body)
	if !ok {
		t.Fatal("the live slot-17 body must decode")
	}
	fixture.session.noteLoginWearReassertSlotAbsorbed(loginWearAnchorArmorSlot17, time.Now(), statement)
	fixture.session.equipLoginWearReassertSlots[loginWearAnchorArmorSlot17] = loginWearReassertSlotEpoch{
		absorbed:          true,
		absorbedAt:        time.Now().Add(-time.Duration(loginWearReassertEchoIntervalMillis+1000) * time.Millisecond),
		statement:         statement,
		statementRecorded: true,
	}

	// 该槽复述窗口之外的另一条陈述（背包端点换成 0:16）必须放行到域层。放行之后由
	// **方向判定本身**决定它的含义：客户端在装备端点点名的正是那一格穿着的那件、源端
	// 又是空的 ⇒ 那是复述穿戴态 ⇒ noop（worn_slot_reassert），与窗口无关。
	// 2026-09-28 第五轮：这条用例原来断言"放行 ⇒ 真卸下"；那是旧口径。每槽档在这里
	// 要证明的东西（**不能把该槽之后的所有陈述都吞掉**）一个字都没变，见下面的回退相位。
	released := fixture.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorPlayerDropSlot, loginWearAnchorArmorSlot17, loginWearAnchorArmorItemID17))
	if released.windowOpen {
		t.Fatalf("the slot was absorbed twice: a later statement on the same slot could never reach "+
			"the owner (per_slot=%s)", loginWearReassertPerSlotOn)
	}
	if !strings.Contains(released.result.Reason, "noopGate="+dnfequip.WornSlotReassertGate) {
		t.Fatalf("released reason = %q, want the window-independent %s judgement to own it",
			released.result.Reason, dnfequip.WornSlotReassertGate)
	}
	if id := fixture.wornItemID(t, loginWearAnchorArmorSlot17); id != loginWearAnchorArmorItemID17 {
		t.Fatalf("slot 17 item = %d, want %d still worn", id, loginWearAnchorArmorItemID17)
	}
	// 回退相位（同一条放行样本 + 方向判定写回回退字面量）：旧口径真的会把它卸下来 ——
	// 这证明上面那条放行断言的分量，也给"今天的现场 bug 仍可复现"留下证伪口径。
	rollback := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	rollback.openWindowAgo(t, loginWearAnchorLateDelay)
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	if got := rollback.runOp19(t, loginWearAnchorLiveSlot17Body()); !got.windowOpen {
		t.Fatal("the rollback phase must first absorb the slot's own re-assert")
	}
	rollback.session.noteLoginWearReassertSlotAbsorbed(loginWearAnchorArmorSlot17, time.Now(), statement)
	rollback.session.equipLoginWearReassertSlots[loginWearAnchorArmorSlot17] = loginWearReassertSlotEpoch{
		absorbed:          true,
		absorbedAt:        time.Now().Add(-time.Duration(loginWearReassertEchoIntervalMillis+1000) * time.Millisecond),
		statement:         statement,
		statementRecorded: true,
	}
	today := rollback.runOp19(t, loginWearAnchorFamilyBody(0, loginWearAnchorPlayerDropSlot, loginWearAnchorArmorSlot17, loginWearAnchorArmorItemID17))
	if today.windowOpen || !strings.Contains(today.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback phase = %q (window=%t), want today's real unequip", today.result.Reason, today.windowOpen)
	}
	if id := rollback.wornItemID(t, loginWearAnchorArmorSlot17); id != 0 {
		t.Fatalf("rollback phase: slot 17 item = %d, want today's unequip to have gone through", id)
	}
}

// TestEquipLoginWearAnchorSwitchMeansWhatItSays 钉住锚点开关两侧：
// 默认 slot_refresh 覆盖窗口外；显式 scene_ready 只认会话窗口（= 今天）。
func TestEquipLoginWearAnchorSwitchMeansWhatItSays(t *testing.T) {
	t.Run("default slot_refresh absorbs the late packet", func(t *testing.T) {
		setDefaultTier(t)
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		fixture.openWindowAgo(t, loginWearAnchorLateDelay)
		if got := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearAnchorLiveSlot17Body()); !got {
			t.Fatal("default anchor must absorb the late family packet")
		}
	})

	t.Run("scene_ready keeps today's fallthrough", func(t *testing.T) {
		setDefaultTier(t)
		t.Setenv(loginWearReassertAnchorEnv, loginWearReassertAnchorSceneReady)
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		fixture.openWindowAgo(t, loginWearAnchorLateDelay)
		if got := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearAnchorLiveSlot17Body()); got {
			t.Fatal("scene_ready must not cover anything past the session window (today)")
		}
	})

	t.Run("once keeps its one-shot meaning under the default anchor", func(t *testing.T) {
		setDefaultTier(t)
		t.Setenv(loginWearReassertWindowModeEnv, loginWearReassertWindowOnce)
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		fixture.openWindowAgo(t, loginWearAnchorLateDelay)
		if got := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearAnchorLiveSlot17Body()); got {
			t.Fatal("WINDOW=once means one absorbed packet per session: the slot anchor must not " +
				"silently widen it")
		}
	})

	t.Run("no scene-ready ack means no gate at all", func(t *testing.T) {
		setDefaultTier(t)
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		// 没有开窗（例如这条连接压根没走到场景就绪 ack）：闸门必须保持惰性。
		if got := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearAnchorLiveSlot17Body()); got {
			t.Fatal("the gate must stay inert when the session never opened the login window")
		}
	})

	t.Run("shape scoping survives the new tiers", func(t *testing.T) {
		setDefaultTier(t)
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		fixture.openWindowAgo(t, loginWearAnchorLateDelay)
		if got := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearBridgeEchoBody()); got {
			t.Fatal("the vendor echo shape (B) must keep falling to its own gate (identity_echo)")
		}
		if got := fixture.service.equipLoginWearFamilyWindowOpenForOp19(fixture.session, loginWearBridgeOrdinaryBody()); got {
			t.Fatal("an ordinary bag op19 must never be treated as a login wear re-assert")
		}
	})
}
