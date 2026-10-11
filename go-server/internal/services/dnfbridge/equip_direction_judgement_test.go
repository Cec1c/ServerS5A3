package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfequip "longheng.io/server/internal/modules/dnf/equip"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住 2026-09-28 第五轮的两件事，都用**线上原文**（13 字节 legacy 头 + 28 字节
// op19 body，取自 `/opt/dnf90/logs/packet_log.txt`）：
//
//  1. 判读事件 `EVENT kind=game-equip-direction-judgement`：把方向判定的四个事实
//     （judgement / endpoints / client_named_worn_item / expected_noop_gate）写进
//     packet_log.txt，让下一次实机重登不必再解码 hex；
//
//  2. 端到端语义：把**每一条窗口闸门都强制关闭**之后，那两条复述原文仍然判 noop、
//     一个字节都不写（`noopGate=worn_slot_reassert`）；把方向判定写回回退字面量之后，
//     同一条包逐字节回到今天的真卸。
//
//     :93098  t=13:24:21.46511482Z     光剑在身上那次重登的复述
//     :91884  t=13:20:21.733523941Z    同一次重登的更早一条（包体逐字节相同）
//     :76912  入站 t=10:37:20.89653374Z 同一形状在旧口径下真的执行成 unequip
const (
	equipDirectionJudgementSwitchEnv = "DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT"

	equipDirectionJudgementRelogHex132421 = "01130029000000c97de1fe22000010000000000001000000030c006dc1050600000000ffffffff0000"
	equipDirectionJudgementRelogHex132021 = "01130029000000805fff851f000010000000000001000000030c006dc1050600000000ffffffff0000"
	equipDirectionJudgementPreGateHex     = "01130029000000162069b12b00000a000000000001000000030c006dc1050600000000ffffffff0000"
)

// equipDirectionJudgementLiveBody 把线上帧的 hex 还原成 28 字节 op19 body（桥侧与
// game_aligned.go 拿到的是同一份 body，decode 用生产解码器）。
func equipDirectionJudgementLiveBody(t *testing.T, frameHex string) []byte {
	t.Helper()
	frame, err := hex.DecodeString(frameHex)
	if err != nil {
		t.Fatalf("decode live frame %q: %v", frameHex, err)
	}
	const headerSize = 13
	if len(frame) != headerSize+28 {
		t.Fatalf("live frame %q = %d bytes, want header+28", frameHex, len(frame))
	}
	return frame[headerSize:]
}

// firstLineContaining 返回日志里第一条含 needle 的行（判读事件是一行一个 kind）。
func firstLineContaining(logged, needle string) (string, bool) {
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, needle) {
			return line, true
		}
	}
	return "", false
}

// equipDirectionJudgementFixture 打开包日志（判读事件必须落到 packet_log 口径的那份日志里）。
func equipDirectionJudgementFixture(t *testing.T) (*loginWearAnchorFixture, *packetLogger, string) {
	t.Helper()
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	logger, logPath := newPeerEndpointTestLogger(t)
	fixture.service.packetLog = logger
	return fixture, logger, logPath
}

// equipDirectionJudgementStaleClaimFixture 复刻 AD 报告那一刻的权威状态：**槽 12 空着**
// （玩家刚把武器脱下来）、武器躺在背包 bagSlot 上，而且背包行带着 0x77 原始行
// （正是 unequip 路径写回背包的那种行）—— 旧口径的 o.equip 因此会成功把它穿回去。
// 包日志由调用方挂（需要判读事件时才 newPeerEndpointTestLogger，避免文件句柄留在 TempDir）。
func equipDirectionJudgementStaleClaimFixture(t *testing.T, bagSlot int16) *loginWearAnchorFixture {
	t.Helper()
	fixture := newLoginWearAnchorFixture(t, "6", 6, map[int16]int64{})
	stack := dnfrepo.ItemStack{
		ItemID:   loginWearAnchorWeaponItemID,
		Count:    1,
		RawEntry: loginWearAnchorRawEntry(bagSlot, loginWearAnchorWeaponItemID, 203004656, 48),
		Extra: map[string]string{
			"item_kind":      "equipment",
			"pvf_path":       "equipment/character/swordman/weapon/beamsword/101040493.equ",
			"equipment_type": "[weapon]",
			"durability":     "48",
			"max_durability": "48",
			"quality_seed":   "203004656",
			"equipped_slot":  "12",
		},
	}
	if err := fixture.group.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots:       map[string]dnfrepo.ItemStack{inventoryKeyForDirectionJudgement(loginWearFamilySourceListMain, bagSlot): stack},
		Warehouse:   map[string]dnfrepo.ItemStack{},
	}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// inventoryKeyForDirectionJudgement 是背包 key 的最小构造（"list:slot"）。
func inventoryKeyForDirectionJudgement(listType byte, slot int16) string {
	return strconv.Itoa(int(listType)) + ":" + strconv.Itoa(int(slot))
}

// 判读事件：形状命中即打一行，且四个事实都在。
func TestEquipDirectionJudgementEventNamesTheJudgementEndpointsItemAndOutcome(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(equipWornSlotReassertSwitchEnv, "")
	fixture, logger, logPath := equipDirectionJudgementFixture(t)
	// 不开窗（会话窗口/锚点/每槽/轮次全都不吸收）⇒ 这条包交给域层的方向判定。
	round := fixture.runOp19(t, equipDirectionJudgementLiveBody(t, equipDirectionJudgementRelogHex132421))
	if round.windowOpen {
		t.Fatal("no window was opened: the packet must be released to the domain judgement")
	}
	if !strings.Contains(round.result.Reason, "equipMove=noop") ||
		!strings.Contains(round.result.Reason, "noopGate="+dnfequip.WornSlotReassertGate) ||
		!strings.Contains(round.result.Reason, "item=101040493") ||
		!strings.Contains(round.result.Reason, "changed=false") {
		t.Fatalf("op19 line = %q, want a noop owned by %s with the worn item and changed=false",
			round.result.Reason, dnfequip.WornSlotReassertGate)
	}
	if got := fixture.wornItemID(t, loginWearAnchorWeaponSlot); got != loginWearAnchorWeaponItemID {
		t.Fatalf("slot 12 item = %d, want %d still worn", got, loginWearAnchorWeaponItemID)
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	event, ok := firstLineContaining(logged, "EVENT kind="+equipDirectionJudgementEvent)
	if !ok {
		t.Fatalf("the judgement event is missing from the packet log:\n%s", logged)
	}
	for _, want := range []string{
		"judgement=worn_endpoint_reassert",
		"src_list=0", "src_slot=16", "src_instance=0",
		"dst_list=3", "dst_slot=12", "dst_instance=101040493",
		"endpoints=src=(0,16,0)->dst=(3,12,101040493)",
		"client_named_worn_item=101040493",
		"window_absorption=released_to_the_domain_direction_judgement",
		"domain_rule=" + dnfequip.WornSlotReassertGate,
		"domain_rule_when_that_slot_is_empty=" + dnfequip.WornSlotStaleClaimGate,
		"worn_slot_reassert_switch=on",
		"expected_noop_gate=" + dnfequip.WornSlotReassertGate,
		"expected_noop_gate_when_that_slot_is_empty=" + dnfequip.WornSlotStaleClaimGate,
		"outcome_source=the_following_op19_equipMove_and_noopGate_fields",
	} {
		if !strings.Contains(event, want) {
			t.Fatalf("judgement event = %q, want field %q", event, want)
		}
	}
}

// 窗口各层吸收时，事件必须说明"这一次 noop 归窗口层"，并给出它具名的 gate ——
// 操作者据此区分"窗口挡住的"与"语义挡住的"。
func TestEquipDirectionJudgementEventNamesTheAbsorbingLayer(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
	t.Setenv(equipWornSlotReassertSwitchEnv, "")
	setDefaultTier(t)
	fixture, logger, logPath := equipDirectionJudgementFixture(t)
	fixture.service.openEquipLoginWearWindow(fixture.session, "test")

	round := fixture.runOp19(t, equipDirectionJudgementLiveBody(t, equipDirectionJudgementRelogHex132021))
	if !round.windowOpen {
		t.Fatal("the family packet inside an open window must be absorbed by the window layers")
	}
	if !strings.Contains(round.result.Reason, "noopGate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("op19 line = %q, want the pre-existing login_wear_reassert_noop", round.result.Reason)
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	event, ok := firstLineContaining(logged, "EVENT kind="+equipDirectionJudgementEvent)
	if !ok {
		t.Fatalf("the judgement event is missing from the packet log:\n%s", logged)
	}
	if !strings.Contains(event, "window_absorption=absorbed_by_the_login_wear_reassert_layers") ||
		!strings.Contains(event, "expected_noop_gate="+dnfequip.LoginWearReassertNoopGate) {
		t.Fatalf("judgement event = %q, want the window layer named as the noop owner", event)
	}
}

// 两条现场复述原文 + :76912（旧口径下真的卸下过武器的那条）：**每一条窗口闸门都强制
// 关闭**之后必须判 noop、装备行与背包行一个字节都不动。
//
// "每一条窗口闸门都强制关闭"在这里取最强的一种：既包括主开关关闭（整族连窗口函数都
// 不会被调用），也包括**四层收口全写回退字面量 + 会话窗口已过期**（现场 :76912 那一刻
// 的档位组合）—— 两档下这条包都被放行到域层，noop 只可能来自方向判定本身。
func TestEquipDirectionJudgementCarriesTheLiveReassertShapeWithoutAnyWindow(t *testing.T) {
	for name, frameHex := range map[string]string{
		"live :93098 t=13:24:21.46511482Z":  equipDirectionJudgementRelogHex132421,
		"live :91884 t=13:20:21.733523941Z": equipDirectionJudgementRelogHex132021,
		"live :76912 t=10:37:20.89653374Z":  equipDirectionJudgementPreGateHex,
	} {
		for _, tier := range []struct {
			name                string
			loginWear           string
			allLayersRolledBack bool
		}{
			{"login wear switch on, no window opened", "1", false},
			{"login wear family switched off", "0", false},
			{"every layer at its rollback literal and the session window expired", "1", true},
		} {
			t.Run(name+"/"+tier.name, func(t *testing.T) {
				t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", tier.loginWear)
				t.Setenv(equipWornSlotReassertSwitchEnv, "")
				fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
				if tier.allLayersRolledBack {
					// 四层收口（会话窗口 / 槽锚点 / 每槽 / 轮次）全部写回退字面量，
					// 只把方向判定留在默认档：这样"为什么没卸下"只可能是形状判据。
					t.Setenv(loginWearReassertWindowModeEnv, loginWearReassertWindowOnce)
					t.Setenv(loginWearReassertWindowMillisEnv, "5000")
					t.Setenv(loginWearReassertAnchorEnv, loginWearReassertAnchorSceneReady)
					t.Setenv(loginWearReassertPerSlotEnv, loginWearReassertPerSlotOff)
					t.Setenv(loginWearReassertRepeatEnv, loginWearReassertRepeatGate)
					t.Setenv(loginWearReassertRoundEnv, loginWearReassertRoundSession)
					fixture.service.openEquipLoginWearWindow(fixture.session, "test")
					fixture.session.equipLoginWearWindowOpenedAt = fixture.service.gameplayNow().Add(-20 * time.Second)
				}

				round := fixture.runOp19(t, equipDirectionJudgementLiveBody(t, frameHex))
				if round.windowOpen {
					t.Fatal("every window layer is closed: the packet must be released to the domain")
				}
				if !strings.Contains(round.result.Reason, "equipMove=noop") ||
					!strings.Contains(round.result.Reason, "noopGate="+dnfequip.WornSlotReassertGate) {
					t.Fatalf("op19 line = %q, want the window-independent %s noop",
						round.result.Reason, dnfequip.WornSlotReassertGate)
				}
				if got := fixture.wornItemID(t, loginWearAnchorWeaponSlot); got != loginWearAnchorWeaponItemID {
					t.Fatalf("slot 12 item = %d, want %d still worn", got, loginWearAnchorWeaponItemID)
				}
				if slot, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); inBag {
					t.Fatalf("a re-assert moved the weapon into bag %s", slot)
				}
			})
		}
	}
}

// 回退字面量：整条链路上同一条原文逐字节回到今天的真卸；判读事件照打（档位可观测性
// 不许依赖档位本身）。
func TestEquipDirectionJudgementRollbackRestoresTheLiveUnequipAndStillLogs(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "0")
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	fixture, logger, logPath := equipDirectionJudgementFixture(t)

	round := fixture.runOp19(t, equipDirectionJudgementLiveBody(t, equipDirectionJudgementPreGateHex))
	if round.windowOpen || !strings.Contains(round.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback line = %q (window=%t), want today's real unequip", round.result.Reason, round.windowOpen)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != 0 {
		t.Fatalf("rollback tier: slot 12 item = %d, want today's unequip", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); !inBag {
		t.Fatal("rollback tier: the beamsword must land in the bag (today's behaviour)")
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	event, ok := firstLineContaining(logged, "EVENT kind="+equipDirectionJudgementEvent)
	if !ok {
		t.Fatalf("the judgement event must be printed in the rollback tier too:\n%s", logged)
	}
	if !strings.Contains(event, "worn_slot_reassert_switch=off") {
		t.Fatalf("judgement event = %q, want worn_slot_reassert_switch=off", event)
	}
}

// 方向判定的 noop 必须**带上权威状态**，否则只修了写库、客户端的幻影副本还在
// （它会以为"背包那一格还有一件"/"穿戴格还穿着"，下一次登录再复述同一句）。
//
// 走的是**登录作用域的逐槽权威行**（sendLoginEquipmentNoopSlotRows：list 0/1 源端点行 +
// list 3 穿戴端点行，class0/msg14 载体），**不是**整容器回放 —— 客户端在初始化期接受前者
// （同形的守卫宝石/勋章 list-3 op14 行已在线上跑通），而明确拒绝泛化的整容器刷新
// （见 initial_town_player_state.go:208-212）。DNFBRIDGE_EQUIP_MOVE_CONTAINER_REFRESH
// 的 suppressed 默认档因此一个字都不用改。
func TestEquipDirectionJudgementNoopCarriesTheAuthoritativeEndpointRows(t *testing.T) {
	splitTail := func(t *testing.T, tail []byte) []loginWearPacket {
		t.Helper()
		var out []loginWearPacket
		rest := tail
		for len(rest) > 0 {
			packet, remaining := splitGameServerUpperPacketWithHeader(t, rest, dnfproto.GameServerUpperHeaderSize16)
			rest = remaining
			out = append(out, loginWearPacket{
				msgID:          packet.Header.MsgID,
				classification: packet.Header.Classification,
				body:           append([]byte(nil), packet.Body...),
			})
		}
		return out
	}
	sendConvergence := func(t *testing.T, fixture *loginWearAnchorFixture, body []byte, round loginWearRound) []loginWearPacket {
		t.Helper()
		before := len(round.stream)
		plan := fixture.service.planLoginEquipmentNoopConvergence(fixture.session, body, round.windowOpen, round.result)
		if !plan.planned {
			t.Fatalf("the claim noop must be planned for the authoritative endpoint rows: %+v", plan)
		}
		fixture.service.sendLoginEquipmentNoopSlotRows(fixture.session, plan, "login_wear_noop_after_ack")
		full := fixture.buffer.write.Bytes()
		if len(full) <= before {
			t.Fatalf("the convergence wrote nothing: stream=%d", len(full))
		}
		return splitTail(t, append([]byte(nil), full[before:]...))
	}

	t.Run("worn slot still holds the named item", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "1")
		t.Setenv(equipWornSlotReassertSwitchEnv, "")
		fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
		body := equipDirectionJudgementLiveBody(t, equipDirectionJudgementRelogHex132421)
		round := fixture.runOp19(t, body)
		if round.windowOpen || !strings.Contains(round.result.Reason, "noopGate="+dnfequip.WornSlotReassertGate) {
			t.Fatalf("op19 line = %q (window=%t), want the worn_slot_reassert noop", round.result.Reason, round.windowOpen)
		}

		rows := sendConvergence(t, fixture, body, round)
		if len(rows) != 2 {
			t.Fatalf("convergence rows = %d, want 2 (source slot + worn slot)", len(rows))
		}
		source, worn := rows[0], rows[1]
		if source.classification != 0 || source.msgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) ||
			worn.classification != 0 || worn.msgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
			t.Fatalf("carriers = class %d msg %d / class %d msg %d, want class 0 msg %d",
				source.classification, source.msgID, worn.classification, worn.msgID,
				uint16(dnfenum.CmdPacketWalkoutPartyMember))
		}
		sourceRow := source.body[3:]
		// 源槽（客户端手里那份幻影副本所在的格子）必须是**已验证的空行哨兵**
		// 0xFFFFFFFF：itemID=0 会被客户端实例化成 `stackable/gold.stk`（金币），
		// 把那件刚脱下来的装备的本地行整体覆盖掉（耐久字一起变 0）。见
		// empty_slot_row_encoding.go。
		if source.body[0] != loginWearFamilySourceListMain ||
			binary.LittleEndian.Uint16(sourceRow[0:2]) != uint16(loginWearAnchorPlayerDropSlot) ||
			binary.LittleEndian.Uint32(sourceRow[2:6]) != emptySlotItemIDCurrentEXE {
			t.Fatalf("source row = %x, want list 0 slot 16 encoded as the verified empty-slot sentinel %#x (the client's phantom bag slot contradicted)",
				source.body, emptySlotItemIDCurrentEXE)
		}
		wornRow := worn.body[3:]
		if worn.body[0] != currentSocketListEquipment ||
			binary.LittleEndian.Uint16(wornRow[0:2]) != uint16(loginWearAnchorWeaponSlot) ||
			binary.LittleEndian.Uint32(wornRow[2:6]) != uint32(loginWearAnchorWeaponItemID) {
			t.Fatalf("worn row = %x, want list 3 slot 12 item %d", worn.body, loginWearAnchorWeaponItemID)
		}
		// 这一行必须把**权威耐久**带上（客户端渲染 0 耐久那条症状的正面钉子）：
		// 0x77 行的 +0x0B 是 u16 耐久，仓储行（raw 与 Extra 双证据）都是 48。
		if got := binary.LittleEndian.Uint16(wornRow[0x0B:0x0D]); got != 48 {
			t.Fatalf("worn row durability = %d, want 48 (row=%x)", got, worn.body)
		}
		if got := binary.LittleEndian.Uint32(wornRow[6:10]); got == 0 {
			t.Fatalf("worn row instance = 0, want the durable create/quality value (row=%x)", worn.body)
		}
	})

	t.Run("worn slot is empty after a deliberate unequip", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "0")
		t.Setenv(equipWornSlotReassertSwitchEnv, "")
		fixture := equipDirectionJudgementStaleClaimFixture(t, 10)
		body := equipDirectionJudgementLiveBody(t, equipDirectionJudgementPreGateHex)
		round := fixture.runOp19(t, body)
		if round.windowOpen || !strings.Contains(round.result.Reason, "noopGate="+dnfequip.WornSlotStaleClaimGate) {
			t.Fatalf("op19 line = %q (window=%t), want the worn_slot_stale_claim noop", round.result.Reason, round.windowOpen)
		}

		rows := sendConvergence(t, fixture, body, round)
		if len(rows) != 1 {
			t.Fatalf("convergence rows = %d, want 1 (source slot only: an empty list-3 row has no live shape)", len(rows))
		}
		sourceRow := rows[0].body[3:]
		if rows[0].body[0] != loginWearFamilySourceListMain ||
			binary.LittleEndian.Uint16(sourceRow[0:2]) != 10 ||
			binary.LittleEndian.Uint32(sourceRow[2:6]) != uint32(loginWearAnchorWeaponItemID) {
			t.Fatalf("source row = %x, want list 0 slot 10 item %d (the weapon the client says is nowhere)",
				rows[0].body, loginWearAnchorWeaponItemID)
		}
	})
}

// 回退字面量：方向判定关掉之后，这条包回到"真落库卸下"，补偿行**一个字节都不发**
// （= 今天的下行）；这也证明新增的权威行只挂在形状判据的 noop 上。
func TestEquipDirectionJudgementRollbackSendsNoConvergenceRows(t *testing.T) {
	t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "0")
	t.Setenv(equipWornSlotReassertSwitchEnv, "off")
	fixture := newLoginWearAnchorFixture(t, "6", 6, loginWearAnchorLiveArmor)
	body := equipDirectionJudgementLiveBody(t, equipDirectionJudgementPreGateHex)

	round := fixture.runOp19(t, body)
	if round.windowOpen || !strings.Contains(round.result.Reason, "equipMove=unequip") {
		t.Fatalf("rollback line = %q (window=%t), want today's real unequip", round.result.Reason, round.windowOpen)
	}
	if plan := fixture.service.planLoginEquipmentNoopConvergence(fixture.session, body, round.windowOpen, round.result); plan.planned {
		t.Fatalf("the rollback tier must not plan authoritative rows: %+v", plan)
	}
	if got := len(fixture.buffer.write.Bytes()); got != len(round.stream) {
		t.Fatalf("the rollback tier wrote %d extra bytes", got-len(round.stream))
	}
}

// 真正的脱下（源端是装备槽）走桥+域全链仍然真卸，并且**不打**判读事件（它不是该族形状）。
func TestEquipDirectionJudgementLeavesTheGenuineUnequipAloneAndSilent(t *testing.T) {
	setDefaultTier(t)
	t.Setenv(equipWornSlotReassertSwitchEnv, "")
	fixture, logger, logPath := equipDirectionJudgementFixture(t)

	body := loginWearBridgeBody(currentSocketListEquipment, loginWearAnchorWeaponSlot, int32(loginWearAnchorWeaponItemID),
		loginWearFamilySourceListMain, loginWearAnchorPlayerDropSlot, 0)
	round := fixture.runOp19(t, body)
	if round.windowOpen || !strings.Contains(round.result.Reason, "equipMove=unequip") {
		t.Fatalf("genuine unequip line = %q (window=%t), want the real unequip", round.result.Reason, round.windowOpen)
	}
	if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != 0 {
		t.Fatalf("slot 12 item = %d, want the weapon to have left the worn slot", id)
	}
	if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); !inBag {
		t.Fatal("the genuine unequip must land the beamsword in the bag")
	}
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if _, found := firstLineContaining(logged, "EVENT kind="+equipDirectionJudgementEvent); found {
		t.Fatalf("the judgement event must only fire for the re-assert family:\n%s", logged)
	}
}

// 方向 (b)（操作者 2026-09-28 更正的另一半）：玩家**故意把武器脱下来**（槽 12 空着、
// 武器在背包里），随后选角再进角色 —— 客户端照旧发那条登录复述，走桥+域全链也必须判
// noop：穿戴集保持为空、一个字节都不写，绝不用这条陈述把背包里那件穿回去。
//
// 现场那条 :76912 的原文正好就是"src=(0,10,0) dst=(3,12,101040493)"，背包 0:10 就是
// 脱下之后武器落袋的那一格，所以这里直接用它做 golden body。
func TestEquipDirectionJudgementNeverReEquipsAStaleClaim(t *testing.T) {
	const bagSlot int16 = 10
	for _, tier := range []struct {
		name      string
		loginWear string
	}{
		{"login wear switch on, no window opened", "1"},
		{"login wear family switched off", "0"},
	} {
		t.Run(tier.name, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", tier.loginWear)
			t.Setenv(equipWornSlotReassertSwitchEnv, "")
			fixture := equipDirectionJudgementStaleClaimFixture(t, bagSlot)
			logger, logPath := newPeerEndpointTestLogger(t)
			fixture.service.packetLog = logger

			round := fixture.runOp19(t, equipDirectionJudgementLiveBody(t, equipDirectionJudgementPreGateHex))
			if round.windowOpen {
				t.Fatal("no window layer is open: the packet must be released to the domain")
			}
			if !strings.Contains(round.result.Reason, "equipMove=noop") ||
				!strings.Contains(round.result.Reason, "noopGate="+dnfequip.WornSlotStaleClaimGate) {
				t.Fatalf("op19 line = %q, want a noop owned by %s", round.result.Reason, dnfequip.WornSlotStaleClaimGate)
			}
			if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != 0 {
				t.Fatalf("the stale claim re-equipped the weapon: slot 12 item = %d (the operator's AD report)", id)
			}
			if _, inBag := fixture.bagSlotHolding(t, loginWearAnchorWeaponItemID); !inBag {
				t.Fatal("the weapon must stay in the bag: the persisted state is authoritative")
			}
			logged := readPeerEndpointTestLog(t, logger, logPath)
			event, ok := firstLineContaining(logged, "EVENT kind="+equipDirectionJudgementEvent)
			if !ok {
				t.Fatalf("the judgement event is missing from the packet log:\n%s", logged)
			}
			if !strings.Contains(event, "expected_noop_gate_when_that_slot_is_empty="+dnfequip.WornSlotStaleClaimGate) {
				t.Fatalf("judgement event = %q, want the stale-claim gate named for the empty-slot case", event)
			}
		})
	}

	// 证伪相位：方向判定写回回退字面量 ⇒ 逐字节回到今天 —— 刚脱下来的武器**又**被穿回去。
	t.Run("rollback re-equips it (today's bug)", func(t *testing.T) {
		t.Setenv("DNFBRIDGE_EQUIP_LOGIN_REASSERT_NOOP", "0")
		t.Setenv(equipWornSlotReassertSwitchEnv, "off")
		fixture := equipDirectionJudgementStaleClaimFixture(t, bagSlot)

		round := fixture.runOp19(t, equipDirectionJudgementLiveBody(t, equipDirectionJudgementPreGateHex))
		if !strings.Contains(round.result.Reason, "equipMove=equip") {
			t.Fatalf("rollback line = %q, want today's re-equip", round.result.Reason)
		}
		if id := fixture.wornItemID(t, loginWearAnchorWeaponSlot); id != loginWearAnchorWeaponItemID {
			t.Fatalf("rollback slot 12 item = %d, want today's re-equip of %d",
				id, loginWearAnchorWeaponItemID)
		}
	})
}

// 桥侧的开关名字与档位读数必须与域层一致（域层是唯一消费者，桥侧只写日志）。
func TestEquipDirectionJudgementSwitchNameAgreesWithTheDomain(t *testing.T) {
	if equipWornSlotReassertSwitchEnv != "DNFBRIDGE_EQUIP_MOVE_WORN_SLOT_REASSERT" {
		t.Fatalf("bridge switch env = %q, want the shipped literal", equipWornSlotReassertSwitchEnv)
	}
	if dnfequip.WornSlotReassertGate != "worn_slot_reassert" {
		t.Fatalf("domain gate = %q, want the shipped literal", dnfequip.WornSlotReassertGate)
	}
	for _, raw := range []string{"", "on", "1", "typo"} {
		t.Setenv(equipWornSlotReassertSwitchEnv, raw)
		if got := equipWornSlotReassertSwitchMode(); got != "on" {
			t.Fatalf("%s=%q -> %q, want on (default tier)", equipWornSlotReassertSwitchEnv, raw, got)
		}
	}
	for _, raw := range []string{"off", "0", "false", "no", "legacy"} {
		t.Setenv(equipWornSlotReassertSwitchEnv, raw)
		if got := equipWornSlotReassertSwitchMode(); got != "off" {
			t.Fatalf("%s=%q -> %q, want off (rollback tier)", equipWornSlotReassertSwitchEnv, raw, got)
		}
	}
}
