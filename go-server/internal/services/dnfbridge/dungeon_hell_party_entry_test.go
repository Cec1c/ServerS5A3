package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 现场报障（操作者原话）：
//
//	未完成深渊派对任务的角色可以直接点击挑战深渊派对按钮
//	尝试进入副本后卡在地下城选择界面
//
// 本文件钉住两件事：
//  1. 「深渊派对」请求（op16 `EntryOption`/body[5] 非 0）**必须过 `[hell quest]` 前置**：
//     没做完的角色要被拒绝；
//  2. 拒绝**必须真的发到客户端**（class1/op16 `{0, reason, 0}`），不能静默丢弃 ——
//     静默丢弃就是"卡在地下城选择界面"的直接原因。

// hellPartyBridgePVF 在既有合成副本夹具上加一段声明了深渊前置的 worldmap area：
//
//	[hell dungeon] 1      ← 这个区域是深渊区域
//	[hell quest] 7777     ← 进入深渊派对前必须通关的任务
//
// 这两个 section 的真源就在本仓 worldmap 解析器里
// （`internal/modules/dnf/worldmap/parse_worldmap.go:41`），本夹具只按同一形状喂进去。
func hellPartyBridgePVF() bridgePVFSource {
	source := bridgeDungeonPVF(false)
	source[worldmap.DefaultWorldMapList] = "1 `test.wdm`\n"
	source["worldmap/test.wdm"] = "[name]\n`Synthetic Area`\n" +
		"[hell dungeon]\n1\n" +
		"[hell quest]\n7777\n[/hell quest]\n" +
		"[dungeon]\n700 -1\n[/dungeon]\n"
	return source
}

func newHellPartyEntryTestService(
	t *testing.T,
	questRecord dnfrepo.QuestRecord,
	saveQuest bool,
	mode hellPartyEntryMode,
) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, hellPartyBridgePVF())
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
		t.Fatalf("save character: %v", err)
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "99",
		Slots:       map[string]dnfrepo.ItemStack{"0:1": {ItemID: 1, Count: 7}},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	if saveQuest {
		if err := repositories.Quest.Save(context.Background(), questRecord); err != nil {
			t.Fatalf("save quest record: %v", err)
		}
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			hellPartyEntry:     mode,
		},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:           logger,
	}
	conn := &bufferConn{}
	session := &gameSession{
		conn:                         conn,
		connID:                       "hell-party-test",
		selectedCharacterID:          99,
		selectPreviewObjectStateSent: true,
		currentSceneObjectListSent:   true,
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
	return service, session, conn, logger, logPath
}

// hellPartySelectBody 组装一条真实的 21 字节 op16：dungeon 700 / difficulty 1 /
// body[5] = hellPartyFlag（A21 `HellPartyRequestFlag => Flag1`）。
func hellPartySelectBody(hellPartyFlag byte) []byte {
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], 700)
	body[4] = 1
	body[5] = hellPartyFlag
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)
	return body
}

// 主回归：没通关 `[hell quest] 7777` 的角色点「挑战深渊派对」必须被拒绝，
// 而且拒绝必须出现在线上（正好一条 class1/op16 `{0, 0xad, 0}`），不得留下任何
// 进图运行时/场景状态。
func TestHellPartyDungeonSelectRefusesCharacterWithoutHellQuest(t *testing.T) {
	service, session, conn, logger, logPath := newHellPartyEntryTestService(
		t,
		dnfrepo.QuestRecord{
			CharacterID: "99",
			// 只是"接到任务"，没有完成：这正是操作者说的"未完成深渊派对任务"。
			States: map[int64]dnfrepo.QuestState{7777: {Status: "active"}},
		},
		true,
		hellPartyEntryVendor,
	)
	body := hellPartySelectBody(1)

	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}

	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) {
		t.Fatalf("hell party refusal packet=%+v", packet.Header)
	}
	if want := []byte{0, currentSelectDungeonMissingPermissionReason, 0}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("hell party refusal body=%x want=%x", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("hell party refusal emitted trailing entry packets: %x", rest)
	}
	assertHellPartyEntryCommittedNothing(t, session)

	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "kind=game-dungeon-hell-party-entry-blocked") ||
		!strings.Contains(logText, "reason=hell_party_quest_required") ||
		!strings.Contains(logText, "missing_hell_quest_id=7777") {
		t.Fatalf("hell party refusal log missing evidence:\n%s", logText)
	}
}

// 对照组：`[hell quest]` 已通关的角色也必须收到拒绝包（本版没有深渊派对的副本
// 运行时 —— 深渊房间/封印门/深渊怪都没接），但拒绝理由换成 DungeonUnavailable。
// 关键点仍然是：**绝不是静默丢弃**。
func TestHellPartyDungeonSelectIsRefusedNotSilentlyDroppedWhenHellQuestCleared(t *testing.T) {
	service, session, conn, logger, logPath := newHellPartyEntryTestService(
		t,
		dnfrepo.QuestRecord{
			CharacterID: "99",
			States:      map[int64]dnfrepo.QuestState{7777: {Status: "completed"}},
		},
		true,
		hellPartyEntryVendor,
	)
	body := hellPartySelectBody(1)

	if err := service.handleDungeonSelectUpper(session, body); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}

	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) {
		t.Fatalf("hell party refusal packet=%+v", packet.Header)
	}
	if want := []byte{0, currentSelectDungeonUnavailableReason, 0}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("hell party refusal body=%x want=%x", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("hell party refusal emitted trailing entry packets: %x", rest)
	}
	assertHellPartyEntryCommittedNothing(t, session)

	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "reason=hell_party_runtime_not_supported_by_this_build") {
		t.Fatalf("hell party refusal log missing evidence:\n%s", logText)
	}
	if !strings.Contains(logText, "required_hell_quest_ids=7777") {
		t.Fatalf("hell party refusal log lost the requirement list:\n%s", logText)
	}
}

// `[hell dungeon]` 没写 1 的区域（= 不是深渊区域）也拒绝，但理由要能一眼区分。
func TestHellPartyDungeonSelectRefusesAreaThatIsNotHellDungeon(t *testing.T) {
	table, resolver, monsters := loadBridgeDungeonStaticData(t, bridgeDungeonPVF(false))
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		CharacterID: "99",
		AccountID:   "account-1",
		Level:       20,
		Stats:       map[string]int64{"fatigue": 100},
	}); err != nil {
		t.Fatal(err)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	service := &Service{
		options:             options{accountID: "account-1", gameUpperBodyCodec: gameUpperBodyCodecPlain},
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:           logger,
	}
	conn := &bufferConn{}
	session := &gameSession{conn: conn, selectedCharacterID: 99}

	if err := service.handleDungeonSelectUpper(session, hellPartySelectBody(2)); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if want := []byte{0, currentSelectDungeonUnavailableReason, 0}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("non-hell area refusal body=%x want=%x rest=%x", packet.Body, want, rest)
	}
	if len(rest) != 0 {
		t.Fatalf("non-hell area refusal emitted trailing packets: %x", rest)
	}
	assertHellPartyEntryCommittedNothing(t, session)
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "reason=hell_party_area_is_not_hell_dungeon") {
		t.Fatalf("non-hell area log missing evidence:\n%s", logText)
	}
}

// 普通选图（EntryOption = 0）一个字节都不能被这个闸门碰到：仍然走完整进图序列，
// 第一条仍然是 op16 成功体。
func TestOrdinaryDungeonSelectIsNotAffectedByHellPartyGate(t *testing.T) {
	service, session, conn, logger, logPath := newHellPartyEntryTestService(
		t,
		dnfrepo.QuestRecord{CharacterID: "99"},
		true,
		hellPartyEntryVendor,
	)

	if err := service.handleDungeonSelectUpper(session, hellPartySelectBody(0)); err != nil {
		t.Fatalf("handle ordinary select: %v", err)
	}
	previewRemoval, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if previewRemoval.Header.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("ordinary select preview removal=%+v", previewRemoval.Header)
	}
	selectAck, _ := splitGameServerUpperPacket(t, rest)
	if selectAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) ||
		!bytes.Equal(selectAck.Body, upperSuccessBody(buildSelectDungeonBodyForCharacter(99))) {
		t.Fatalf("ordinary select ACK=%+v body=%x", selectAck.Header, selectAck.Body)
	}
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "game-dungeon-hell-party-entry-blocked") {
		t.Fatalf("ordinary select was intercepted by the hell party gate:\n%s", logText)
	}
}

// `legacy` 档 = 改动前行为（不校验、不回包），供实机一键 A/B。
func TestHellPartyDungeonSelectLegacyModeKeepsPreChangeSilentDrop(t *testing.T) {
	service, session, conn, logger, logPath := newHellPartyEntryTestService(
		t,
		dnfrepo.QuestRecord{CharacterID: "99"},
		true,
		hellPartyEntryLegacy,
	)

	if err := service.handleDungeonSelectUpper(session, hellPartySelectBody(1)); err != nil {
		t.Fatal(err)
	}
	if conn.write.Len() != 0 {
		t.Fatalf("legacy mode replied: %x", conn.write.Bytes())
	}
	assertHellPartyEntryCommittedNothing(t, session)
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "game-dungeon-hell-party-entry-blocked") {
		t.Fatalf("legacy mode still ran the hell party gate:\n%s", logText)
	}
}

// 开关与判据本身。
func TestHellPartyEntryModeAndRequestFlagParsing(t *testing.T) {
	if readHellPartyEntryMode("").String() != hellPartyEntryValueVendor ||
		readHellPartyEntryMode("junk").String() != hellPartyEntryValueVendor {
		t.Fatal("empty/invalid DNFBRIDGE_HELL_PARTY_ENTRY must fall back to vendor")
	}
	if readHellPartyEntryMode(" LEGACY ").String() != hellPartyEntryValueLegacy {
		t.Fatal("explicit legacy must be honoured")
	}
	if !readHellPartyEntryMode("").enforces() || readHellPartyEntryMode("legacy").enforces() {
		t.Fatal("enforces() mismatch")
	}
	var zero Service
	if zero.hellPartyEntryGate().String() != hellPartyEntryValueVendor {
		t.Fatal("zero-value Service must default to vendor")
	}
	request := dungeoncmd.SelectDungeonRequest{DungeonID: 700, Difficulty: 1}
	if hellPartySelectRequested(request) {
		t.Fatal("plain request must not be treated as a hell party request")
	}
	request.EntryOption = 1
	if !hellPartySelectRequested(request) {
		t.Fatal("EntryOption!=0 (body[5] HellPartyRequestFlag) must be a hell party request")
	}
	request.EntryOption = 0x0201
	if !hellPartySelectRequested(request) {
		t.Fatal("both hell party flag bytes must be detected")
	}
	if formatQuestIDList(nil) != "" || formatQuestIDList([]int64{7777, 7778}) != "7777,7778" {
		t.Fatalf("quest id list formatting = %q", formatQuestIDList([]int64{7777, 7778}))
	}
}

// assertHellPartyEntryCommittedNothing 钉住"被拒绝的进图不留下任何东西"。
func assertHellPartyEntryCommittedNothing(t *testing.T, session *gameSession) {
	t.Helper()
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime != nil {
		t.Fatalf("refused hell party select committed a runtime: %+v", runtime)
	}
	if session.selectPreviewActorRemoved || session.preDungeonContextPlayerStateSent ||
		session.enterSelectDungeonContextSent || session.postStartMapPlayerStateSent ||
		session.sceneBootstrapTailSent {
		t.Fatalf("refused hell party select advanced entry state: %+v", session)
	}
}

// ---------------------------------------------------------------------------
// 发起者闸门：`DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 档下 blocked 投影只当 UI 提示，
// 「发起者本人没做完 `[hell quest]`」仍然必须拒绝（现场缺陷）。
//
// 真源与逐槽位投影同源：权威任务记录（`dnf_quest_states`）+ `dungeonQuestStateSets`
// 的 cleared 集；区域门槛（任务号）只来自 PVF 的 `[hell quest]`（夹具合成 7777）。
// ---------------------------------------------------------------------------

// hellRequesterGateCase 是发起者闸门表驱动用例的一行。
type hellRequesterGateCase struct {
	name string
	// dungeonID：700 = 深渊区域 + `[hell quest] 7777`；701 = 深渊区域但没有 `[hell quest]` 段；
	// 702 = 有 `[hell quest] 7777` 但 `[hell dungeon]` 没写 1。见 hellBlockedSlotsBridgePVF。
	dungeonID uint32
	// questStates：角色 id（十进制串）→ 任务号 → 状态串。
	questStates map[string]map[int64]string
	// partyMembers 非空即造一支队伍（leader 恒在 slot 0）。
	partyMembers []uint16
	// repositoryDown = 任务仓库不可用（读不到权威记录）。
	repositoryDown   bool
	hellPartyMode    hellPartyEntryMode
	blockedSlotsMode hellBlockedSlotsMode

	wantIntercepted bool
	wantAllow       bool
	wantFailure     byte
	wantReason      string
	wantMissing     int64
	wantBlocked     []uint16
	wantPvfStatus   string
}

func TestHellPartyRequesterQuestGateTable(t *testing.T) {
	cleared := hellBlockedSlotsQuestStates(map[string]string{"99": "completed"})
	accepted := hellBlockedSlotsQuestStates(map[string]string{"99": "active"})
	noState := map[string]map[int64]string{}

	cases := []hellRequesterGateCase{
		{
			// (b) 做完了任务：照常进图，深渊房间由此产生。
			name: "on/requester cleared enters", dungeonID: 700,
			questStates: cleared, blockedSlotsMode: hellBlockedSlotsOn,
			wantAllow: true, wantReason: "hell_party_all_members_eligible",
			wantPvfStatus: hellPVFStatusIDsParsed,
		},
		{
			// (a) 现场那条报障：只接到任务、没完成 ⇒ 拒绝 0xAD，并给出缺哪一个任务号。
			name: "on/requester accepted but not cleared is refused", dungeonID: 700,
			questStates: accepted, blockedSlotsMode: hellBlockedSlotsOn,
			wantIntercepted: true, wantFailure: currentSelectDungeonMissingPermissionReason,
			wantReason: "hell_party_quest_required", wantMissing: 7777, wantBlocked: []uint16{0},
			wantPvfStatus: hellPVFStatusIDsParsed,
		},
		{
			name: "on/requester without any quest state is refused", dungeonID: 700,
			questStates: noState, blockedSlotsMode: hellBlockedSlotsOn,
			wantIntercepted: true, wantFailure: currentSelectDungeonMissingPermissionReason,
			wantReason: "hell_party_quest_required", wantMissing: 7777, wantBlocked: []uint16{0},
			wantPvfStatus: hellPVFStatusIDsParsed,
		},
		{
			// 发起者合规 + 队友不合规：厂商逐槽位口径，只投影槽位 1，不整队拒绝。
			name: "on/eligible requester keeps blocked member projected", dungeonID: 700,
			questStates:      hellBlockedSlotsQuestStates(map[string]string{"99": "completed", "100": "active"}),
			partyMembers:     []uint16{hellBlockedSlotsTestLeaderID, hellBlockedSlotsTestMemberID},
			blockedSlotsMode: hellBlockedSlotsOn,
			wantAllow:        true, wantReason: "hell_party_quest_required", wantMissing: 7777,
			wantBlocked: []uint16{1}, wantPvfStatus: hellPVFStatusIDsParsed,
		},
		{
			// 发起者自己是那个不合规的：整队不放行（判定只看发起者本人的资格）。
			name: "on/ineligible requester is refused even when the member is cleared", dungeonID: 700,
			questStates:      hellBlockedSlotsQuestStates(map[string]string{"99": "active", "100": "completed"}),
			partyMembers:     []uint16{hellBlockedSlotsTestLeaderID, hellBlockedSlotsTestMemberID},
			blockedSlotsMode: hellBlockedSlotsOn,
			wantIntercepted:  true, wantFailure: currentSelectDungeonMissingPermissionReason,
			wantReason: "hell_party_quest_required", wantMissing: 7777, wantBlocked: []uint16{0},
			wantPvfStatus: hellPVFStatusIDsParsed,
		},
		{
			// 区域**没有** `[hell quest]` 段 = 没有任务门槛（厂商
			// DungeonEntryCostService.cs:507-508 的 HellQuestIds.Count==0 ⇒ 门槛满足）。
			name: "on/area without a hell quest section has no gate", dungeonID: 701,
			questStates: accepted, blockedSlotsMode: hellBlockedSlotsOn,
			wantAllow: true, wantReason: "hell_party_area_has_no_hell_quest_section",
			wantPvfStatus: hellPVFStatusSectionAbsent,
		},
		{
			name: "on/area that is not a hell dungeon has no gate", dungeonID: 702,
			questStates: accepted, blockedSlotsMode: hellBlockedSlotsOn,
			wantAllow: true, wantReason: "hell_party_area_is_not_hell_dungeon",
			wantPvfStatus: hellPVFStatusNotHellDungeon,
		},
		{
			// 读不到权威任务记录 = 无法证明资格 ⇒ fail-closed（0x01），
			// 与默认档 vendor 路径的 hell_party_quest_record_unavailable 同一口径。
			name: "on/quest repository unavailable is refused", dungeonID: 700,
			questStates: accepted, repositoryDown: true, blockedSlotsMode: hellBlockedSlotsOn,
			wantIntercepted: true, wantFailure: currentSelectDungeonUnavailableReason,
			wantReason:    "hell_party_quest_record_unavailable",
			wantPvfStatus: hellPVFStatusIDsParsed,
		},
		{
			// 开关仍有意义：blocked off（默认档）= 逐字节今天那条 vendor 路径。
			name: "off/requester not cleared keeps the default refusal", dungeonID: 700,
			questStates: accepted, blockedSlotsMode: hellBlockedSlotsOff,
			wantIntercepted: true, wantFailure: currentSelectDungeonMissingPermissionReason,
			wantReason: "hell_party_quest_required", wantMissing: 7777,
		},
		{
			name: "off/requester cleared keeps the default runtime refusal", dungeonID: 700,
			questStates: cleared, blockedSlotsMode: hellBlockedSlotsOff,
			wantIntercepted: true, wantFailure: currentSelectDungeonUnavailableReason,
			wantReason: "hell_party_runtime_not_supported_by_this_build",
		},
		{
			// legacy 档仍然优先：不校验、不回包（实机一键 A/B），逐槽位投影也不跑。
			name: "legacy wins over on and stays pre-change", dungeonID: 700,
			questStates: accepted, hellPartyMode: hellPartyEntryLegacy, blockedSlotsMode: hellBlockedSlotsOn,
			wantAllow: true, wantReason: "hell_party_gate_legacy_mode",
		},
		{
			name: "legacy and off stays pre-change", dungeonID: 700,
			questStates: accepted, hellPartyMode: hellPartyEntryLegacy, blockedSlotsMode: hellBlockedSlotsOff,
			wantAllow: true, wantReason: "hell_party_gate_legacy_mode",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, session, _, _, _ := hellBlockedSlotsFixture(
				t, test.blockedSlotsMode, test.questStates, test.partyMembers)
			service.options.hellPartyEntry = test.hellPartyMode
			if test.repositoryDown {
				service.repositoryProvider = func() (dnfrepo.Group, bool) { return dnfrepo.Group{}, false }
			}
			request, err := dungeoncmd.DecodeSelectDungeonRequest(
				hellBlockedSlotsSelectBody(test.dungeonID))
			if err != nil {
				t.Fatal(err)
			}

			decision := service.evaluateHellPartyDungeonEntry(context.Background(), session, request, true)
			if !decision.HellParty {
				t.Fatalf("hell party request was not recognised: %+v", decision)
			}
			if decision.Intercepted != test.wantIntercepted {
				t.Fatalf("intercepted=%v want=%v (reason=%q)", decision.Intercepted, test.wantIntercepted, decision.Reason)
			}
			if decision.AllowEntry != test.wantAllow {
				t.Fatalf("allow=%v want=%v (reason=%q)", decision.AllowEntry, test.wantAllow, decision.Reason)
			}
			if decision.Reason != test.wantReason {
				t.Fatalf("reason=%q want=%q", decision.Reason, test.wantReason)
			}
			if decision.MissingQuestID != test.wantMissing {
				t.Fatalf("missing quest=%d want=%d", decision.MissingQuestID, test.wantMissing)
			}
			if decision.FailureReason != test.wantFailure {
				t.Fatalf("failure reason byte=%#x want=%#x", decision.FailureReason, test.wantFailure)
			}
			if decision.PvfStatus != test.wantPvfStatus {
				t.Fatalf("pvf_status=%q want=%q", decision.PvfStatus, test.wantPvfStatus)
			}
			if len(decision.BlockedSlots) != len(test.wantBlocked) {
				t.Fatalf("blocked slots=%v want=%v", decision.BlockedSlots, test.wantBlocked)
			}
			for index, slot := range test.wantBlocked {
				if decision.BlockedSlots[index] != slot {
					t.Fatalf("blocked slots=%v want=%v", decision.BlockedSlots, test.wantBlocked)
				}
			}
			// 被拒绝的裁决永远不能同时是放行：调用方只看 Intercepted。
			if decision.Intercepted && decision.AllowEntry {
				t.Fatalf("refusal was also marked as an allow: %+v", decision)
			}
			// 区域声明了门槛时，取证字段必须是 PVF 里那一串任务号（不得硬编码）。
			if test.wantPvfStatus == hellPVFStatusIDsParsed {
				if len(decision.RequiredQuestIDs) == 0 {
					t.Fatalf("required quest ids were not carried for %q: %+v", test.wantReason, decision)
				}
				if decision.AreaID == 0 {
					t.Fatalf("area id was not carried for %q: %+v", test.wantReason, decision)
				}
			}
		})
	}
}

// 现场那条报障的端到端回归（`DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 的实机档位）：
// 没做任务的角色点「挑战深渊派对」⇒ 客户端**正好收到一条** `{0, 0xAD, 0}` 拒绝帧，
// 服务端一个进图运行时都不建；同时逐槽位投影照旧记录（UI 提示不被丢掉）。
func TestHellPartyRequesterGateRefusalReachesTheClientAndCommitsNothing(t *testing.T) {
	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "active"}), // 只是接到任务，没有完成
		nil,
	)

	if err := service.handleDungeonSelectUpper(session, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}

	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) {
		t.Fatalf("hell party refusal packet=%+v", packet.Header)
	}
	if want := []byte{0, currentSelectDungeonMissingPermissionReason, 0}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("hell party refusal body=%x want=%x", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("hell party refusal emitted trailing entry packets: %x", rest)
	}
	assertHellPartyEntryCommittedNothing(t, session)

	// 投影还在（纯 UI 提示）：blocked 槽位已缓存，且能随进本选择页下发。
	if !service.hellBlockedSlotsPending(session) {
		t.Fatal("the refusal dropped the blocked-slot projection")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "test_hell_requester_refused"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{0})

	logText := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"kind=game-dungeon-hell-party-entry-blocked",
		"reason=hell_party_quest_required",
		"missing_hell_quest_id=7777",
		"required_hell_quest_ids=7777",
		"hell_blocked_slots_mode=on",
		"requester_quest_gate=a21_try_prepare_hell_party_requester_only",
		"pvf_status=" + hellPVFStatusIDsParsed,
		// 投影先跑：blocked 事件与汇总事件都还在（UI 提示没有被拒绝吞掉）。
		"kind=game-hell-eligibility-blocked",
		"kind=game-hell-eligibility-evaluated",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("hell requester refusal log missing %q:\n%s", want, logText)
		}
	}
}

// 对照组（同一档位）：`[hell quest]` 已通关的发起者**照常进图**（深渊房间由此产生），
// 一个 `{0,*,0}` 拒绝帧都不发，也不留 `game-dungeon-hell-party-entry-blocked`。
func TestHellPartyRequesterGateAllowsClearedRequesterThrough(t *testing.T) {
	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "completed"}),
		nil,
	)

	if err := service.handleDungeonSelectUpper(session, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}

	stream := conn.write.Bytes()
	first, rest := splitGameServerUpperPacket(t, stream)
	if first.Header.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("first packet after hell select=%+v want the preview removal", first.Header)
	}
	selectAck, _ := splitGameServerUpperPacket(t, rest)
	if selectAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) ||
		!bytes.Equal(selectAck.Body, upperSuccessBody(buildSelectDungeonBodyForCharacter(hellBlockedSlotsTestLeaderID))) {
		t.Fatalf("hell select ACK=%+v body=%x (must be the ordinary success ack, not a refusal)",
			selectAck.Header, selectAck.Body)
	}
	for remaining := stream; len(remaining) > 0; {
		packet, trailing := splitGameServerUpperPacket(t, remaining)
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) &&
			len(packet.Body) == 3 && packet.Body[0] == 0 {
			t.Fatalf("an eligible requester still got an op16 refusal frame: body=%x", packet.Body)
		}
		remaining = trailing
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime == nil || runtime.Dungeon.ID != hellBlockedSlotsTestDungeonID {
		t.Fatalf("eligible requester did not commit the dungeon runtime: %+v", runtime)
	}

	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "kind=game-dungeon-hell-party-entry-blocked") {
		t.Fatalf("eligible requester was refused by the hell party gate:\n%s", logText)
	}
	if !strings.Contains(logText, "reason=hell_party_all_members_eligible") ||
		!strings.Contains(logText, "blocked_count=0") {
		t.Fatalf("eligible requester log missing evidence:\n%s", logText)
	}
}

// 队长带队友进图时，服务端会把队长那条 op16 **内部扇出**给每个队友会话
// （`handleDungeonSelectUpperPlanned(follower, body, member, false)`，
// 见 dungeon_navigation_runtime.go:954 与 party_dungeon_entry_barrier.go）。
// 那些会话**不是发起者**：`clientRequest=false` 下只做逐槽位投影（队友自己那条 op27
// 里也要带 blocked 槽位），绝不能拿队友自己的任务状态当"发起者没资格"拒绝 ——
// 那会把整队的进图序列从中间打断，并给队友发一条它根本没请求过的拒绝帧。
func TestHellPartyRequesterGateSkipsLeaderDrivenMemberReentry(t *testing.T) {
	service, leader, _, _, _, _, _ := hellBlockedSlotsPartyFixture(
		t, map[string]string{"99": "completed", "100": "active"})
	member := &gameSession{
		connID:              "hell-party-fanout-member",
		selectedCharacterID: hellBlockedSlotsTestMemberID,
	}
	storeRuntimePartyState(member, runtimePartyStateSnapshot(leader))
	request, err := dungeoncmd.DecodeSelectDungeonRequest(
		hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID))
	if err != nil {
		t.Fatal(err)
	}

	// 扇出（clientRequest=false）：队友自己不合规，但这一条不是它的请求 ⇒ 照旧放行 + 投影。
	fanout := service.evaluateHellPartyDungeonEntry(context.Background(), member, request, false)
	if fanout.Intercepted || !fanout.AllowEntry {
		t.Fatalf("leader-driven member re-entry was refused as if it were the requester: %+v", fanout)
	}
	if len(fanout.BlockedSlots) != 1 || fanout.BlockedSlots[0] != 1 {
		t.Fatalf("fan-out projection blocked slots=%v want=[1]", fanout.BlockedSlots)
	}
	// 队友会话自己的缓存也拿到了 blocked 列表：它自己那条 op27 里要带出去。
	if !service.hellBlockedSlotsPending(member) {
		t.Fatal("the fan-out projection did not record the blocked slots on the member session")
	}

	// 队友**自己**点「挑战深渊派对」（clientRequest=true）：它没有资格 ⇒ 拒绝。
	ownRequest := service.evaluateHellPartyDungeonEntry(context.Background(), member, request, true)
	if !ownRequest.Intercepted || ownRequest.AllowEntry {
		t.Fatalf("the member's own hell request was not refused: %+v", ownRequest)
	}
	if ownRequest.FailureReason != currentSelectDungeonMissingPermissionReason {
		t.Fatalf("member's own refusal byte=%#x want=%#x",
			ownRequest.FailureReason, currentSelectDungeonMissingPermissionReason)
	}
	if ownRequest.MissingQuestID != hellBlockedSlotsTestQuestID {
		t.Fatalf("member's own refusal missing quest=%d want=%d",
			ownRequest.MissingQuestID, hellBlockedSlotsTestQuestID)
	}
}

// ---------------------------------------------------------------------------
// 真档 PVF 证据（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：任务号只从线上同源 Script.pvf 的
// worldmap area `[hell quest]` 读出来，(a) 没做完 ⇒ 拒绝、(b) 做完了 ⇒ 放行。
// ---------------------------------------------------------------------------

// realHellQuestAreaForGate 找第一个"真是深渊区域 + 真声明了 `[hell quest]` 任务号 +
// 真有副本"的真档 area，并返回它和它的一个副本 id。
func realHellQuestAreaForGate(t *testing.T, table *worldmap.Table) (worldmap.Area, int64) {
	t.Helper()
	for _, area := range table.Areas() {
		if area.HellDungeon.Value != 1 || !area.HasSection("hell quest") || len(area.HellQuestIDs) == 0 {
			continue
		}
		for _, dungeon := range area.Dungeons {
			if dungeon.DungeonID <= 0 {
				continue
			}
			return area, dungeon.DungeonID
		}
	}
	t.Fatal("real Script.pvf has no hell area with a [hell quest] list and a dungeon")
	return worldmap.Area{}, 0
}

func TestRealPVFHellPartyRequesterGateRefusesLockedAndAllowsCleared(t *testing.T) {
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatalf("open real pvf: %v", err)
	}
	table, err := worldmap.LoadSource(context.Background(), archive, worldmap.Options{SkipMaps: true, SkipDungeons: true})
	if err != nil {
		t.Fatalf("load real worldmap: %v", err)
	}
	realArea, realDungeonID := realHellQuestAreaForGate(t, table)
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatalf("build real worldmap resolver: %v", err)
	}
	byDungeon, found := table.FindAreaByDungeon(realDungeonID)
	if !found || byDungeon.ID != realArea.ID {
		t.Fatalf("FindAreaByDungeon(%d)=area %d found=%v want area %d", realDungeonID, byDungeon.ID, found, realArea.ID)
	}
	requirement := resolveHellQuestRequirement(byDungeon, found)
	if !requirement.Gate || len(requirement.RequiredQuestIDs) == 0 {
		t.Fatalf("real area %d requirement=%+v want an active hell-quest gate", realArea.ID, requirement)
	}
	lockedQuest := requirement.RequiredQuestIDs[0]
	// 真档任务号（不是夹具那个 7777）：下面所有断言都用它，硬编码任何数字都会露馅。
	realQuestStates := func(status string) map[string]map[int64]string {
		perQuest := make(map[int64]string, len(requirement.RequiredQuestIDs))
		for _, questID := range requirement.RequiredQuestIDs {
			perQuest[questID] = status
		}
		return map[string]map[int64]string{"99": perQuest}
	}

	// (a) 没做完真档 `[hell quest]`：拒绝，且拒绝帧就是 `{0, 0xAD, 0}`，什么都不提交。
	lockedService, lockedSession, lockedConn, lockedLogger, lockedLogPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn, realQuestStates("active"), nil)
	lockedService.worldMapMu.Lock()
	lockedService.worldMapTable = table
	lockedService.worldMapResolver = resolver
	lockedService.worldMapMu.Unlock()
	lockedRequest, err := dungeoncmd.DecodeSelectDungeonRequest(hellBlockedSlotsSelectBody(uint32(realDungeonID)))
	if err != nil {
		t.Fatal(err)
	}
	locked := lockedService.evaluateHellPartyDungeonEntry(context.Background(), lockedSession, lockedRequest, true)
	if !locked.Intercepted || locked.AllowEntry {
		t.Fatalf("real-PVF locked requester was allowed in: %+v", locked)
	}
	if locked.FailureReason != currentSelectDungeonMissingPermissionReason {
		t.Fatalf("real-PVF locked refusal byte=%#x want=%#x", locked.FailureReason, currentSelectDungeonMissingPermissionReason)
	}
	if locked.MissingQuestID != lockedQuest {
		t.Fatalf("real-PVF locked refusal missing quest=%d want=%d (from the document)", locked.MissingQuestID, lockedQuest)
	}
	if locked.AreaID != realArea.ID || locked.PvfStatus != hellPVFStatusIDsParsed {
		t.Fatalf("real-PVF locked area=%d pvf_status=%q want %d/%q",
			locked.AreaID, locked.PvfStatus, realArea.ID, hellPVFStatusIDsParsed)
	}
	if err := lockedService.handleDungeonSelectUpper(
		lockedSession, hellBlockedSlotsSelectBody(uint32(realDungeonID))); err != nil {
		t.Fatalf("handle real-PVF locked hell select: %v", err)
	}
	packet, rest := splitGameServerUpperPacket(t, lockedConn.write.Bytes())
	if want := []byte{0, currentSelectDungeonMissingPermissionReason, 0}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("real-PVF locked refusal body=%x want=%x", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("real-PVF locked refusal emitted trailing packets: %x", rest)
	}
	assertHellPartyEntryCommittedNothing(t, lockedSession)
	lockedLog := readPeerEndpointTestLog(t, lockedLogger, lockedLogPath)
	if !strings.Contains(lockedLog, "missing_hell_quest_id="+strconv.FormatInt(lockedQuest, 10)) ||
		!strings.Contains(lockedLog, "reason=hell_party_quest_required") {
		t.Fatalf("real-PVF locked refusal log missing evidence:\n%s", lockedLog)
	}
	t.Logf("real PVF locked requester: area=%d %q dungeon=%d quest=%d body=%x",
		realArea.ID, realArea.Name, realDungeonID, lockedQuest, packet.Body)

	// (b) 做完了真档 `[hell quest]`（全部）：放行 —— 闸门不再拒绝，请求落到普通进图链。
	clearedService, clearedSession, clearedConn, clearedLogger, clearedLogPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn, realQuestStates("completed"), nil)
	clearedService.worldMapMu.Lock()
	clearedService.worldMapTable = table
	clearedService.worldMapResolver = resolver
	clearedService.worldMapMu.Unlock()
	clearedRequest, err := dungeoncmd.DecodeSelectDungeonRequest(hellBlockedSlotsSelectBody(uint32(realDungeonID)))
	if err != nil {
		t.Fatal(err)
	}
	cleared := clearedService.evaluateHellPartyDungeonEntry(context.Background(), clearedSession, clearedRequest, true)
	if cleared.Intercepted || !cleared.AllowEntry {
		t.Fatalf("real-PVF cleared requester was refused: %+v", cleared)
	}
	if len(cleared.BlockedSlots) != 0 {
		t.Fatalf("real-PVF cleared requester blocked slots=%v want none", cleared.BlockedSlots)
	}
	if cleared.AreaID != realArea.ID || cleared.PvfStatus != hellPVFStatusIDsParsed {
		t.Fatalf("real-PVF cleared area=%d pvf_status=%q want %d/%q",
			cleared.AreaID, cleared.PvfStatus, realArea.ID, hellPVFStatusIDsParsed)
	}
	for index, questID := range requirement.RequiredQuestIDs {
		if cleared.RequiredQuestIDs[index] != questID {
			t.Fatalf("real-PVF required quests=%v want the document's %v",
				cleared.RequiredQuestIDs, requirement.RequiredQuestIDs)
		}
	}
	if err := clearedService.handleDungeonSelectUpper(
		clearedSession, hellBlockedSlotsSelectBody(uint32(realDungeonID))); err != nil {
		t.Fatalf("handle real-PVF cleared hell select: %v", err)
	}
	clearedLog := readPeerEndpointTestLog(t, clearedLogger, clearedLogPath)
	if strings.Contains(clearedLog, "kind=game-dungeon-hell-party-entry-blocked") {
		t.Fatalf("real-PVF cleared requester was refused by the hell party gate:\n%s", clearedLog)
	}
	if !strings.Contains(clearedLog, "requester_blocked=false") {
		t.Fatalf("real-PVF cleared requester log missing evidence:\n%s", clearedLog)
	}
	clearedSession.dungeon.mu.Lock()
	clearedRuntime := clearedSession.dungeon.runtime
	clearedSession.dungeon.mu.Unlock()
	// 闸门放行 ≠ 这一条真实副本身份/等级一定进得去：后面的普通进图链有自己的
	// 等级/疲劳/内容校验。这里把"闸门之后的普通链到底怎么判"一并打出来，
	// 免得把"深渊闸门放行"误读成"整条进图链成功"。
	ordinaryChain := make([]string, 0, 1)
	for _, line := range strings.Split(clearedLog, "\n") {
		if strings.Contains(line, "kind=game-dungeon-select-blocked") {
			ordinaryChain = append(ordinaryChain, strings.TrimSpace(line))
		}
		if strings.Contains(line, "kind=game-dungeon-runtime-prepared") {
			ordinaryChain = append(ordinaryChain, "game-dungeon-runtime-prepared")
		}
	}
	t.Logf("real PVF cleared requester: area=%d dungeon=%d quests=%s runtime_committed=%v packets=%d ordinary_chain=%v",
		realArea.ID, realDungeonID, formatQuestIDList(requirement.RequiredQuestIDs),
		clearedRuntime != nil, len(clearedConn.write.Bytes()), ordinaryChain)
}
