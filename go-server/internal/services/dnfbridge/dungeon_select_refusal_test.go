package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfdungeon "longheng.io/server/internal/modules/dnf/dungeon"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 现场缺陷：`handleDungeonSelectUpperPlanned` 过去只在等级不足档回拒绝包，其余
// 前置失败（疲劳不足 / 疲劳不可知 / 队伍超限 / 副本未被 PVF 收录）只记
// `game-dungeon-select-blocked reason=dungeon_owner_validation_failed` 然后
// 静默 `return nil`，玩家看到的就是「开始冒险没反应」。本文件的用例钉住：
// 每一类失败都回**恰好一包** `{0,reason,0}`（msg id = CmdPacketSelectDungeon），
// 并且拒绝发生在任何 runtime/场景提交之前。

// newDungeonSelectRefusalFixture 造一份最小进图夹具：真档形状的合成 PVF
// （dungeon 700，`[minimum required level] 10`、`[limit party count] 1`）、
// 内存仓储、缓冲连接，以及一块可读回的包日志。`partyMembers` 非空时按队伍进图
// 处理（队长 = 本会话角色 99）。
func newDungeonSelectRefusalFixture(
	t *testing.T,
	character dnfrepo.CharacterRecord,
	partyMembers []alignedcmd.PartyMemberState,
) (*Service, *gameSession) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	connection := &bufferConn{}
	session := &gameSession{conn: connection, connID: "op16-refusal-test", selectedCharacterID: 99}
	if len(partyMembers) != 0 {
		session.party.state = alignedcmd.PartyState{
			PartyID:    1,
			IsLeader:   true,
			UserID:     99,
			MaxMembers: 4,
			Members:    partyMembers,
		}
	}
	return service, session
}

func TestHandleDungeonSelectUpperAnswersEveryEntryValidationFailure(t *testing.T) {
	// 合成 dungeon 700 声明 `[limit party count] 1`：两人队即超限。
	overLimitParty := []alignedcmd.PartyMemberState{
		{UserID: 99, UserState: 1, HPPercent: 100, MPPercent: 100},
		{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100},
	}
	tests := []struct {
		name       string
		character  dnfrepo.CharacterRecord
		dungeonID  uint32
		members    []alignedcmd.PartyMemberState
		wantReason byte
		wantClass  string
	}{
		{
			name: "entry level too low stays on the established reason",
			character: dnfrepo.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 9,
				Stats: map[string]int64{"fatigue": 100},
			},
			dungeonID:  700,
			wantReason: currentSelectDungeonEntryLevelTooLowReason,
			wantClass:  "entry_level_too_low",
		},
		{
			name: "fatigue exhausted answers InsufficientFatigue",
			character: dnfrepo.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 20,
				Stats: map[string]int64{"fatigue": 0},
			},
			dungeonID:  700,
			wantReason: currentSelectDungeonInsufficientFatigueReason,
			wantClass:  "entry_fatigue_exhausted",
		},
		{
			name: "party exceeds the PVF limit answers MemberEntryLimitReached",
			character: dnfrepo.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 20,
				Stats: map[string]int64{"fatigue": 100},
			},
			dungeonID:  700,
			members:    overLimitParty,
			wantReason: currentSelectDungeonMemberEntryLimitReason,
			wantClass:  "entry_party_limit",
		},
		{
			name: "dungeon missing from the PVF answers DungeonUnavailable",
			character: dnfrepo.CharacterRecord{
				CharacterID: "99", AccountID: "account-1", Level: 20,
				Stats: map[string]int64{"fatigue": 100},
			},
			dungeonID:  701,
			wantReason: currentSelectDungeonUnavailableReason,
			wantClass:  "entry_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, session := newDungeonSelectRefusalFixture(t, test.character, test.members)
			body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
			binary.LittleEndian.PutUint32(body[0:4], test.dungeonID)
			binary.LittleEndian.PutUint16(body[9:11], 0xffff)

			if err := service.handleDungeonSelectUpper(session, body); err != nil {
				t.Fatalf("select dungeon: %v", err)
			}

			assertDungeonSelectRefusal(
				t, service, session,
				test.wantReason, "dungeon_owner_validation_failed", test.wantClass)
			if len(test.members) != 0 {
				state := runtimePartyStateSnapshot(session)
				if state.PartyID != 1 || state.UserID != 99 || len(state.Members) != len(test.members) {
					t.Fatalf("refusal mutated the party state: %+v", state)
				}
			}
		})
	}
}

// assertDungeonSelectRefusal 钉住一次拒绝的三件事：(a) 恰好一包
// `{0,reason,0}`（msg id = op16，普通频道分类）；(b) runtime / 场景 / 进图进度位
// 一律未被改动；(c) 日志里带上判定依据（站点 reason、failure_class、reply_sent）。
func assertDungeonSelectRefusal(
	t *testing.T,
	service *Service,
	session *gameSession,
	wantReason byte,
	wantSiteReason string,
	wantClass string,
) {
	t.Helper()
	connection := session.conn.(*bufferConn)
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) ||
		!bytes.Equal(packet.Body, []byte{0, wantReason, 0}) {
		t.Fatalf("refusal packet header=%+v body=%x want {00 %02x 00}",
			packet.Header, packet.Body, wantReason)
	}
	if len(rest) != 0 {
		t.Fatalf("refusal emitted trailing packets: %x", rest)
	}

	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime != nil {
		t.Fatalf("refusal committed a dungeon runtime: %+v", runtime)
	}
	if session.selectPreviewActorRemoved || session.preDungeonContextPlayerStateSent ||
		session.enterSelectDungeonContextSent || session.enterSelectDungeonSent ||
		session.enterSelectDungeonAckSent || session.postStartMapPlayerStateSent ||
		session.sceneBootstrapTailSent {
		t.Fatalf("refusal advanced entry state: %+v", session)
	}

	log := testPacketLogText(t, service)
	for _, want := range []string{
		"game-dungeon-select-blocked",
		"reason=" + wantSiteReason,
		"failure_class=" + wantClass,
		"reply_sent=true",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("refusal log missing %q:\n%s", want, log)
		}
	}
}

// TestHandleDungeonSelectUpperAnswersTownReturnOriginUnavailable 覆盖进图准备阶段
// 的第二个静默点：返回城镇原点不可用（角色行没有持久化落点、会话也没有 op35
// 绑定的 selector origin）。拒绝发生在 runtime 提交之前，所以必须回包。
func TestHandleDungeonSelectUpperAnswersTownReturnOriginUnavailable(t *testing.T) {
	service, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{"fatigue": 100},
	}, nil)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)

	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("select dungeon: %v", err)
	}
	assertDungeonSelectRefusal(
		t, service, session,
		currentSelectDungeonInvalidSelectionStateReason,
		"ordinary_dungeon_town_return_origin_unavailable",
		"entry_town_return_origin_unavailable")
}

// TestHandleDungeonSelectUpperAnswersUnsupportedEntryMode 覆盖进图准备阶段的第三个
// 静默点：op16 请求模式不被当前 EXE 证明支持（`validateCurrentDungeonEntryRequest`
// 的 errDungeonEntryModeUnsupported）。同样必须在提交 runtime 之前回包。
// TestRequiredItemRefusalUsesVendorMissingItemReason 钉住"缺 PVF `[required item]` 时回厂商
// MissingRequiredItem(0x11)"：客户端只有拿到 0x11 才会在聊天框系统栏渲染"缺少 XXX"那一行，
// 回 0x01 时玩家被拦住却看不到任何提示（操作者报的"黄龙大会缺入选资格，系统栏没弹东西"）。
// 回退档必须逐字节回到 0x01。
func TestRequiredItemRefusalUsesVendorMissingItemReason(t *testing.T) {
	t.Setenv(dungeonEntryRequiredItemReasonEnv, "")
	if reason, class := currentSelectDungeonEntryFailureReason(dnfdungeon.ErrEntryRequiredItemMissing); reason != 0x11 || class != "entry_required_item_missing" {
		t.Fatalf("fixed tier reason=%#02x class=%q, want 0x11/entry_required_item_missing", reason, class)
	}
	if reason, class := currentSelectDungeonEntryFailureReason(dnfdungeon.ErrEntryRequiredItemUnknown); reason != 0x11 || class != "entry_required_item_unknown" {
		t.Fatalf("fixed tier unknown reason=%#02x class=%q, want 0x11/entry_required_item_unknown", reason, class)
	}

	// 回退档：与改动前逐字节相同。
	t.Setenv(dungeonEntryRequiredItemReasonEnv, dungeonEntryRequiredItemReasonUnavailableValue)
	if reason, class := currentSelectDungeonEntryFailureReason(dnfdungeon.ErrEntryRequiredItemMissing); reason != 0x01 || class != "entry_required_item_missing" {
		t.Fatalf("rollback tier reason=%#02x class=%q, want 0x01/entry_required_item_missing", reason, class)
	}

	// 这条开关只动 required-item 一个 case：其它类别逐字节不变。
	for _, probe := range []struct {
		name string
		err  error
		want byte
	}{
		{name: "level too low", err: dnfdungeon.ErrEntryLevelTooLow, want: currentSelectDungeonEntryLevelTooLowReason},
		{name: "fatigue", err: dnfdungeon.ErrEntryFatigueExhausted, want: currentSelectDungeonInsufficientFatigueReason},
		{name: "party limit", err: dnfdungeon.ErrEntryPartyLimit, want: currentSelectDungeonMemberEntryLimitReason},
	} {
		if reason, _ := currentSelectDungeonEntryFailureReason(probe.err); reason != probe.want {
			t.Fatalf("%s reason=%#02x, want %#02x (must not be affected)", probe.name, reason, probe.want)
		}
	}
}

func TestHandleDungeonSelectUpperAnswersUnsupportedEntryMode(t *testing.T) {
	// 这条测试钉的是"模式不支持 ⇒ 必须回包"这条契约本身，所以把深渊标志的读取位置放回
	// **回退档**（读 body[5:7]，真机 245/245 次恒为 0），body[7] 才仍然是一个纯粹的
	// "selection mode" 值。修复档下 body[7] 是厂商 Flag1（深渊标志），带 1 的请求是合法的
	// 开深渊请求（见 TestLiveOp16BodiesProveTheHellFlagSitsAtBody7）。
	t.Setenv(dungeonHellRequestByteEnv, dungeonHellRequestByteEntryOptionValue)
	service, session := newDungeonSelectRefusalFixture(t, dnfrepo.CharacterRecord{
		CharacterID: "99", AccountID: "account-1", Level: 20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}, nil)
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	// body[7] = SelectionMode（回退档口径）：普通选图请求必须为 0。
	body[7] = 1

	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("select dungeon: %v", err)
	}
	assertDungeonSelectRefusal(
		t, service, session,
		currentSelectDungeonInvalidSelectionStateReason,
		"dungeon_entry_packet_preflight_failed",
		"entry_mode_unsupported")
}

// TestHandleDungeonSelectUpperSuccessPathStaysByteIdentical 证明新增的拒绝回包
// 没有进入成功路径：op16 ACK 与 op5 的字节仍与既有口径逐字节一致，整条流里
// 不存在任何 `{0,*,0}` 拒绝帧。
func TestHandleDungeonSelectUpperSuccessPathStaysByteIdentical(t *testing.T) {
	table, resolver, monsters := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats: map[string]int64{
			"fatigue": 100, "town_id": 38, "area_id": 1,
			"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{CharacterID: "99", Slots: map[string]dnfrepo.ItemStack{}}); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 1, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
	}
	connection := &bufferConn{}
	session := &gameSession{
		conn:                          connection,
		selectedCharacterID:           99,
		enterSelectDungeonContextSent: true,
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)

	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatal(err)
	}

	ack, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) ||
		!bytes.Equal(ack.Body, upperSuccessBody(buildSelectDungeonBodyForCharacter(99))) {
		t.Fatalf("success op16 ACK header=%+v body=%x", ack.Header, ack.Body)
	}
	resource, rest := splitGameServerUpperPacket(t, rest)
	if resource.Header.MsgID != currentDungeonResourceStateMsgID ||
		!bytes.Equal(resource.Body, buildCurrentDungeonResourceStateBody(700)) {
		t.Fatalf("success op5 header=%+v body=%x", resource.Header, resource.Body)
	}
	rest = assertCurrentPreDungeonContextPlayerState(t, session, rest)
	next, rest := splitGameServerUpperPacket(t, rest)
	if next.Header.MsgID != currentDungeonInfoNotification {
		t.Fatalf("packet after pre-op27 actor state=%+v, want op28", next.Header)
	}
	// 整条流里不允许出现任何 `{0,*,0}` 拒绝帧（同一个 op16 消息号）。
	for len(rest) > 0 {
		packet, trailing := splitGameServerUpperPacket(t, rest)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) &&
			len(packet.Body) == 3 && packet.Body[0] == 0 {
			t.Fatalf("success path emitted an op16 refusal frame: body=%x", packet.Body)
		}
		rest = trailing
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime == nil || runtime.Dungeon.ID != 700 {
		t.Fatalf("success path did not commit the runtime: %+v", runtime)
	}
}
