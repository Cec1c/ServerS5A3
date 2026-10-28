package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/modules/dnf/worldmap"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 本文件钉住操作者清单第 69 条「挑战深渊进不去副本」的修复层：
// `DNFBRIDGE_HELL_BLOCKED_SLOTS=on` 的**逐槽位判定 + blocked 槽位下发 + 照常进图**，
// 以及"默认档逐字节回到今天（含第 57 条的必回包拒绝）"这条承诺。

// hellBlockedSlotsTestQuestID / hellBlockedSlotsTestDungeonID 与 hellPartyBridgePVF 的
// 合成 area 一致（`[hell quest] 7777` / `[dungeon] 700`）。
const (
	hellBlockedSlotsTestQuestID   int64  = 7777
	hellBlockedSlotsTestDungeonID        = 700
	hellBlockedSlotsTestLeaderID  uint16 = 99
	hellBlockedSlotsTestMemberID  uint16 = 100
)

// hellBlockedSlotsBridgePVF 在既有合成副本夹具上再补两个**没有深渊门槛**的区域，
// 用来钉住 `pvf_status` 的两种"读不到 ⇒ 不拦"：
//
//	area 1（hellPartyBridgePVF）：`[hell dungeon] 1` + `[hell quest] 7777` ⇒ 有门槛
//	area 2：`[hell dungeon] 1`、**没有** `[hell quest]` 段        ⇒ 没有门槛（pvf_hell_quest_section_absent）
//	area 3：`[hell quest] 7777`、**没有** `[hell dungeon] 1`      ⇒ 没有门槛（pvf_hell_dungeon_flag_off）
func hellBlockedSlotsBridgePVF() bridgePVFSource {
	source := hellPartyBridgePVF()
	source[worldmap.DefaultWorldMapList] = "1 `test.wdm`\n2 `nohellquest.wdm`\n3 `nothelldungeon.wdm`\n"
	source["worldmap/nohellquest.wdm"] = "[name]\n`Synthetic Hell Area Without Quest Section`\n" +
		"[hell dungeon]\n1\n" +
		"[dungeon]\n701 -1\n[/dungeon]\n"
	source["worldmap/nothelldungeon.wdm"] = "[name]\n`Synthetic Non Hell Area`\n" +
		"[hell quest]\n7777\n[/hell quest]\n" +
		"[dungeon]\n702 -1\n[/dungeon]\n"
	return source
}

// hellBlockedSlotsFixture 造一个可选队伍、可选任务完成状态的夹具。
// questStates: 角色 id（十进制串）→ 任务号 → 状态串（未列出的任务号就是"没有记录"）。
func hellBlockedSlotsFixture(
	t *testing.T,
	mode hellBlockedSlotsMode,
	questStates map[string]map[int64]string,
	partyMembers []uint16,
) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	return hellBlockedSlotsFixtureWithPVF(t, hellBlockedSlotsBridgePVF(), mode, questStates, partyMembers)
}

// hellBlockedSlotsFixtureWithPVF 与 hellBlockedSlotsFixture 完全相同，只是允许换一份
// PVF 源（例如把合成副本的 `[limit party count]` 放大，让两人队伍真的能进图 ——
// 发起者合规 + 队友不合规这条形状必须跑到进图尾段才能验 op27 补发）。
func hellBlockedSlotsFixtureWithPVF(
	t *testing.T,
	source bridgePVFSource,
	mode hellBlockedSlotsMode,
	questStates map[string]map[int64]string,
	partyMembers []uint16,
) (*Service, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, source)
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	characters := []uint16{hellBlockedSlotsTestLeaderID}
	if len(partyMembers) > 0 {
		characters = partyMembers
	}
	for _, characterID := range characters {
		key := strconv.Itoa(int(characterID))
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: key,
			AccountID:   "account-1",
			Level:       20,
			Stats: map[string]int64{
				"fatigue": 100, "town_id": 38, "area_id": 1,
				"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			},
		}); err != nil {
			t.Fatalf("save character %s: %v", key, err)
		}
		record := dnfrepo.QuestRecord{CharacterID: key}
		if states := questStates[key]; len(states) > 0 {
			record.States = make(map[int64]dnfrepo.QuestState, len(states))
			for questID, status := range states {
				record.States[questID] = dnfrepo.QuestState{Status: status}
			}
		}
		if err := repositories.Quest.Save(context.Background(), record); err != nil {
			t.Fatalf("save quest record %s: %v", key, err)
		}
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: strconv.Itoa(int(hellBlockedSlotsTestLeaderID)),
		Slots:       map[string]dnfrepo.ItemStack{"0:1": {ItemID: 1, Count: 7}},
	}); err != nil {
		t.Fatalf("save inventory: %v", err)
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	// 日志文件句柄必须随测试一起收掉：否则 t.TempDir 的清理在 Windows 上会失败。
	t.Cleanup(func() { _ = logger.Close() })
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			hellPartyEntry:     hellPartyEntryVendor,
			hellBlockedSlots:   mode,
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
		connID:                       "hell-blocked-slots-test",
		selectedCharacterID:          hellBlockedSlotsTestLeaderID,
		selectPreviewObjectStateSent: true,
		currentSceneObjectListSent:   true,
	}
	if len(partyMembers) > 1 {
		members := make([]alignedcmd.PartyMemberState, 0, len(partyMembers))
		for _, characterID := range partyMembers {
			members = append(members, alignedcmd.PartyMemberState{UserID: characterID})
		}
		storeRuntimePartyState(session, alignedcmd.PartyState{
			PartyID:    1,
			IsLeader:   true,
			UserID:     hellBlockedSlotsTestLeaderID,
			Members:    members,
			MaxMembers: byte(len(members)),
		})
	}
	bindDungeonSelectorOriginForTestAt(t, service, session, 38, 1, 450, 234)
	return service, session, conn, logger, logPath
}

// hellBlockedSlotsQuestStates 是合成区域那一个任务号（7777）的状态表简写。
func hellBlockedSlotsQuestStates(statusByCharacter map[string]string) map[string]map[int64]string {
	states := make(map[string]map[int64]string, len(statusByCharacter))
	for characterID, status := range statusByCharacter {
		states[characterID] = map[int64]string{hellBlockedSlotsTestQuestID: status}
	}
	return states
}

// hellBlockedSlotsSelectBody 组装一条带深渊标志的真实 op16（body[5] = 1 =
// 厂商 `HellPartyRequestFlag`）。
func hellBlockedSlotsSelectBody(dungeonID uint32) []byte {
	body := make([]byte, dungeoncmd.SelectDungeonRequestSize)
	binary.LittleEndian.PutUint32(body[0:4], dungeonID)
	body[4] = 1
	body[5] = 1
	binary.LittleEndian.PutUint16(body[9:11], 0xffff)
	return body
}

// hellBlockedSlotsExpectedBody 是"无 blocked 形状 + N 个槽位"的**逐字节**期望：
// 37 字节全零体（= 本仓 `currentDungeonContext{}` 的最小体，= 厂商
// `BuildA21EnterSelectDungeon` 在 blocked 为空时的同一形状），
// 追加 blocked 时只在 body[8]（条数）与 body[9+2i]（u16 槽位）上落笔。
func hellBlockedSlotsExpectedBody(blocked []uint16) []byte {
	body := make([]byte, 37+2*len(blocked))
	body[8] = byte(len(blocked))
	for index, slot := range blocked {
		binary.LittleEndian.PutUint16(body[9+2*index:], slot)
	}
	return body
}

// assertHellBlockedSlotsBody 逐字节断言 op27 下发体，并把"哪些字节非零"讲清楚。
func assertHellBlockedSlotsBody(t *testing.T, body []byte, blocked []uint16) {
	t.Helper()
	want := hellBlockedSlotsExpectedBody(blocked)
	if !bytes.Equal(body, want) {
		t.Fatalf("op27 body=%x want=%x (blocked=%v)", body, want, blocked)
	}
	if len(blocked) == 0 {
		if len(body) != 37 {
			t.Fatalf("unblocked op27 body_len=%d want=37", len(body))
		}
		for index, value := range body {
			if value != 0 {
				t.Fatalf("unblocked op27 body[%d]=%#x want=0 (body=%x)", index, value, body)
			}
		}
	}
	if got := int(body[8]); got != len(blocked) {
		t.Fatalf("op27 body[8] blockedCount=%d want=%d", got, len(blocked))
	}
}

// hellEligibilityBlockedEventLines 只取 `game-hell-eligibility-blocked` 那几行，
// 避免把汇总事件（它带的是发起者的 char_id）误当成 blocked 记录。
func hellEligibilityBlockedEventLines(logText string) []string {
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(logText, "\n") {
		if strings.Contains(line, "kind=game-hell-eligibility-blocked") {
			lines = append(lines, line)
		}
	}
	return lines
}

// countHellBlockedSlotsOp27 数一条下行字节流里的 class0/op27（进本选择页）条数。
func countHellBlockedSlotsOp27(t *testing.T, data []byte) int {
	t.Helper()
	count := 0
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentDungeonContextMsgID {
			count++
		}
		data = rest
	}
	return count
}

// findHellBlockedSlotsOp27 在一条连接的下行字节流里找出 class0/op27 那条进本选择页。
func findHellBlockedSlotsOp27(t *testing.T, data []byte) (dnfproto.ChannelPacket, int) {
	t.Helper()
	count := 0
	var found dnfproto.ChannelPacket
	for len(data) > 0 {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentDungeonContextMsgID {
			if count == 0 {
				found = packet
			}
			count++
		}
		data = rest
	}
	if count != 1 {
		t.Fatalf("class0/op27 count=%d want=1", count)
	}
	return found, count
}

// 主回归（单人）：未完成 `[hell quest]` 的角色点「挑战深渊派对」——
// on 档下**该槽位进 blocked 列表**（下发体逐字节可验，= 客户端 UI 的置灰提示），
// 但 blocked 列表**不是准入授权**：发起者本人不合规 ⇒ `{0, 0xAD, 0}` 拒绝、不进图。
// （2026-09-28 修正：本用例原先断言"照样进得去副本"，那正是现场缺陷本身 —— 没做完
// 深渊任务的角色拿到了真实深渊房间。厂商口径见 dungeon_hell_party_entry.go 的
// enforceHellPartyRequesterQuestGate：blocked 投影只进 UI，admission 层另判发起者。）
func TestHellBlockedSlotsSoloUnqualifiedRequesterIsRefusedAndStillProjected(t *testing.T) {
	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "active"}), // 只是接到任务，没有完成
		nil,
	)

	if err := service.handleDungeonSelectUpper(session, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}

	// (a) 客户端收到的是拒绝帧，而且只有这一帧；服务端不建任何进图运行时。
	stream := conn.write.Bytes()
	first, rest := splitGameServerUpperPacket(t, stream)
	if first.Header.Classification != dnfproto.DefaultChannelClassification ||
		first.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) {
		t.Fatalf("first packet after unqualified hell select=%+v want the hell refusal", first.Header)
	}
	if want := []byte{0, currentSelectDungeonMissingPermissionReason, 0}; !bytes.Equal(first.Body, want) {
		t.Fatalf("unqualified hell select refusal body=%x want=%x", first.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("unqualified hell select emitted trailing entry packets: %x", rest)
	}
	assertHellPartyEntryCommittedNothing(t, session)

	// (b) blocked 槽位照旧记录并可随 class0/op27 下发：单人 ⇒ blockedCount=1、slot=0。
	if !service.hellBlockedSlotsPending(session) {
		t.Fatal("the refusal dropped the blocked-slot projection")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "test_hell_solo_unqualified"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{0})

	// (c) 事件日志可 grep（第 5 条要求）。
	logText := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"kind=game-hell-eligibility-blocked",
		"char_id=99",
		"slot=0",
		"missing_quest=7777",
		"area=1",
		"pvf_status=" + hellPVFStatusIDsParsed,
		"mode=on",
		"required_hell_quest_ids=7777",
		"source_section=worldmap_area_hell_quest",
		// 拒绝本身也要留下判据（与逐槽位投影分开记）。
		"kind=game-dungeon-hell-party-entry-blocked",
		"reason=hell_party_quest_required",
		"missing_hell_quest_id=7777",
		"requester_quest_gate=a21_try_prepare_hell_party_requester_only",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("hell eligibility log missing %q:\n%s", want, logText)
		}
	}
	if !strings.Contains(logText, "kind=game-hell-eligibility-evaluated") ||
		!strings.Contains(logText, "blocked_count=1") ||
		!strings.Contains(logText, "roster_slots=0:99") {
		t.Fatalf("hell eligibility summary log missing evidence:\n%s", logText)
	}
	if !strings.Contains(logText, "hell_blocked_slots=0") ||
		!strings.Contains(logText, "hell_blocked_slots_mode=on") {
		t.Fatalf("op27 delivery log missing the blocked slots:\n%s", logText)
	}
}

// 双人：只有一人未完成 ⇒ **只 block 那一槽**（不是整队拒绝），下发体逐字节可验。
func TestHellBlockedSlotsPartyBlocksOnlyTheIneligibleSlot(t *testing.T) {
	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "completed", "100": "active"}),
		[]uint16{hellBlockedSlotsTestLeaderID, hellBlockedSlotsTestMemberID},
	)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(
		hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID))
	if err != nil {
		t.Fatal(err)
	}

	decision := service.evaluateHellPartyDungeonEntry(context.Background(), session, request, true)
	if decision.Intercepted {
		t.Fatalf("two-member party with one ineligible slot was refused as a whole team: %+v", decision)
	}
	if !decision.AllowEntry {
		t.Fatalf("hell select was not allowed through: %+v", decision)
	}
	if len(decision.BlockedSlots) != 1 || decision.BlockedSlots[0] != 1 {
		t.Fatalf("blocked slots=%v want=[1] (only the ineligible member's slot)", decision.BlockedSlots)
	}
	if decision.PvfStatus != hellPVFStatusIDsParsed || decision.AreaID != 1 {
		t.Fatalf("pvf status=%q area=%d want %q/1", decision.PvfStatus, decision.AreaID, hellPVFStatusIDsParsed)
	}

	// 下发体：只多出 blockedCount=1 + slot=1 这 3 个字节。
	if err := service.sendCurrentDungeonContextOp27(session, "test_hell_blocked_slots"); err != nil {
		t.Fatalf("send op27: %v", err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{1})

	logText := readPeerEndpointTestLog(t, logger, logPath)
	blockedLines := hellEligibilityBlockedEventLines(logText)
	if len(blockedLines) != 1 {
		t.Fatalf("blocked events=%d want=1 (only the ineligible member):\n%s", len(blockedLines), logText)
	}
	for _, want := range []string{" char_id=100 ", " slot=1 ", "missing_quest=7777", "requester_char_id=99"} {
		if !strings.Contains(blockedLines[0], want) {
			t.Fatalf("blocked event %q missing %q", blockedLines[0], want)
		}
	}
	if strings.Contains(blockedLines[0], " char_id=99") {
		t.Fatalf("the eligible leader must not be reported as blocked: %s", blockedLines[0])
	}
	if !strings.Contains(logText, "roster_slots=0:99,1:100") {
		t.Fatalf("roster order must be the stable party slot order:\n%s", logText)
	}
}

// 全部完成 ⇒ blocked 为空，下发体与"无 blocked"形状**逐字节一致**（且逐字节等于默认档）。
func TestHellBlockedSlotsAllEligibleBodyIsByteIdenticalToDefault(t *testing.T) {
	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "completed"}),
		nil,
	)
	if err := service.handleDungeonSelectUpper(session, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}
	onBody, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, onBody.Body, nil)
	if !bytes.Equal(onBody.Body, hellBlockedSlotsExpectedBody(nil)) {
		t.Fatalf("on-mode unblocked body=%x want=%x", onBody.Body, hellBlockedSlotsExpectedBody(nil))
	}

	// 默认档（未设置 env = 零值）在同一条发送点上的下发体：必须与上面一模一样。
	offService, offSession, offConn, _, _ := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOff,
		hellBlockedSlotsQuestStates(map[string]string{"99": "completed"}),
		nil,
	)
	if err := offService.sendCurrentDungeonContextOp27(offSession, "test_default_mode_body"); err != nil {
		t.Fatalf("default-mode op27: %v", err)
	}
	offBody, _ := findHellBlockedSlotsOp27(t, offConn.write.Bytes())
	if !bytes.Equal(offBody.Body, onBody.Body) {
		t.Fatalf("default-mode body=%x != on-mode unblocked body=%x", offBody.Body, onBody.Body)
	}
	// 直接组包对照：本仓 `currentDungeonContext{}` 的最小体也是同一串 37 字节。
	minimal, err := (currentDungeonContext{}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(offBody.Body, minimal) {
		t.Fatalf("default-mode body=%x != zero-value context body=%x", offBody.Body, minimal)
	}

	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "kind=game-hell-eligibility-blocked") {
		t.Fatalf("an all-eligible run must not report blocked slots:\n%s", logText)
	}
	if !strings.Contains(logText, "reason=hell_party_all_members_eligible") ||
		!strings.Contains(logText, "blocked_count=0") {
		t.Fatalf("all-eligible summary log missing evidence:\n%s", logText)
	}
}

// 默认档（未设置/空/非法 env）= **逐字节回到今天**：深渊请求仍然走 vendor 档的
// 必回包拒绝（第 57 条的修复），而且这一层一个新包、一条新日志都不加。
func TestHellBlockedSlotsDefaultModeKeepsTodaysRefusalByteForByte(t *testing.T) {
	for _, value := range []string{"", " ", "off", "junk", "true", "1", "yes", "ON?"} {
		if got := readHellBlockedSlotsMode(value).String(); got != "off" {
			t.Fatalf("readHellBlockedSlotsMode(%q)=%q want=off (默认档必须逐字节等于今天)", value, got)
		}
	}
	for _, value := range []string{"on", " ON ", "On"} {
		if got := readHellBlockedSlotsMode(value).String(); got != "on" {
			t.Fatalf("readHellBlockedSlotsMode(%q)=%q want=on", value, got)
		}
	}
	var zero Service
	if zero.hellBlockedSlotsGate() != hellBlockedSlotsOff {
		t.Fatal("zero-value Service must default to off")
	}

	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOff,
		hellBlockedSlotsQuestStates(map[string]string{"99": "active"}),
		nil,
	)
	if err := service.handleDungeonSelectUpper(session, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}
	packet, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	if packet.Header.Classification != dnfproto.DefaultChannelClassification ||
		packet.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) {
		t.Fatalf("default-mode refusal packet=%+v", packet.Header)
	}
	if want := []byte{0, currentSelectDungeonMissingPermissionReason, 0}; !bytes.Equal(packet.Body, want) {
		t.Fatalf("default-mode refusal body=%x want=%x", packet.Body, want)
	}
	if len(rest) != 0 {
		t.Fatalf("default-mode refusal emitted trailing packets: %x", rest)
	}
	if session.hellBlocked.Delivered || len(session.hellBlocked.Slots) != 0 ||
		service.hellBlockedSlotsPending(session) || service.hellBlockedSlotNumbers(session) != nil {
		t.Fatalf("default mode populated the blocked-slot cache: %+v", session.hellBlocked)
	}
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logText, "game-hell-eligibility") {
		t.Fatalf("default mode must not run the per-slot eligibility layer:\n%s", logText)
	}
	if !strings.Contains(logText, "reason=hell_party_quest_required") {
		t.Fatalf("default-mode refusal log lost its pre-change evidence:\n%s", logText)
	}
}

// 区域没有深渊门槛（读不到 `[hell quest]` 段 / `[hell dungeon]` 没写 1）⇒ 不拦，
// 且 `pvf_status` 必须把到底是哪一种写清楚。
func TestHellBlockedSlotsAreasWithoutHellGateAreNotBlocked(t *testing.T) {
	cases := []struct {
		name       string
		dungeonID  uint32
		wantStatus string
	}{
		{name: "no hell quest section", dungeonID: 701, wantStatus: hellPVFStatusSectionAbsent},
		{name: "hell dungeon flag off", dungeonID: 702, wantStatus: hellPVFStatusNotHellDungeon},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, session, conn, logger, logPath := hellBlockedSlotsFixture(
				t, hellBlockedSlotsOn,
				hellBlockedSlotsQuestStates(map[string]string{"99": "active"}), // 任务没做完也不该被 block：这一区域没有门槛
				nil,
			)
			request, err := dungeoncmd.DecodeSelectDungeonRequest(hellBlockedSlotsSelectBody(test.dungeonID))
			if err != nil {
				t.Fatal(err)
			}
			decision := service.evaluateHellPartyDungeonEntry(context.Background(), session, request, true)
			if decision.Intercepted || !decision.AllowEntry {
				t.Fatalf("area without a hell gate was refused: %+v", decision)
			}
			if len(decision.BlockedSlots) != 0 {
				t.Fatalf("area without a hell gate blocked slots=%v", decision.BlockedSlots)
			}
			if decision.PvfStatus != test.wantStatus {
				t.Fatalf("pvf_status=%q want=%q", decision.PvfStatus, test.wantStatus)
			}
			if err := service.sendCurrentDungeonContextOp27(session, "test_no_gate"); err != nil {
				t.Fatal(err)
			}
			op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
			assertHellBlockedSlotsBody(t, op27.Body, nil)
			logText := readPeerEndpointTestLog(t, logger, logPath)
			if !strings.Contains(logText, "pvf_status="+test.wantStatus) {
				t.Fatalf("log must carry pvf_status=%s:\n%s", test.wantStatus, logText)
			}
			if strings.Contains(logText, "kind=game-hell-eligibility-blocked") {
				t.Fatalf("no gate must not block any slot:\n%s", logText)
			}
		})
	}
}

// blocked 槽位已经随某条 op27 下发过之后，不再重复补发（`hellBlockedSlotsPending`）。
func TestHellBlockedSlotsDeliveryIsOncePerEvaluation(t *testing.T) {
	service, session, conn, _, _ := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "active"}),
		nil,
	)
	request, err := dungeoncmd.DecodeSelectDungeonRequest(hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID))
	if err != nil {
		t.Fatal(err)
	}
	service.evaluateHellPartyDungeonEntry(context.Background(), session, request, true)
	if !service.hellBlockedSlotsPending(session) {
		t.Fatal("a fresh evaluation with blocked slots must be pending delivery")
	}
	if err := service.sendCurrentDungeonContextOp27(session, "test_first_delivery"); err != nil {
		t.Fatal(err)
	}
	if service.hellBlockedSlotsPending(session) {
		t.Fatal("blocked slots must not be pending after the op27 delivery")
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{0})
	// 离开选图世界（回城/换角色/结算另选）必须把这份缓存清干净。
	clearHellBlockedSlots(session)
	if service.hellBlockedSlotsPending(session) || service.hellBlockedSlotNumbers(session) != nil {
		t.Fatalf("clearing the select context left the blocked cache behind: %+v", session.hellBlocked)
	}
}

// hellBlockedSlotsPartyPVF 把合成副本的 `[limit party count]` 放大到 4：两人队伍要真的
// 进得去副本（默认 1 ⇒ 队长那条 op16 会被普通进图链的 entry_party_limit 挡下），
// 才能验到进图尾段那条 op27 补发。
func hellBlockedSlotsPartyPVF() bridgePVFSource {
	source := hellBlockedSlotsBridgePVF()
	source["dungeon/test.dgn"] = strings.Replace(
		source["dungeon/test.dgn"], "[limit party count]\n1\n", "[limit party count]\n4\n", 1)
	return source
}

// hellBlockedSlotsPartyFixture 造一支**两名成员都在线**的队伍（leader 99 + member 100），
// 用于跑完整进图链的组队用例。questStatus: 角色 id → 7777 的状态串。
func hellBlockedSlotsPartyFixture(
	t *testing.T,
	questStatus map[string]string,
) (*Service, *gameSession, *bufferConn, *gameSession, *bufferConn, *packetLogger, string) {
	t.Helper()
	table, resolver, monsters := loadBridgeDungeonStaticData(t, hellBlockedSlotsPartyPVF())
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Account.Save(context.Background(), dnfrepo.AccountRecord{AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	for _, characterID := range []uint16{hellBlockedSlotsTestLeaderID, hellBlockedSlotsTestMemberID} {
		key := strconv.Itoa(int(characterID))
		if err := repositories.Character.Save(context.Background(), dnfrepo.CharacterRecord{
			CharacterID: key,
			AccountID:   "account-1",
			Level:       20,
			Stats: map[string]int64{
				"fatigue": 100, "town_id": 38, "area_id": 1,
				"pos_x": 450, "pos_y": 234, "direction": 5, "area_state": 3,
			},
		}); err != nil {
			t.Fatalf("save character %s: %v", key, err)
		}
		if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
			CharacterID: key,
			Slots:       map[string]dnfrepo.ItemStack{"0:1": {ItemID: 1, Count: 7}},
		}); err != nil {
			t.Fatalf("save inventory %s: %v", key, err)
		}
		if err := repositories.Quest.Save(context.Background(), dnfrepo.QuestRecord{
			CharacterID: key,
			States: map[int64]dnfrepo.QuestState{
				hellBlockedSlotsTestQuestID: {Status: questStatus[key]},
			},
		}); err != nil {
			t.Fatalf("save quest record %s: %v", key, err)
		}
	}
	logger, logPath := newPeerEndpointTestLogger(t)
	t.Cleanup(func() { _ = logger.Close() })
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			hellPartyEntry:     hellPartyEntryVendor,
			hellBlockedSlots:   hellBlockedSlotsOn,
		},
		onlinePlayers:       newOnlinePlayerManager(),
		worldMapTable:       table,
		worldMapResolver:    resolver,
		dungeonMonsterTable: monsters,
		dungeonChoice:       func(int) (int, error) { return 0, nil },
		dungeonSeed:         func() (uint32, error) { return 0x12345678, nil },
		repositoryProvider:  func() (dnfrepo.Group, bool) { return repositories, true },
		packetLog:           logger,
	}
	channel := channelcatalog.Channel{ServerID: 1, ID: 42, Type: 11, Name: "ch.42", Port: 10042}
	leaderConn, memberConn := &bufferConn{}, &bufferConn{}
	leader := &gameSession{
		conn: leaderConn, channel: channel, residentChannel: channel,
		connID:                       "hell-blocked-slots-party-leader",
		selectedCharacterID:          hellBlockedSlotsTestLeaderID,
		selectPreviewObjectStateSent: true,
		currentSceneObjectListSent:   true,
	}
	member := &gameSession{
		conn: memberConn, channel: channel, residentChannel: channel,
		connID:                       "hell-blocked-slots-party-member",
		selectedCharacterID:          hellBlockedSlotsTestMemberID,
		selectPreviewObjectStateSent: true,
		currentSceneObjectListSent:   true,
	}
	service.bindGameSessionCharacter(leader, hellBlockedSlotsTestLeaderID)
	service.bindGameSessionCharacter(member, hellBlockedSlotsTestMemberID)
	state := alignedcmd.PartyState{
		PartyID:    1,
		IsLeader:   true,
		UserID:     hellBlockedSlotsTestLeaderID,
		MaxMembers: 2,
		Members: []alignedcmd.PartyMemberState{
			{UserID: hellBlockedSlotsTestLeaderID, UserState: 1, HPPercent: 100, MPPercent: 100},
			{UserID: hellBlockedSlotsTestMemberID, UserState: 1, HPPercent: 100, MPPercent: 100},
		},
	}
	storeRuntimePartyState(leader, state)
	storeRuntimePartyState(member, state)
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: hellBlockedSlotsTestLeaderID, TownID: 38, AreaID: 1, Session: leader})
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{
		CharacterID: hellBlockedSlotsTestMemberID, TownID: 38, AreaID: 1, Session: member})
	bindDungeonSelectorOriginForTestAt(t, service, leader, 38, 1, 450, 234)
	bindDungeonSelectorOriginForTestAt(t, service, member, 38, 1, 450, 234)
	return service, leader, leaderConn, member, memberConn, logger, logPath
}

// 现场真实顺序是"先 op15 开选择页（op27 已随页发过）→ 再 op16 点挑战"，那时进图尾段
// 本来会跳过 op27（`enterSelectDungeonContextSent=true`）。on 档下 blocked 槽位必须
// **补发一次**，否则客户端永远收不到；而"全队合规"这条路径必须一个字节都不多。
//
// 发起者本人合规、队友不合规 —— 这正是补发路径今天唯一可达的形状
// （2026-09-28：发起者本人不合规时已经在闸门上被拒绝，走不到进图尾段）。
func TestHellBlockedSlotsAreRedeliveredWhenTheSelectorPageWasAlreadySent(t *testing.T) {
	service, leader, leaderConn, member, memberConn, logger, logPath := hellBlockedSlotsPartyFixture(
		t, map[string]string{"99": "completed", "100": "active"})
	leader.enterSelectDungeonContextSent = true // 选择页已经在 op15 那一步发过
	// 全员读条屏障按既有组队用例的口径直接放行：本用例只验 op27 补发。
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	t.Cleanup(func() { partyDungeonLoadingBarrierDispatch = restoreDispatch })

	if err := service.handleDungeonSelectUpper(leader, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}
	stream := leaderConn.write.Bytes()
	previewRemoval, rest := splitGameServerUpperPacket(t, stream)
	if previewRemoval.Header.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) {
		t.Fatalf("first packet after hell select=%+v want the preview removal", previewRemoval.Header)
	}
	selectAck, _ := splitGameServerUpperPacket(t, rest)
	if selectAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectDungeon) ||
		!bytes.Equal(selectAck.Body, upperSuccessBody(buildSelectDungeonBodyForCharacter(hellBlockedSlotsTestLeaderID))) {
		t.Fatalf("hell select ACK=%+v body=%x (must stay the ordinary success ack)", selectAck.Header, selectAck.Body)
	}
	op27, count := findHellBlockedSlotsOp27(t, stream)
	if count != 1 {
		t.Fatalf("class0/op27 count=%d want=1 (the blocked slots must be re-delivered exactly once)", count)
	}
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{1})

	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "source=select_dungeon_after_pre_actor_binding_hell_blocked_slots") {
		t.Fatalf("re-delivery must be logged with its own source:\n%s", logText)
	}
	blockedLines := hellEligibilityBlockedEventLines(logText)
	if len(blockedLines) == 0 {
		t.Fatalf("the ineligible member was never projected as blocked:\n%s", logText)
	}
	for _, line := range blockedLines {
		if !strings.Contains(line, " char_id=100 ") || strings.Contains(line, " char_id=99 ") {
			t.Fatalf("only the ineligible member may be blocked: %s", line)
		}
	}
	// 队长带队友进图时那条**内部扇出**不是发起者：队友会话照旧只做逐槽位投影
	// （下面它自己那条 op27 里也带 blocked 槽位），绝不会因为队友自己的任务状态
	// 被当成发起者拒绝、把整队进图从中间打断。
	if strings.Contains(logText, "kind=game-dungeon-hell-party-entry-blocked") {
		t.Fatalf("the leader-driven member fan-out was gated as if it were the requester:\n%s", logText)
	}
	// 队友连接上有两条 op27：进场预检那条（37B，无 blocked）与进图尾段那条（39B，
	// blocked=1）。blocked 槽位必须出现在后者 —— 队友自己那条 op27 也要带上，
	// 否则队友客户端上的「挑战深渊派对」不会被置灰。
	memberBody := hellBlockedSlotsExpectedBody([]uint16{1})
	memberBlockedOp27 := 0
	for data := memberConn.write.Bytes(); len(data) > 0; {
		packet, rest := splitGameServerUpperPacket(t, data)
		if packet.Header.Classification == 0 && packet.Header.MsgID == currentDungeonContextMsgID &&
			bytes.Equal(packet.Body, memberBody) {
			memberBlockedOp27++
		}
		data = rest
	}
	if memberBlockedOp27 != 1 {
		t.Fatalf("member op27 frames carrying blocked slot 1 = %d want=1", memberBlockedOp27)
	}
	member.dungeon.mu.Lock()
	memberRuntime := member.dungeon.runtime
	member.dungeon.mu.Unlock()
	if memberRuntime == nil || memberRuntime.Dungeon.ID != hellBlockedSlotsTestDungeonID {
		t.Fatalf("the member did not complete its side of the party entry: %+v", memberRuntime)
	}
}

// 对照组：全队合规 ⇒ 进图尾段仍然**跳过** op27（逐字节等于改动前那条路径）。
func TestHellBlockedSlotsAllEligibleKeepsTheDuplicateOp27Skip(t *testing.T) {
	service, session, conn, _, _ := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		hellBlockedSlotsQuestStates(map[string]string{"99": "completed"}),
		nil,
	)
	session.enterSelectDungeonContextSent = true

	if err := service.handleDungeonSelectUpper(session, hellBlockedSlotsSelectBody(hellBlockedSlotsTestDungeonID)); err != nil {
		t.Fatalf("handle hell party select: %v", err)
	}
	if got := countHellBlockedSlotsOp27(t, conn.write.Bytes()); got != 0 {
		t.Fatalf("class0/op27 count=%d want=0 when every slot is eligible", got)
	}
	session.dungeon.mu.Lock()
	runtime := session.dungeon.runtime
	session.dungeon.mu.Unlock()
	if runtime == nil {
		t.Fatal("the all-eligible hell select must still enter the dungeon")
	}
}

// 真档 PVF 用例（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：`[hell quest]` 的任务号**只能**
// 从线上同源 Script.pvf 的 worldmap area 读出来，绝不硬编码。
func TestRealPVFHellQuestEligibilityReadsTheRealWorldMapSection(t *testing.T) {
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

	// 找第一个"真是深渊区域 + 真声明了 `[hell quest]` 任务号"的真档 area。
	var realArea worldmap.Area
	var realDungeonID int64
	for _, area := range table.Areas() {
		if area.HellDungeon.Value != 1 || !area.HasSection("hell quest") || len(area.HellQuestIDs) == 0 {
			continue
		}
		for _, dungeon := range area.Dungeons {
			if dungeon.DungeonID <= 0 {
				continue
			}
			realArea, realDungeonID = area, dungeon.DungeonID
			break
		}
		if realDungeonID != 0 {
			break
		}
	}
	if realDungeonID == 0 {
		t.Fatal("real Script.pvf has no hell area with a [hell quest] list and a dungeon")
	}
	// 生产查找路径（op16 的 dungeon id → area）必须落回同一个区域。
	byDungeon, found := table.FindAreaByDungeon(realDungeonID)
	if !found || byDungeon.ID != realArea.ID {
		t.Fatalf("FindAreaByDungeon(%d)=area %d found=%v want area %d", realDungeonID, byDungeon.ID, found, realArea.ID)
	}
	requirement := resolveHellQuestRequirement(byDungeon, found)
	if !requirement.Gate || requirement.PvfStatus != hellPVFStatusIDsParsed {
		t.Fatalf("real area %d requirement=%+v want a parsed hell-quest gate", realArea.ID, requirement)
	}
	if len(requirement.RequiredQuestIDs) != len(realArea.HellQuestIDs) {
		t.Fatalf("real required quests=%v want the document's %v",
			requirement.RequiredQuestIDs, realArea.HellQuestIDs)
	}
	for index, questID := range realArea.HellQuestIDs {
		if requirement.RequiredQuestIDs[index] != questID {
			t.Fatalf("real required quests=%v want the document's %v",
				requirement.RequiredQuestIDs, realArea.HellQuestIDs)
		}
	}

	// 真档任务号 + 真档完成状态 ⇒ 只 block 那个**没做完真档任务**的槽位，
	// 下发体逐字节可验。任务号直接取真档 area 文档里的那一个，硬编码任何数字都会在这里露馅。
	missingQuest := requirement.RequiredQuestIDs[0]
	service, session, conn, logger, logPath := hellBlockedSlotsFixture(
		t, hellBlockedSlotsOn,
		map[string]map[int64]string{
			"99":  {missingQuest: "completed"},
			"100": {missingQuest: "active"},
		},
		[]uint16{hellBlockedSlotsTestLeaderID, hellBlockedSlotsTestMemberID},
	)
	// 把**真档** worldmap 表接进生产判定入口（`evaluateHellPartyDungeonEntry` ->
	// `s.dungeonWorldMap()` -> `FindAreaByDungeon`），证明任务号与区域声明都来自真档
	// Script.pvf，而不是夹具合成的 `[hell quest] 7777`。
	resolver, err := worldmap.NewResolver(table)
	if err != nil {
		t.Fatalf("build real worldmap resolver: %v", err)
	}
	service.worldMapMu.Lock()
	service.worldMapTable = table
	service.worldMapResolver = resolver
	service.worldMapMu.Unlock()
	request, err := dungeoncmd.DecodeSelectDungeonRequest(
		hellBlockedSlotsSelectBody(uint32(realDungeonID)))
	if err != nil {
		t.Fatal(err)
	}
	decision := service.evaluateHellPartyDungeonEntry(context.Background(), session, request, true)
	if decision.Intercepted || !decision.AllowEntry {
		t.Fatalf("real-PVF hell select was refused: %+v", decision)
	}
	if decision.AreaID != realArea.ID || decision.PvfStatus != hellPVFStatusIDsParsed {
		t.Fatalf("real-PVF area=%d pvf_status=%q want %d/%q",
			decision.AreaID, decision.PvfStatus, realArea.ID, hellPVFStatusIDsParsed)
	}
	if len(decision.BlockedSlots) != 1 || decision.BlockedSlots[0] != 1 {
		t.Fatalf("real-PVF blocked slots=%v want=[1] (only the member that did not clear quest %d)",
			decision.BlockedSlots, missingQuest)
	}
	if err := service.sendCurrentDungeonContextOp27(session, "test_real_pvf_hell_quest"); err != nil {
		t.Fatal(err)
	}
	op27, _ := findHellBlockedSlotsOp27(t, conn.write.Bytes())
	assertHellBlockedSlotsBody(t, op27.Body, []uint16{1})
	t.Logf("real PVF hell gate: area=%d %q dungeon=%d quest=%d body=%x",
		realArea.ID, realArea.Name, realDungeonID, missingQuest, op27.Body)
	logText := readPeerEndpointTestLog(t, logger, logPath)
	if !strings.Contains(logText, "pvf_status="+hellPVFStatusIDsParsed) ||
		!strings.Contains(logText, "required_hell_quest_ids="+strconv.FormatInt(missingQuest, 10)) {
		t.Fatalf("real-PVF eligibility log missing evidence:\n%s", logText)
	}
	blockedLines := hellEligibilityBlockedEventLines(logText)
	if len(blockedLines) != 1 || !strings.Contains(blockedLines[0], "missing_quest="+strconv.FormatInt(missingQuest, 10)) {
		t.Fatalf("real-PVF blocked event missing evidence:\n%s", logText)
	}
}

// 真档 PVF 用例（`DNFBRIDGE_REAL_PVF_SMOKE` 守卫）：真档里确实存在"是深渊区域但
// **没有** `[hell quest]` 段"与"有 `[hell quest]` 段但不是深渊区域"两种区域，
// 它们都必须被判成**没有门槛**（不拦），并各自带上准确的 pvf_status。
func TestRealPVFHellQuestSectionAbsentAreasHaveNoGate(t *testing.T) {
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
	var absent, notHell []worldmap.Area
	for _, area := range table.Areas() {
		switch {
		case area.HellDungeon.Value == 1 && !area.HasSection("hell quest"):
			absent = append(absent, area)
		case area.HellDungeon.Value != 1 && area.HasSection("hell quest"):
			notHell = append(notHell, area)
		}
	}
	if len(absent) == 0 || len(notHell) == 0 {
		t.Fatalf("real Script.pvf evidence changed: hell-without-section=%d non-hell-with-section=%d",
			len(absent), len(notHell))
	}
	for _, area := range absent {
		requirement := resolveHellQuestRequirement(area, true)
		if requirement.Gate || requirement.PvfStatus != hellPVFStatusSectionAbsent {
			t.Fatalf("real area %d (%q) requirement=%+v want no gate with %q",
				area.ID, area.Name, requirement, hellPVFStatusSectionAbsent)
		}
	}
	for _, area := range notHell {
		requirement := resolveHellQuestRequirement(area, true)
		if requirement.Gate || requirement.PvfStatus != hellPVFStatusNotHellDungeon {
			t.Fatalf("real area %d (%q) requirement=%+v want no gate with %q",
				area.ID, area.Name, requirement, hellPVFStatusNotHellDungeon)
		}
	}
	t.Logf("real PVF no-gate areas: hell_without_section=%d non_hell_with_section=%d", len(absent), len(notHell))
}
