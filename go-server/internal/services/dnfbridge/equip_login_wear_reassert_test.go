package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
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

// 登录后"武器被自动卸下"（现场复发）在桥侧的钉子。
//
// 现场（`_dist/weapon-unequip-diagnosis.md` §1.5，conn `game-001200`，角色 16，
// ≈15:09:46 CST）：同一会话两条**逐字节同形**的 28 字节 op19（61423 / 61575），
// 第一条 `equipMove=noop noopGate=login_wear_reassert_noop`，第二条
// `equipMove=unequip changed=true`（武器被真卸进背包）。差别不是判据，而是窗口：
// `equip_login_wear_window.go` 的一次性消费把第二条挡在门外，`game_aligned.go:290`
// 又让**任何** op19 都能吃掉窗口。当天全服 65 次开窗只换来 26 次 noop。
//
// 本文件钉四件事：
//
//  1. 窗口只被"复述穿戴态"那一族形状的 op19 消费/延续（普通背包 op19 与厂商 B 形状
//     都不吃窗口）；
//  2. 同一条同形包连发两次，两次都判 noop、库里一个字节不动（现场回归本身）；
//  3. noop 分支在 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH 默认档下照发真落库
//     路径那份权威刷新（mode0 / 战力），off 档逐字节回到今天（只回一条 msg_id=19）；
//  4. 三个回退开关（WINDOW=once + WINDOW_MS=5000 + NOOP_REFRESH=off）逐字节回到
//     今天的下行流：第二条同形包再次变成 `unequip`。
const (
	loginWearBridgeCharacterID  = 19
	loginWearBridgeWeaponItemID = int32(101040493)
	loginWearBridgeBagSlot      = int16(10)
	loginWearBridgeWornSlot     = int16(12)
)

// loginWearBridgeFamilyBody 是现场 A 形状：客户端把身份写在**装备**端点、
// 背包端点实例为 0。offset 口径见 dnfinventory.DecodeMoveItemspaceRequest。
func loginWearBridgeFamilyBody() []byte {
	return loginWearBridgeBody(0, loginWearBridgeBagSlot, 0, 3, loginWearBridgeWornSlot, loginWearBridgeWeaponItemID)
}

// loginWearBridgeEchoBody 是现场的 B 形状（identity_echo 那一族，厂商同源判据）：
// 身份写在背包端点、装备端点实例为 0。
func loginWearBridgeEchoBody() []byte {
	return loginWearBridgeBody(0, loginWearBridgeBagSlot, loginWearBridgeWeaponItemID, 3, loginWearBridgeWornSlot, 0)
}

// loginWearBridgeOrdinaryBody 是普通背包搬运（同容器 0:10 → 0:11），不属于该族。
func loginWearBridgeOrdinaryBody() []byte {
	return loginWearBridgeBody(0, loginWearBridgeBagSlot, 3227, 0, 11, 0)
}

func loginWearBridgeBody(srcList byte, srcSlot int16, srcInstance int32, dstList byte, dstSlot int16, dstInstance int32) []byte {
	body := make([]byte, 28)
	body[0] = srcList
	binary.LittleEndian.PutUint16(body[1:3], uint16(srcSlot))
	binary.LittleEndian.PutUint32(body[3:7], uint32(srcInstance))
	binary.LittleEndian.PutUint32(body[7:11], 1)
	body[11] = dstList
	binary.LittleEndian.PutUint16(body[12:14], uint16(dstSlot))
	binary.LittleEndian.PutUint32(body[14:18], uint32(dstInstance))
	binary.LittleEndian.PutUint32(body[22:26], ^uint32(0))
	return body
}

// loginWearBridgeFixture 是一个"武器穿在槽 12、背包槽 10 为空"的最小连接。
type loginWearBridgeFixture struct {
	group   dnfrepo.Group
	service *Service
	session *gameSession
	buffer  *bufferConn
}

func newLoginWearBridgeFixture(t *testing.T) *loginWearBridgeFixture {
	t.Helper()
	ctx := context.Background()
	group := dnfrepomemory.NewMemoryGroup()
	if err := group.Account.Save(ctx, dnfrepo.AccountRecord{AccountID: "dnf:1", Metadata: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := group.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "19", AccountID: "dnf:1", Name: "Actor19", Job: "0", Level: 90,
	}); err != nil {
		t.Fatal(err)
	}
	if err := group.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, currentItemListEntryWireSize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(loginWearBridgeWornSlot))
	binary.LittleEndian.PutUint32(raw[2:6], uint32(loginWearBridgeWeaponItemID))
	if err := group.Equipment.Save(ctx, dnfrepo.EquipmentRecord{
		CharacterID: "19",
		Entries: map[string]dnfrepo.EquipmentEntry{
			"12": {
				SlotIndex: loginWearBridgeWornSlot,
				ItemID:    int64(loginWearBridgeWeaponItemID),
				RawEntry:  raw,
				Extra: map[string]string{
					"item_kind":      "equipment",
					"equipment_type": "[weapon]",
					"pvf_path":       "equipment/character/swordman/weapon/beamsword/101040493.equ",
					"equipped_slot":  "12",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	buffer := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return group, true },
	}
	session := &gameSession{
		conn:                            buffer,
		connID:                          "login-wear-reassert",
		accountID:                       "dnf:1",
		channel:                         channelcatalog.Channel{ID: 19},
		selectedCharacterID:             loginWearBridgeCharacterID,
		initialTownRouteCharacterID:     loginWearBridgeCharacterID,
		initialTownRouteStage:           currentInitialTownRoutePlayerStateSent,
		townSceneReadyCharacterID:       loginWearBridgeCharacterID,
		connectionTownActorOwnerChannel: loginWearBridgeCharacterID,
		townActorOwnerChannel:           loginWearBridgeCharacterID,
	}
	return &loginWearBridgeFixture{group: group, service: service, session: session, buffer: buffer}
}

// loginWearRound 是一次 op19 的完整桥侧+域侧往返（与 game_aligned.go 的调用点同序：
// 先算窗口标记，再路由 owner，再发上行应答与后置刷新）。
type loginWearRound struct {
	windowOpen bool
	result     alignedcmd.Result
	stream     []byte
	msgIDs     []uint16
	// bodies 是每个包的 (msg_id, classification, body) 三元组。比较"下行流是否等于今天"
	// 只能用包体：16 字节头上的 upperSeq/时间戳是**逐包**变化的（今天也一样），
	// 逐字节比整条流在任何两轮之间都不成立。
	bodies []loginWearPacket
}

type loginWearPacket struct {
	msgID          uint16
	classification byte
	body           []byte
}

func (f *loginWearBridgeFixture) runOp19(t *testing.T, body []byte) loginWearRound {
	t.Helper()
	f.buffer.write.Reset()
	windowOpen := f.service.equipLoginWearFamilyWindowOpenForOp19(f.session, body)
	result, err := dnfinventory.NewHandler().Handle(context.Background(), alignedcmd.Request{
		Opcode:              uint16(dnfenum.CmdPacketMoveItemspace),
		Body:                body,
		AccountID:           "dnf:1",
		SelectedCharacterID: loginWearBridgeCharacterID,
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

// wornItemID 读库确认槽 12 上还是不是那把武器（0 = 已经被卸走）。
func (f *loginWearBridgeFixture) wornItemID(t *testing.T) int64 {
	t.Helper()
	record, ok, err := f.group.Equipment.Load(context.Background(), "19")
	if err != nil || !ok {
		t.Fatalf("equipment load ok=%t err=%v", ok, err)
	}
	return record.Entries["12"].ItemID
}

func (f *loginWearBridgeFixture) bagHoldsWeapon(t *testing.T) bool {
	t.Helper()
	record, ok, err := f.group.Inventory.Load(context.Background(), "19")
	if err != nil || !ok {
		t.Fatalf("inventory load ok=%t err=%v", ok, err)
	}
	for _, stack := range record.Slots {
		if stack.ItemID == int64(loginWearBridgeWeaponItemID) {
			return true
		}
	}
	return false
}

// TestEquipLoginWearFamilyWindowIsShapeScoped 钉住缺口 b：窗口只被该族形状消费。
func TestEquipLoginWearFamilyWindowIsShapeScoped(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	service := &Service{}
	session := &gameSession{}
	service.openEquipLoginWearWindow(session, "test")

	if !loginWearReassertFamilyOp19Shape(loginWearBridgeFamilyBody()) {
		t.Fatal("the live A shape must be recognized as this family")
	}
	if loginWearReassertFamilyOp19Shape(loginWearBridgeEchoBody()) {
		t.Fatal("the vendor echo shape (B) is not this family: it has its own gate")
	}
	if loginWearReassertFamilyOp19Shape(loginWearBridgeOrdinaryBody()) {
		t.Fatal("an ordinary same-list bag move is not this family")
	}
	if loginWearReassertFamilyOp19Shape([]byte{1, 2, 3}) {
		t.Fatal("a body that is not the 28-byte op19 layout is not this family")
	}

	// 普通背包 op19 不吃窗口：它返回 false，窗口仍然完好。
	if service.equipLoginWearFamilyWindowOpenForOp19(session, loginWearBridgeOrdinaryBody()) {
		t.Fatal("an ordinary op19 must not be treated as a login wear reassert")
	}
	if session.equipLoginWearWindowConsumed {
		t.Fatal("an ordinary op19 must not consume the window")
	}
	// B 形状（identity_echo）也不吃窗口：现场 61550 的 B 夹在两条 A 之间。
	if service.equipLoginWearFamilyWindowOpenForOp19(session, loginWearBridgeEchoBody()) {
		t.Fatal("the vendor echo shape must not be treated as a login wear reassert")
	}
	if session.equipLoginWearWindowConsumed {
		t.Fatal("the vendor echo shape must not consume the login wear window")
	}
	// 该族仍然拿得到窗口 —— 说明前两条都没把它吃掉。
	if !service.equipLoginWearFamilyWindowOpenForOp19(session, loginWearBridgeFamilyBody()) {
		t.Fatal("the family shape must still be inside the window after an ordinary op19 and a B packet")
	}
}

// TestEquipLoginWearFamilyWindowScopingSurvivesTheRollbackTier 把回退档与本轮改动前
// **唯一**的行为差异显式钉住：形状作用域不随 WINDOW=once 关掉。今天的代码里
// "任何 op19 都能消费那一次性窗口"，于是 1345 ack 之后先来一条普通背包 op19，
// 随后那条该族包就会变成真卸下 —— 这个方向只可能是"少丢保护"，所以它不属于回退面。
func TestEquipLoginWearFamilyWindowScopingSurvivesTheRollbackTier(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(loginWearReassertWindowModeEnv, loginWearReassertWindowOnce)
	service := &Service{}
	session := &gameSession{}
	service.openEquipLoginWearWindow(session, "test")

	if service.equipLoginWearFamilyWindowOpenForOp19(session, loginWearBridgeOrdinaryBody()) {
		t.Fatal("an ordinary op19 must never be treated as a login wear reassert")
	}
	if session.equipLoginWearWindowConsumed {
		t.Fatal("the once tier must still not let an ordinary op19 consume the window")
	}
	if !service.equipLoginWearFamilyWindowOpenForOp19(session, loginWearBridgeFamilyBody()) {
		t.Fatal("the family shape must get the remaining one-shot window")
	}
	if service.equipLoginWearFamilyWindowOpenForOp19(session, loginWearBridgeFamilyBody()) {
		t.Fatal("the once tier must still be one-shot for the family shape (today)")
	}
}

// TestEquipLoginWearFamilyWindowRepeatsWithinItsLifetime 钉住缺口 a 的桥侧半边：
// 同一条同形包连发，每次都落在窗口内（默认 repeat 档）。
func TestEquipLoginWearFamilyWindowRepeatsWithinItsLifetime(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	service := &Service{}
	session := &gameSession{}
	service.openEquipLoginWearWindow(session, "test")

	body := loginWearBridgeFamilyBody()
	for hit := 1; hit <= 4; hit++ {
		if !service.equipLoginWearFamilyWindowOpenForOp19(session, body) {
			t.Fatalf("family op19 #%d fell out of the window: this is the live game-001200 "+
				"regression (the second byte-identical packet became a real unequip)", hit)
		}
	}
	// 超时之后：默认档（锚点 = 该装备槽的权威纪元）**仍然**吸收该槽**第一条**晚到的复述
	// —— 现场 +46 s / +62 s / +68 s 真卸的就是这一类（角色 6 槽 17、角色 1 槽 12）。
	expired := &gameSession{equipLoginWearWindowOpenedAt: service.gameplayNow().Add(-20 * time.Second)}
	if !service.equipLoginWearFamilyWindowOpenForOp19(expired, body) {
		t.Fatal("the default tier must absorb the worn slot's first re-assert past the window: " +
			"the live 09:09:20.875025325Z unequip (char 6 slot 17) happened exactly there")
	}
	// 回退档（scene_ready + off = 今天）才是"窗口一过就一律不吸收"。
	t.Setenv(loginWearReassertAnchorEnv, loginWearReassertAnchorSceneReady)
	t.Setenv(loginWearReassertPerSlotEnv, loginWearReassertPerSlotOff)
	rollback := &gameSession{equipLoginWearWindowOpenedAt: service.gameplayNow().Add(-20 * time.Second)}
	if service.equipLoginWearFamilyWindowOpenForOp19(rollback, body) {
		t.Fatal("an expired window must not absorb a family op19 in the rollback tier (today)")
	}
}

// TestEquipLoginWearIdenticalFamilyPacketsStayGuarded 是**现场回归本身**（桥+域）：
// 两条逐字节相同的该族 op19 都必须判 noop，武器不许离开槽 12。
func TestEquipLoginWearIdenticalFamilyPacketsStayGuarded(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(equipLoginWearReassertNoopRefreshEnv, "")
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")

	body := loginWearBridgeFamilyBody()
	var ack []byte
	for round := 1; round <= 2; round++ {
		got := fixture.runOp19(t, body)
		if !got.windowOpen {
			t.Fatalf("round %d: the window flag is false — game_aligned.go would hand the "+
				"packet to equip/owner.go:1046 and really unequip it", round)
		}
		if !strings.Contains(got.result.Reason, "equipMove=noop") ||
			!strings.Contains(got.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("round %d reason = %q", round, got.result.Reason)
		}
		if len(got.msgIDs) == 0 || got.msgIDs[0] != uint16(dnfenum.CmdPacketMoveItemspace) {
			t.Fatalf("round %d must start with the op19 ACK, got %v", round, got.msgIDs)
		}
		first := firstPacketBody(t, got.stream)
		if round == 1 {
			ack = first
		} else if !bytes.Equal(first, ack) {
			t.Fatalf("the second identical packet got a different ACK: %x vs %x", first, ack)
		}
		if id := fixture.wornItemID(t); id != int64(loginWearBridgeWeaponItemID) {
			t.Fatalf("round %d unequipped the weapon: slot 12 item = %d", round, id)
		}
		if fixture.bagHoldsWeapon(t) {
			t.Fatalf("round %d moved the weapon into the bag", round)
		}
	}
}

// TestEquipLoginWearNoopRefreshMatchesTheCommittedMoveStream 钉住 noop 分支的权威刷新：
// 默认档下 noop 的下行尾巴（ACK 之后）与**真落库**那次穿脱的尾巴同形 —— 这正是
// 让客户端本地视图收敛、停止重发那一步的机制。
func TestEquipLoginWearNoopRefreshMatchesTheCommittedMoveStream(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(equipLoginWearReassertNoopRefreshEnv, "")

	guarded := newLoginWearBridgeFixture(t)
	guarded.service.openEquipLoginWearWindow(guarded.session, "test")
	noop := guarded.runOp19(t, loginWearBridgeFamilyBody())
	if len(noop.msgIDs) < 2 {
		t.Fatalf("the default noop tier must emit the authoritative refresh after the ACK, got %v", noop.msgIDs)
	}
	if !hasMsgID(noop.msgIDs, uint16(dnfenum.CmdPacketSetUDPIPPort)) {
		t.Fatalf("noop tier must emit the mode0 self projection (msg 2), got %v", noop.msgIDs)
	}

	// 对照：同一个体、同一条 body，窗口不开 ⇒ 真卸一下，取它的尾巴。
	// 2026-09-28 第五轮：A 形状现在由**方向判定本身**判 noop（与窗口无关），所以要拿到
	// "真落库那次"的下行尾巴，必须把方向判定写回回退字面量（= 今天的口径）。
	committed := newLoginWearBridgeFixture(t)
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	real := committed.runOp19(t, loginWearBridgeFamilyBody())
	if !strings.Contains(real.result.Reason, "equipMove=unequip") {
		t.Fatalf("control move must really unequip, reason = %q", real.result.Reason)
	}
	if got, want := noop.msgIDs[1:], real.msgIDs[1:]; !equalMsgIDs(got, want) {
		t.Fatalf("noop tail = %v, committed-move tail = %v: the noop branch must send the same refresh family", got, want)
	}
	// 尾巴里的 413（战力）与 13（整容器）都由各自既有的 PVF/开关门控：单测里
	// 战力目录（combatPowerCatalog）与整容器补充档（默认 suppressed）都没有装，
	// 所以两档**同时**被 pvf_catalog_unavailable / suppressed 挡掉 —— 这正是
	// "noop 与真落库路径逐包同形"的判据本身；上线的两档差异只可能来自时长与窗口。
	if hasMsgID(noop.msgIDs, currentCombatPowerAffixMsgID) {
		t.Log("combat power (413) emitted: the fixture provided the PVF catalog")
	}
}

// TestEquipLoginWearNoopRefreshSwitchKeepsTodaysBytes 钉住开关两侧的**下行字节**：
// 默认 on = ACK + 权威刷新；off = 逐字节回到今天（只有一条 msg_id=19）。
func TestEquipLoginWearNoopRefreshSwitchKeepsTodaysBytes(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")

	t.Run("default tier emits the refresh", func(t *testing.T) {
		t.Setenv(equipLoginWearReassertNoopRefreshEnv, "")
		fixture := newLoginWearBridgeFixture(t)
		fixture.service.openEquipLoginWearWindow(fixture.session, "test")
		round := fixture.runOp19(t, loginWearBridgeFamilyBody())
		if len(round.msgIDs) < 2 {
			t.Fatalf("default tier packets = %v, want the ACK plus the authoritative refresh", round.msgIDs)
		}
	})

	t.Run("rollback tier keeps today's single ACK", func(t *testing.T) {
		t.Setenv(equipLoginWearReassertNoopRefreshEnv, "off")
		fixture := newLoginWearBridgeFixture(t)
		fixture.service.openEquipLoginWearWindow(fixture.session, "test")
		round := fixture.runOp19(t, loginWearBridgeFamilyBody())
		if len(round.msgIDs) != 1 || round.msgIDs[0] != uint16(dnfenum.CmdPacketMoveItemspace) {
			t.Fatalf("rollback tier packets = %v, want exactly one msg_id=19 (today's bytes)", round.msgIDs)
		}
		// 仍然判 noop、武器仍在槽 12：这是**唯一**被回退掉的东西——那一次权威刷新。
		if !strings.Contains(round.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
			t.Fatalf("rollback tier must keep the noop decision, reason = %q", round.result.Reason)
		}
		if id := fixture.wornItemID(t); id != int64(loginWearBridgeWeaponItemID) {
			t.Fatalf("rollback tier unequipped the weapon: slot 12 item = %d", id)
		}
	})
}

// TestEquipLoginWearRollbackSwitchesReproduceToday 钉住三个回退开关的组合
// （WINDOW=once + WINDOW_MS=5000 + NOOP_REFRESH=off）逐字节回到今天的下行流：
// 第一条同形包 noop（只有 ACK），第二条同形包**再次变成真卸下** —— 现场 game-001200
// 的复现，也正是修复档不许出现的形状。
//
// 2026-09-28 第五轮：这两条同形包现在**还有**一层保护 —— 方向判定本身
// （DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT，默认开启、与窗口无关，见
// equip/worn_slot_reassert_gate.go）。所以要复现"今天"，必须把它也写回回退字面量；
// 本用例因此显式写出它，滚动回来的三条窗口开关语义一个字都没变。
func TestEquipLoginWearRollbackSwitchesReproduceToday(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(loginWearReassertWindowModeEnv, loginWearReassertWindowOnce)
	t.Setenv(loginWearReassertWindowMillisEnv, "5000")
	t.Setenv(equipLoginWearReassertNoopRefreshEnv, "off")
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")

	fixture := newLoginWearBridgeFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")
	body := loginWearBridgeFamilyBody()

	first := fixture.runOp19(t, body)
	if !first.windowOpen || !strings.Contains(first.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("rollback round 1 = %q (window=%t)", first.result.Reason, first.windowOpen)
	}
	if len(first.msgIDs) != 1 {
		t.Fatalf("rollback round 1 packets = %v, want only the ACK (today)", first.msgIDs)
	}
	second := fixture.runOp19(t, body)
	if second.windowOpen {
		t.Fatal("rollback tier must consume the window on the first op19 (today's one-shot)")
	}
	if !strings.Contains(second.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback round 2 must reproduce the live unequip, reason = %q", second.result.Reason)
	}
	if id := fixture.wornItemID(t); id != 0 {
		t.Fatalf("rollback tier must leave slot 12 empty (today's bug), item = %d", id)
	}
	if !fixture.bagHoldsWeapon(t) {
		t.Fatal("rollback tier must move the weapon into the bag (today's bug)")
	}
	// 第二条的**下行包体**必须等于"窗口从未开启"的同一条往返（= 今天的下行）。
	// 只比包体：16 字节头上的 upperSeq/时间戳逐包变化（今天也一样），整条流逐字节
	// 相等在任何两轮之间都不成立；而 mode0 档有 200 ms 连点合并，同一会话里的第二次
	// mode0 会被并进尾随刷新 —— 所以对照必须用**新会话**（同样的角色/装备/背包状态）。
	control := newLoginWearBridgeFixture(t)
	today := control.runOp19(t, body)
	if !strings.Contains(today.result.Reason, "equipMove=unequip") {
		t.Fatalf("control round must be today's unequip, reason = %q", today.result.Reason)
	}
	if !equalPacketBodies(second.bodies, today.bodies) {
		t.Fatalf("rollback round 2 packets differ from today's:\n got = %v\n want= %v",
			describePackets(second.bodies), describePackets(today.bodies))
	}
}

// TestEquipLoginWearNoopRefreshConvergesTheClientResendLoop 是本轮修复的**收敛**口径。
//
// ⚠️ 这里的"客户端"是一个**模型**，不是实测客户端：它照抄
// `_dist/weapon-unequip-diagnosis.md` §3.3 的下行序列对照 —— 命中闸门的 noop 只回一条
// `msg_id=19`（61426-61427 之后什么都没有），客户端本地那份不一致的视图无从纠正，
// 于是在同一会话里 A↔B 来回重发（现场 2..9 条）；而真落库那次后面跟着
// `msg_id=13 / 2 / 413`（61580-61588）。模型规则：**一轮里收到权威穿戴刷新
// （mode0 或战力）就停止重发，只收到 ACK 就继续重发**。
// 诊断 §10.8 已声明"noop 补刷新能否让客户端停止重发"尚未实机复验 —— 这条测试钉的是
// 修复所提供的**机制**，不是客户端实测行为。
//
// 证伪：把 DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP_REFRESH 设成 off 后重跑本用例，
// "收敛"断言必然失败 —— 运行记录见 _dist/login-wear-reassert-implementation.md。
func TestEquipLoginWearNoopRefreshConvergesTheClientResendLoop(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	fixture := newLoginWearBridgeFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")

	body := loginWearBridgeFamilyBody()
	sent := 0
	// 客户端最多重发 9 次（现场上界）；收敛就必须在此之前停下。
	for attempt := 1; attempt <= 9; attempt++ {
		round := fixture.runOp19(t, body)
		sent++
		if !loginWearClientWouldResend(round.msgIDs) {
			break
		}
	}
	if sent != 1 {
		t.Fatalf("the client-like loop kept re-sending: %d identical op19 packets; the noop "+
			"reply must carry the authoritative worn-state refresh so the stale local view converges", sent)
	}
	// 收敛过程中武器一次都不许被卸下。
	if id := fixture.wornItemID(t); id != int64(loginWearBridgeWeaponItemID) {
		t.Fatalf("the weapon left slot 12 during the resend loop: item = %d", id)
	}
}

// loginWearClientWouldResend 就是上面那条模型规则（见该用例的说明）：
// 只收到 op19 ACK ⇒ 继续重发；收到 mode0（msg 2）或战力（msg 413）⇒ 收敛。
func loginWearClientWouldResend(msgIDs []uint16) bool {
	for _, id := range msgIDs {
		if id == uint16(dnfenum.CmdPacketSetUDPIPPort) || id == currentCombatPowerAffixMsgID {
			return false
		}
	}
	return len(msgIDs) > 0
}

func hasMsgID(ids []uint16, want uint16) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func equalMsgIDs(got []uint16, want []uint16) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func equalPacketBodies(got []loginWearPacket, want []loginWearPacket) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].msgID != want[index].msgID ||
			got[index].classification != want[index].classification ||
			!bytes.Equal(got[index].body, want[index].body) {
			return false
		}
	}
	return true
}

func describePackets(packets []loginWearPacket) string {
	var builder strings.Builder
	for index, packet := range packets {
		if index != 0 {
			builder.WriteString(" ")
		}
		builder.WriteString(strconv.Itoa(int(packet.msgID)))
		builder.WriteString("/")
		builder.WriteString(strconv.Itoa(int(packet.classification)))
		builder.WriteString("/")
		builder.WriteString(hex.EncodeToString(packet.body))
	}
	return builder.String()
}

func firstPacketBody(t *testing.T, stream []byte) []byte {
	t.Helper()
	packet, _ := splitLongHengGameServerUpperPacket(t, stream)
	return append([]byte(nil), packet.Body...)
}
