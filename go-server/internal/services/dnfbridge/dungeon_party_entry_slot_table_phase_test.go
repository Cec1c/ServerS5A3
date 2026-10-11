package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 本文件钉住"进图相位"上的三个开关（全部**默认 = 逐字节今天**）：
//
//	① `DNFBRIDGE_PARTY_DUNGEON_ENTRY_SLOT_TABLE`（H1）
//	   start-map 提交里同一个 actor 的两条 kind0 槽位表（落位名册帧 + 厂商形状房间槽位表）；
//	② `DNFBRIDGE_PARTY_DUNGEON_ENTRY_FOLLOWER_FRAME`（H2）
//	   队员"跟随者选择器转换"那一批里那条 op9 名册帧（在 op28/op29 **之前**，队长没有）；
//	③ `DNFBRIDGE_PARTY_DUNGEON_POST_LOADING_ROSTER`（H3）
//	   读条尾段之后给**本人**补推一次 op153 实时信息 + 自己那条 op9。
//
// 现场证据、排除记录与安全阀见各自实现处的文件头注释
// （dungeon_party_room_slot_table.go / game_enter_select_dungeon.go /
// party_dungeon_loading_barrier.go）。
//
// 进图这一次的实际相位（同步进场夹具逐包实测，`char 99` / `char 100` = 队长 / 队员）：
//
//	队员：op2 mode0 → op2 mode1 → op3 → **op9 名册帧（跟随者选择器转换）** → op28 → op800
//	      → op29 → op36 fatigue → **op9 厂商房间槽位表** → 两人 op37 → 落位（**op9 名册帧**
//	      → op120 → op3）→ op37 主状态 → op30 → op19 → op120 → 队友 actor 补发
//	队长：同一条，只是没有"跟随者选择器转换"那一条 op9。
//
// ⇒ 队员在进图全程会收到**自己 actor 的三条** kind0 槽位表；厂商每个 start-map 提交只发一条。

// dungeonEntryPhaseScenario 是一次进图夹具上要打开的开关组合。
type dungeonEntryPhaseScenario struct {
	EntrySlotTable dungeonPartyEntrySlotTableMode
	RoomSlotTable  dungeonPartyRoomSlotTableMode
	FollowerFrame  partyDungeonEntryFollowerFrameMode
	// PostLoadingRoster 打开 H3（`DNFBRIDGE_PARTY_DUNGEON_POST_LOADING_ROSTER`）。
	PostLoadingRoster bool
}

// dungeonEntrySlotTableScenario 走一次完整的两人才进图：
// op16 → op28 屏障 → op800/op29 → op36 fatigue → 房间槽位表 → 两人 op37 → 读条屏障放行
// （落位 + op37 尾段 + 队友 actor 补发）。返回全局包序与包日志。
func dungeonEntrySlotTableScenario(
	t *testing.T,
	entry dungeonPartyEntrySlotTableMode,
	room dungeonPartyRoomSlotTableMode,
) ([]partyDungeonSyncStep, string) {
	t.Helper()
	return dungeonEntryPhaseScenarioRun(t, dungeonEntryPhaseScenario{
		EntrySlotTable: entry,
		RoomSlotTable:  room,
	})
}

func dungeonEntryPhaseScenarioRun(
	t *testing.T,
	setup dungeonEntryPhaseScenario,
) ([]partyDungeonSyncStep, string) {
	t.Helper()
	// 放行派发改同步：否则断言会与放行 goroutine 竞态（口径与
	t.Setenv(partySelectorPreserveActorEnv, "off") // these scenarios audit the legacy conversion switch
	// TestPartyDungeonSharedLoadingBarrierReleasesBothMembersAfterTheLastOp37 相同）。
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()
	if setup.PostLoadingRoster {
		t.Setenv(partyDungeonPostLoadingRosterEnv, "on")
	} else {
		t.Setenv(partyDungeonPostLoadingRosterEnv, "off")
	}

	service, leader, follower, _, _, trace := partyDungeonSyncFixture(t)
	service.options.partyDungeonEntrySlotTable = setup.EntrySlotTable
	service.options.dungeonPartyRoomSlotTable = setup.RoomSlotTable
	service.options.partyDungeonEntryFollowerFrame = setup.FollowerFrame
	runPartyDungeonSyncEntry(t, service, leader)
	for _, session := range []*gameSession{leader, follower} {
		if err := service.sendFinishLoadingStatus(session, make([]byte, currentFinishLoadingLegacyRequestBodySize)); err != nil {
			t.Fatalf("finish loading char %d: %v", session.selectedCharacterID, err)
		}
	}
	steps := trace.orderedSteps(t)
	if len(steps) == 0 {
		t.Fatal("dungeon entry wrote no packet")
	}
	return steps, testPacketLogText(t, service)
}

// dungeonEntrySelfKind0Indexes 返回"发给该角色**自己**"的 class0/op9 kind0 槽位表在全局
// 包序里的下标（两种形状都算：落位/转换里的名册帧与厂商形状房间槽位表）。队友 actor 投影
// 那几条 op9 描述的是**队友**的 object key，因此不在本列表里（它们由 4 个既有用例单独钉住）。
func dungeonEntrySelfKind0Indexes(steps []partyDungeonSyncStep, charID uint16) []int {
	wantKey := currentSceneActorObjectKey(charID)
	indexes := make([]int, 0, 4)
	for index, step := range steps {
		if step.CharID != charID || step.Classification != 0 ||
			step.MsgID != uint16(dnfenum.CmdPacketRecoverStamina) {
			continue
		}
		body := step.Body
		if len(body) < 12 || body[9] != currentSceneOp9ActorDisplayKind {
			continue
		}
		if binary.LittleEndian.Uint16(body[4:6]) != wantKey {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

// dungeonEntryPreOp29SelfKind0Indexes 只保留"落在 start-map（op29）提交之前"的自己槽位表：
// 也就是队员那条跟随者选择器转换（队长没有这一条）。
func dungeonEntryPreOp29SelfKind0Indexes(steps []partyDungeonSyncStep, charID uint16) []int {
	op29 := firstPartyDungeonSyncStep(steps, charID, currentDungeonStartNotification, nil)
	if op29 < 0 {
		return nil
	}
	pre := make([]int, 0, 1)
	for _, index := range dungeonEntrySelfKind0Indexes(steps, charID) {
		if index < op29 {
			pre = append(pre, index)
		}
	}
	return pre
}

// dungeonEntryCommitWindowIndexes 只保留"落在 start-map（op29）提交之后"的槽位表下标：
// 跟随者选择器转换那一条更早，不属于 H1 的作用域。
func dungeonEntryCommitWindowIndexes(steps []partyDungeonSyncStep, charID uint16) []int {
	op29 := firstPartyDungeonSyncStep(steps, charID, currentDungeonStartNotification, nil)
	if op29 < 0 {
		return nil
	}
	window := make([]int, 0, 2)
	for _, index := range dungeonEntrySelfKind0Indexes(steps, charID) {
		if index > op29 {
			window = append(window, index)
		}
	}
	return window
}

// dungeonEntryLastStepIndex 返回某个角色收到的最后一个指定 msg_id 的下标（找不到 = -1）。
func dungeonEntryLastStepIndex(steps []partyDungeonSyncStep, charID uint16, msgID uint16) int {
	found := -1
	for index, step := range steps {
		if step.CharID == charID && step.MsgID == msgID {
			found = index
		}
	}
	return found
}

// dungeonEntryCharLocalIndex 把全局包序下标换算成"该角色自己的第几个包"。
func dungeonEntryCharLocalIndex(steps []partyDungeonSyncStep, charID uint16, globalIndex int) int {
	local := 0
	for index := 0; index < globalIndex && index < len(steps); index++ {
		if steps[index].CharID == charID {
			local++
		}
	}
	return local
}

// dungeonEntryFrameKey 是"按会话顺序排列的一个上行包"的可比较形式。
type dungeonEntryFrameKey struct {
	class byte
	msgID uint16
	body  string
}

func dungeonEntryFrameKeys(steps []partyDungeonSyncStep, charID uint16) []dungeonEntryFrameKey {
	keys := make([]dungeonEntryFrameKey, 0, len(steps))
	for _, step := range steps {
		if step.CharID != charID {
			continue
		}
		keys = append(keys, dungeonEntryFrameKey{
			class: step.Classification,
			msgID: step.MsgID,
			body:  string(step.Body),
		})
	}
	return keys
}

// dungeonEntryRelayEndpointBodyMsgID 是唯一"body 按进程变化"的入场帧：op11（队友端点
// remotes）里带着本进程分配的 UDP 中继端口。用例先用 off-vs-off 自证这一点（除它之外
// 逐字节一致），再把它排除在 off-vs-on 的逐字节比较之外。
const dungeonEntryRelayEndpointBodyMsgID = uint16(dnfenum.CmdPacketResponsePeer)

// dungeonEntryWireDiffs 比较两段包序。`dropped` >= 0 表示 want 比 got 多出下标为 dropped
// 的那一帧（其余按下标对齐比较）；`dungeonEntryRelayEndpointBodyMsgID` 的帧只比 msg_id/class。
func dungeonEntryWireDiffs(
	label string,
	want []dungeonEntryFrameKey,
	got []dungeonEntryFrameKey,
	dropped int,
) []string {
	diffs := make([]string, 0, 4)
	wantCount := len(want)
	if dropped >= 0 {
		wantCount--
	}
	if len(got) != wantCount {
		return append(diffs, fmt.Sprintf("%s: frame count want=%d got=%d (dropped=%d)", label, wantCount, len(got), dropped))
	}
	for index := range got {
		expected := want[index]
		if dropped >= 0 && index >= dropped {
			expected = want[index+1]
		}
		if got[index].class != expected.class || got[index].msgID != expected.msgID {
			diffs = append(diffs, fmt.Sprintf("%s: frame[%d] header got=class%d/msg%d want=class%d/msg%d",
				label, index, got[index].class, got[index].msgID, expected.class, expected.msgID))
			continue
		}
		if got[index].msgID == dungeonEntryRelayEndpointBodyMsgID {
			continue
		}
		if got[index].body != expected.body {
			diffs = append(diffs, fmt.Sprintf("%s: frame[%d] msg=%d body changed\n  want=% X\n  got =% X",
				label, index, got[index].msgID, []byte(expected.body), []byte(got[index].body)))
		}
	}
	return diffs
}

// dungeonEntryInsertedPairIndex 报告"got = want + 恰好两帧插入"时的插入下标（否则 -1 + 差异）。
// H3（读条后名册重推）就是这种形状：既有包序一字节不改，只多出 op153 + 自己那条 op9。
func dungeonEntryInsertedPairIndex(label string, want, got []dungeonEntryFrameKey) (int, []string) {
	if len(got) != len(want)+2 {
		return -1, []string{fmt.Sprintf("%s: frame count want=%d got=%d (need exactly two inserted frames)",
			label, len(want), len(got))}
	}
	for insert := 0; insert <= len(want); insert++ {
		matched := true
		for index := 0; index < len(want) && matched; index++ {
			gotIndex := index
			if index >= insert {
				gotIndex = index + 2
			}
			expected, actual := want[index], got[gotIndex]
			if expected.class != actual.class || expected.msgID != actual.msgID {
				matched = false
				break
			}
			if actual.msgID == dungeonEntryRelayEndpointBodyMsgID {
				continue
			}
			if expected.body != actual.body {
				matched = false
			}
		}
		if matched {
			return insert, nil
		}
	}
	return -1, []string{fmt.Sprintf("%s: no single inserted pair explains the extra frames", label)}
}

// TestDungeonPartyEntrySlotTableSwitchDefaultsAndReadback 钉住 H1 开关的默认档与解析：
// 空/未设置/非法值都是 `current`（= 今天的行为），只有显式 vendor 取值才让位。
func TestDungeonPartyEntrySlotTableSwitchDefaultsAndReadback(t *testing.T) {
	if got := (&Service{}).partyDungeonEntrySlotTable(); got != dungeonPartyEntrySlotTableCurrent {
		t.Fatalf("zero-value service selected mode %v", got)
	}
	if got := (&Service{}).partyDungeonEntrySlotTable().String(); got != dungeonPartyEntrySlotTableValueCurrent {
		t.Fatalf("zero-value service log label=%q want=%q", got, dungeonPartyEntrySlotTableValueCurrent)
	}
	if dungeonPartyEntrySlotTableVendorOnly.String() != dungeonPartyEntrySlotTableValueVendorOnly {
		t.Fatalf("vendor_only log label=%q", dungeonPartyEntrySlotTableVendorOnly.String())
	}
	if dungeonPartyEntrySlotTableCurrent.dropsLegacyPlacementFrame() {
		t.Fatal("current mode must keep the placement party frame")
	}
	if !dungeonPartyEntrySlotTableVendorOnly.dropsLegacyPlacementFrame() {
		t.Fatal("vendor_only mode must drop the duplicated placement party frame")
	}
	for _, value := range []string{"", "   ", "current", "CURRENT", "off", "legacy", "junk"} {
		if got := readDungeonPartyEntrySlotTableMode(value); got != dungeonPartyEntrySlotTableCurrent {
			t.Fatalf("readDungeonPartyEntrySlotTableMode(%q)=%v want current", value, got)
		}
	}
	for _, value := range []string{"vendor_only", " VENDOR_ONLY ", "vendor", "single"} {
		if got := readDungeonPartyEntrySlotTableMode(value); got != dungeonPartyEntrySlotTableVendorOnly {
			t.Fatalf("readDungeonPartyEntrySlotTableMode(%q)=%v want vendor_only", value, got)
		}
	}
}

// TestDungeonPartyEntrySlotTableCurrentKeepsBothKind0Tables 钉住 H1 的**默认档 = 今天**：
// start-map 提交之后，每个成员都会收到两条描述自己的 kind0 槽位表 ——
// 先是 op36 之后那条厂商形状房间槽位表，再是落位里那条名册帧（op120/op3 之前、
// op37 主状态之前）。两条描述的是同一个 actor、同一份权威槽位表。
func TestDungeonPartyEntrySlotTableCurrentKeepsBothKind0Tables(t *testing.T) {
	steps, log := dungeonEntrySlotTableScenario(
		t,
		dungeonPartyEntrySlotTableCurrent,
		dungeonPartyRoomSlotTableVendor,
	)
	for _, charID := range []uint16{99, 100} {
		window := dungeonEntryCommitWindowIndexes(steps, charID)
		if len(window) != 2 {
			t.Fatalf("char %d start-map commit kind0 slot tables=%d want=2 (vendor room slot table + placement party record)",
				charID, len(window))
		}
		roomIndex, nameIndex := window[0], window[1]
		roomBody := steps[roomIndex].Body
		nameBody := steps[nameIndex].Body
		if !isDungeonRoomSlotTableBody(roomBody) {
			t.Fatalf("char %d first commit table is not the vendor room slot table: % X", charID, roomBody)
		}
		if isDungeonRoomSlotTableBody(nameBody) {
			t.Fatalf("char %d second commit table must be the placement party record: % X", charID, nameBody)
		}
		// 相位：房间槽位表在 op36 fatigue 之后；名册帧在落位的 op120/op3 之前。
		op36 := dungeonEntryLastStepIndex(steps, charID, currentFatigueMsgID)
		placement := firstPartyDungeonSyncStep(steps, charID, uint16(dnfenum.CmdPacketRequestBlacklist), func(step partyDungeonSyncStep) bool {
			return len(step.Body) == 2
		})
		op37 := firstPartyDungeonSyncStep(steps, charID, uint16(dnfenum.CmdPacketFinishLoading), func(step partyDungeonSyncStep) bool {
			return step.Classification == 0
		})
		if op36 < 0 || placement < 0 || op37 < 0 {
			t.Fatalf("char %d entry phase anchors missing: op36=%d placement=%d op37=%d", charID, op36, placement, op37)
		}
		if roomIndex <= op36 {
			t.Fatalf("char %d vendor room slot table index=%d must follow op36 index=%d", charID, roomIndex, op36)
		}
		if nameIndex >= placement || nameIndex >= op37 {
			t.Fatalf("char %d placement party record index=%d must precede op120 index=%d and op37 index=%d",
				charID, nameIndex, placement, op37)
		}
		// 两条写的是同一份权威槽位表：行号、uid 与队长槽位必须一致（让位不丢名册信息）。
		roomRows, roomLeaderSlot := parseOp9PartyFrame(t, roomBody, true)
		nameRows, nameLeaderSlot := parseOp9PartyFrame(t, nameBody, true)
		if len(roomRows) != 2 || len(nameRows) != 2 {
			t.Fatalf("char %d row counts room=%d name=%d want 2/2", charID, len(roomRows), len(nameRows))
		}
		for index := range roomRows {
			if roomRows[index].slot != nameRows[index].slot || roomRows[index].uid != nameRows[index].uid {
				t.Fatalf("char %d row[%d] room=%+v name=%+v must describe the same roster",
					charID, index, roomRows[index], nameRows[index])
			}
		}
		if roomLeaderSlot != nameLeaderSlot {
			t.Fatalf("char %d leader slot room=%d name=%d", charID, roomLeaderSlot, nameLeaderSlot)
		}
	}
	if strings.Contains(log, "game-party-dungeon-entry-slot-table-legacy-frame-skipped") {
		t.Fatalf("default mode must not skip anything:\n%s", log)
	}
}

// TestDungeonPartyEntrySlotTableVendorOnlyDropsOnlyThePlacementFrame 是 H1 on 档的主回归：
// start-map 提交里每个成员只剩一条 kind0 槽位表（厂商形状、厂商相位），而**其余每一个包
// 逐字节、逐顺序不变** —— on 档包序恰好等于 off 档删掉那一条名册帧。
func TestDungeonPartyEntrySlotTableVendorOnlyDropsOnlyThePlacementFrame(t *testing.T) {
	offSteps, offLog := dungeonEntrySlotTableScenario(
		t,
		dungeonPartyEntrySlotTableCurrent,
		dungeonPartyRoomSlotTableVendor,
	)
	offAgainSteps, _ := dungeonEntrySlotTableScenario(
		t,
		dungeonPartyEntrySlotTableCurrent,
		dungeonPartyRoomSlotTableVendor,
	)
	onSteps, onLog := dungeonEntrySlotTableScenario(
		t,
		dungeonPartyEntrySlotTableVendorOnly,
		dungeonPartyRoomSlotTableVendor,
	)
	for _, charID := range []uint16{99, 100} {
		off := dungeonEntryFrameKeys(offSteps, charID)
		offAgain := dungeonEntryFrameKeys(offAgainSteps, charID)
		on := dungeonEntryFrameKeys(onSteps, charID)

		// 自证：两次 off 之间唯一的差异只在 op11 的 body（本进程分配的 UDP 中继端口），
		// 其余帧逐字节一致 ⇒ 上面对 on 档的"只少一帧"结论不是被随机性掩盖的。
		if diffs := dungeonEntryWireDiffs(fmt.Sprintf("char %d off-vs-off", charID), off, offAgain, -1); len(diffs) != 0 {
			t.Fatalf("two identical off-mode runs differ outside msg %d:\n%s",
				dungeonEntryRelayEndpointBodyMsgID, strings.Join(diffs, "\n"))
		}

		offWindow := dungeonEntryCommitWindowIndexes(offSteps, charID)
		onWindow := dungeonEntryCommitWindowIndexes(onSteps, charID)
		if len(offWindow) != 2 || len(onWindow) != 1 {
			t.Fatalf("char %d start-map commit kind0 slot tables off=%d on=%d want 2/1",
				charID, len(offWindow), len(onWindow))
		}
		if !isDungeonRoomSlotTableBody(onSteps[onWindow[0]].Body) {
			t.Fatalf("char %d on-mode survivor must be the vendor room slot table: % X",
				charID, onSteps[onWindow[0]].Body)
		}
		if string(onSteps[onWindow[0]].Body) != string(offSteps[offWindow[0]].Body) {
			t.Fatalf("char %d vendor room slot table changed bytes\n off=% X\n on =% X",
				charID, offSteps[offWindow[0]].Body, onSteps[onWindow[0]].Body)
		}
		// off 的完整包序 = on + 恰好那一条被让位的名册帧。
		dropped := dungeonEntryCharLocalIndex(offSteps, charID, offWindow[1])
		if dropped < 0 || dropped >= len(off) || off[dropped].msgID != uint16(dnfenum.CmdPacketRecoverStamina) {
			t.Fatalf("char %d dropped frame index=%d is not the placement party record", charID, dropped)
		}
		if diffs := dungeonEntryWireDiffs(
			fmt.Sprintf("char %d off-vs-on", charID),
			off,
			on,
			dropped,
		); len(diffs) != 0 {
			t.Fatalf("on mode changed more than the duplicated placement party record:\n%s",
				strings.Join(diffs, "\n"))
		}
	}
	if strings.Contains(offLog, "game-party-dungeon-entry-slot-table-legacy-frame-skipped") {
		t.Fatalf("off mode must not log the skip event:\n%s", offLog)
	}
	if got := strings.Count(onLog, "game-party-dungeon-entry-slot-table-legacy-frame-skipped"); got != 2 {
		t.Fatalf("on mode skip events=%d want=2 (one per member):\n%s", got, onLog)
	}
	for _, want := range []string{
		"game-party-dungeon-entry-slot-table-legacy-frame-skipped",
		"mode=vendor_only",
		"reason=vendor_sends_exactly_one_kind0_slot_table_per_start_map_commit",
		"keeper_source=party_start_room_after_op29_actors",
	} {
		if !strings.Contains(onLog, want) {
			t.Fatalf("on-mode log missing %q:\n%s", want, onLog)
		}
	}
}

// TestDungeonPartyEntrySlotTableVendorOnlyWithoutKeeperKeepsLegacyFrame 是 H1 的安全阀：
// 本次提交**没有**发出厂商形状房间槽位表时（`…ROOM_SLOT_TABLE=once` 或 `legacy`、单人队伍、
// 角色记录缺失），落位里那条名册帧必须照旧发出 —— 否则这名成员会一条槽位表都拿不到。
func TestDungeonPartyEntrySlotTableVendorOnlyWithoutKeeperKeepsLegacyFrame(t *testing.T) {
	for _, room := range []dungeonPartyRoomSlotTableMode{
		dungeonPartyRoomSlotTableOnce,
		dungeonPartyRoomSlotTableLegacy,
	} {
		steps, log := dungeonEntrySlotTableScenario(
			t,
			dungeonPartyEntrySlotTableVendorOnly,
			room,
		)
		for _, charID := range []uint16{99, 100} {
			window := dungeonEntryCommitWindowIndexes(steps, charID)
			if len(window) != 1 {
				t.Fatalf("room=%v char %d start-map commit kind0 slot tables=%d want=1",
					room, charID, len(window))
			}
			if isDungeonRoomSlotTableBody(steps[window[0]].Body) {
				t.Fatalf("room=%v char %d survivor must be the placement party record when no room slot table was sent: % X",
					room, charID, steps[window[0]].Body)
			}
		}
		if strings.Contains(log, "game-party-dungeon-entry-slot-table-legacy-frame-skipped") {
			t.Fatalf("room=%v the safety gate must not fire without a replacement table:\n%s", room, log)
		}
		if !strings.Contains(log, "game-party-dungeon-room-slot-table-skipped") {
			t.Fatalf("room=%v expected the room slot table to be suppressed by its own switch:\n%s", room, log)
		}
	}
}

// TestDungeonPartyEntryFollowerFrameSwitchDefaultsAndReadback 钉住 H2 开关的默认档与解析。
func TestDungeonPartyEntryFollowerFrameSwitchDefaultsAndReadback(t *testing.T) {
	if got := (&Service{}).partyDungeonEntryFollowerFrame(); got != partyDungeonEntryFollowerFrameCurrent {
		t.Fatalf("zero-value service selected mode %v", got)
	}
	if got := (&Service{}).partyDungeonEntryFollowerFrame().String(); got != partyDungeonEntryFollowerFrameValueCurrent {
		t.Fatalf("zero-value service log label=%q", got)
	}
	if partyDungeonEntryFollowerFrameSkip.String() != partyDungeonEntryFollowerFrameValueSkip {
		t.Fatalf("skip log label=%q", partyDungeonEntryFollowerFrameSkip.String())
	}
	if partyDungeonEntryFollowerFrameCurrent.skipsRosterFrame() {
		t.Fatal("current mode must keep the follower conversion party record")
	}
	if !partyDungeonEntryFollowerFrameSkip.skipsRosterFrame() {
		t.Fatal("skip mode must drop the follower conversion party record")
	}
	// 2026-09-27：默认档由 current 改为 **skip**（实机单变量已证：skip ⇒ 进图后退队 0 次、
	// 双方照常进图；current ⇒ 每轮 1 次退队并把队伍降成单人）。空/未设置/非法值都落到修复档；
	// 回退值给的是 current / legacy / off / 0 / false / no。
	for _, value := range []string{"current", "CURRENT", "legacy", "off", "OFF", "0", "false", "no"} {
		if got := readPartyDungeonEntryFollowerFrameMode(value); got != partyDungeonEntryFollowerFrameCurrent {
			t.Fatalf("readPartyDungeonEntryFollowerFrameMode(%q)=%v want current (rollback value)", value, got)
		}
	}
	for _, value := range []string{"", "  ", "junk", "skip", " SKIP ", "skipped", "omit"} {
		if got := readPartyDungeonEntryFollowerFrameMode(value); got != partyDungeonEntryFollowerFrameSkip {
			t.Fatalf("readPartyDungeonEntryFollowerFrameMode(%q)=%v want skip (verified fix default)", value, got)
		}
	}
}

// TestDungeonPartyEntryFollowerFrameCurrentKeepsConversionRoster 钉住 H2 的 **off 档 = 今天**：
// 队员在"跟随者选择器转换"这一批里会收到 op153 实时信息 + 自己那条 op9 名册帧
// （在 op28/op29 **之前**），队长没有这一条（它没有转换相位）。
func TestDungeonPartyEntryFollowerFrameCurrentKeepsConversionRoster(t *testing.T) {
	steps, log := dungeonEntryPhaseScenarioRun(t, dungeonEntryPhaseScenario{
		EntrySlotTable: dungeonPartyEntrySlotTableCurrent,
		RoomSlotTable:  dungeonPartyRoomSlotTableVendor,
		FollowerFrame:  partyDungeonEntryFollowerFrameCurrent,
	})
	op28 := firstPartyDungeonSyncStep(steps, 100, currentDungeonInfoNotification, nil)
	if op28 < 0 {
		t.Fatalf("follower op28 anchor missing")
	}
	conversion := dungeonEntryPreOp29SelfKind0Indexes(steps, 100)
	if len(conversion) != 1 {
		t.Fatalf("follower pre-op29 self kind0 slot tables=%d want=1 (the follower selector conversion)", len(conversion))
	}
	if conversion[0] >= op28 {
		t.Fatalf("follower conversion party record index=%d must precede the op28 barrier index=%d", conversion[0], op28)
	}
	if conversion[0] == 0 || steps[conversion[0]-1].MsgID != upperMsgRealtimeInfoFrameID ||
		steps[conversion[0]-1].CharID != 100 {
		t.Fatalf("the conversion party record must be preceded by its op153 realtime info, got %+v", steps[conversion[0]-1])
	}
	if pre := dungeonEntryPreOp29SelfKind0Indexes(steps, 99); len(pre) != 0 {
		t.Fatalf("the leader has no follower-conversion phase, got %d pre-op29 self kind0 tables", len(pre))
	}
	if strings.Contains(log, "game-party-dungeon-entry-follower-frame-skipped") {
		t.Fatalf("off mode must not skip anything:\n%s", log)
	}
}

// TestDungeonPartyEntryFollowerFrameSkipDropsOnlyThatFrame 是 H2 on 档的主回归：
// 队员转换相位那条 op9 消失（op153 仍在），其余每个包逐字节、逐顺序不变；队长的整条流
// 一个字节都不动（它本来就没有这一条）。
func TestDungeonPartyEntryFollowerFrameSkipDropsOnlyThatFrame(t *testing.T) {
	offSteps, _ := dungeonEntryPhaseScenarioRun(t, dungeonEntryPhaseScenario{
		EntrySlotTable: dungeonPartyEntrySlotTableCurrent,
		RoomSlotTable:  dungeonPartyRoomSlotTableVendor,
		FollowerFrame:  partyDungeonEntryFollowerFrameCurrent,
	})
	onSteps, onLog := dungeonEntryPhaseScenarioRun(t, dungeonEntryPhaseScenario{
		EntrySlotTable: dungeonPartyEntrySlotTableCurrent,
		RoomSlotTable:  dungeonPartyRoomSlotTableVendor,
		FollowerFrame:  partyDungeonEntryFollowerFrameSkip,
	})
	offConversion := dungeonEntryPreOp29SelfKind0Indexes(offSteps, 100)
	if len(offConversion) != 1 {
		t.Fatalf("off-mode follower pre-op29 self kind0 slot tables=%d want=1", len(offConversion))
	}
	if onConversion := dungeonEntryPreOp29SelfKind0Indexes(onSteps, 100); len(onConversion) != 0 {
		t.Fatalf("on-mode follower pre-op29 self kind0 slot tables=%d want=0", len(onConversion))
	}
	// start-map 提交窗口内的两条（H1 的对象）不受 H2 影响。
	if offWindow, onWindow := dungeonEntryCommitWindowIndexes(offSteps, 100), dungeonEntryCommitWindowIndexes(onSteps, 100); len(offWindow) != 2 || len(onWindow) != 2 {
		t.Fatalf("the start-map commit window must keep its two kind0 slot tables: off=%d on=%d", len(offWindow), len(onWindow))
	}
	// 队员：整条流 = off 删掉那一条（shift 对齐；op11 body 按进程变化故豁免）。
	off := dungeonEntryFrameKeys(offSteps, 100)
	on := dungeonEntryFrameKeys(onSteps, 100)
	dropped := dungeonEntryCharLocalIndex(offSteps, 100, offConversion[0])
	if diffs := dungeonEntryWireDiffs("follower off-vs-on", off, on, dropped); len(diffs) != 0 {
		t.Fatalf("follower on mode changed more than the conversion party record:\n%s", strings.Join(diffs, "\n"))
	}
	// 队长：一条都不该动。
	if diffs := dungeonEntryWireDiffs("leader off-vs-on",
		dungeonEntryFrameKeys(offSteps, 99), dungeonEntryFrameKeys(onSteps, 99), -1); len(diffs) != 0 {
		t.Fatalf("the leader wire must not change at all:\n%s", strings.Join(diffs, "\n"))
	}
	// op153 照发：转换相位仍能看到实时信息（血条/槽位/小地图）。
	foundRealtime := false
	for _, step := range onSteps {
		if step.CharID == 100 && step.MsgID == upperMsgRealtimeInfoFrameID {
			foundRealtime = true
			break
		}
	}
	if !foundRealtime {
		t.Fatal("on mode dropped the conversion op153 realtime info as well")
	}
	if got := strings.Count(onLog, "game-party-dungeon-entry-follower-frame-skipped"); got != 1 {
		t.Fatalf("on mode skip events=%d want=1 (the follower conversion):\n%s", got, onLog)
	}
	for _, want := range []string{
		"game-party-dungeon-entry-follower-frame-skipped",
		"mode=skip",
		"phase=follower_selector_conversion_before_op28_op29",
		"kept=op153_realtime_info_still_sent",
	} {
		if !strings.Contains(onLog, want) {
			t.Fatalf("on-mode log missing %q:\n%s", want, onLog)
		}
	}
}

// dungeonEntryFinishLoadingTailRun 用"有真实副本运行时 + 真实角色仓储"的组队夹具
// （dungeon_party_instance_death_test.go 的 partyDungeonDeathFixture）驱动一次**读条尾段**：
// 这正是队友 actor 补发与 H3 名册重推共同的相位边界。同步进场夹具没有活动副本运行时，
// 尾段里那两类补发都会按既有门禁跳过（`sessionHasActiveDungeonRuntime`），所以 H3 的
// 相位断言在那边看不到效果。
func dungeonEntryFinishLoadingTailRun(
	t *testing.T,
	postLoadingRoster bool,
) ([]upperFrame, []upperFrame, string) {
	t.Helper()
	if postLoadingRoster {
		t.Setenv(partyDungeonPostLoadingRosterEnv, "on")
	} else {
		t.Setenv(partyDungeonPostLoadingRosterEnv, "off")
	}
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	// 这个夹具本身不挂包日志；本用例要断言"补推事件是否出现"，所以补一个（口径同同步进场夹具）。
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	followerConn := follower.conn.(*bufferConn)
	leaderConn := leader.conn.(*bufferConn)
	followerConn.write.Reset()
	leaderConn.write.Reset()
	// 复位三件套门闩：等价于"客户端刚上报 op37"，让这一次调用真的走完整条读条尾段。
	// `postFinishLoadingPlayerStateSent` 保持 true（即"那一段已经发过"）：它只负责 op19 技能页与
	// post-finish op120，需要真实 `Script.pvf`（本夹具没有）；H3 的相位边界在这之后，
	// 与这一段是否重发无关。
	follower.currentFinishLoadingStateSent = false
	follower.currentFinishLoadingCompletionSent = false
	follower.postFinishLoadingPlayerStateSent = true
	if err := service.sendCurrentFinishLoadingCharacterState(follower, "test_finish_loading_tail"); err != nil {
		t.Fatalf("finish loading tail: %v", err)
	}
	return splitUpperFrames(t, followerConn.write.Bytes()),
		splitUpperFrames(t, leaderConn.write.Bytes()),
		testPacketLogText(t, service)
}

// TestDungeonPartyPostLoadingRosterOffKeepsTailWire 钉住 H3 的 **off 档 = 今天**：
// 读条尾段一个字节都不多、日志里没有重推事件。
func TestDungeonPartyPostLoadingRosterOffKeepsTailWire(t *testing.T) {
	frames, peerFrames, log := dungeonEntryFinishLoadingTailRun(t, false)
	if len(frames) == 0 {
		t.Fatal("finish loading tail wrote no packet")
	}
	for _, frame := range frames {
		if frame.msgID == upperMsgRealtimeInfoFrameID {
			t.Fatalf("off mode sent an op153 inside the finish-loading tail: % X", frame.body)
		}
	}
	if len(peerFrames) != 0 {
		t.Fatalf("the tail must only touch the triggering session, peer got %d frames", len(peerFrames))
	}
	if strings.Contains(log, "game-party-dungeon-party-post-loading-roster-repush") {
		t.Fatalf("off mode must not repush the roster:\n%s", log)
	}
}

// TestDungeonPartyPostLoadingRosterOnRepushesAfterOp37 是 H3 on 档的主回归：
// 读条尾段之后**恰好**多一对 op153 + 自己那条 op9 名册帧，位置在本人 op37 主状态之后，
// 其余包序逐字节不变，且只发给触发方自己（队友零字节）。
func TestDungeonPartyPostLoadingRosterOnRepushesAfterOp37(t *testing.T) {
	offFrames, _, _ := dungeonEntryFinishLoadingTailRun(t, false)
	onFrames, peerFrames, onLog := dungeonEntryFinishLoadingTailRun(t, true)
	if len(peerFrames) != 0 {
		t.Fatalf("the roster repush must only touch the triggering session, peer got %d frames", len(peerFrames))
	}
	off := make([]dungeonEntryFrameKey, 0, len(offFrames))
	for _, frame := range offFrames {
		off = append(off, dungeonEntryFrameKey{class: 0, msgID: frame.msgID, body: string(frame.body)})
	}
	on := make([]dungeonEntryFrameKey, 0, len(onFrames))
	for _, frame := range onFrames {
		on = append(on, dungeonEntryFrameKey{class: 0, msgID: frame.msgID, body: string(frame.body)})
	}
	insert, diffs := dungeonEntryInsertedPairIndex("finish-loading tail off-vs-on", off, on)
	if len(diffs) != 0 {
		t.Fatalf("post-loading roster repush changed more than the inserted pair:\n%s", strings.Join(diffs, "\n"))
	}
	realtimeFrame, rosterFrame := on[insert], on[insert+1]
	if realtimeFrame.msgID != upperMsgRealtimeInfoFrameID {
		t.Fatalf("inserted frame[%d] must be the op153 realtime info, got msg%d", insert, realtimeFrame.msgID)
	}
	followerKey := currentSceneActorObjectKey(100)
	rosterBody := []byte(rosterFrame.body)
	if rosterFrame.msgID != uint16(dnfenum.CmdPacketRecoverStamina) || len(rosterBody) < 12 ||
		rosterBody[9] != currentSceneOp9ActorDisplayKind ||
		binary.LittleEndian.Uint16(rosterBody[4:6]) != followerKey {
		t.Fatalf("inserted frame[%d] must be the receiver's own kind0 party record: % X", insert+1, rosterBody)
	}
	rows, leaderSlot := parseOp9PartyFrame(t, rosterBody, true)
	if len(rows) != 2 || rows[0].uid != 99 || rows[1].uid != 100 {
		t.Fatalf("repushed party record rows=%+v want leader 99 at slot 0", rows)
	}
	if leaderSlot != 0 {
		t.Fatalf("repushed party record leader slot=%d want 0", leaderSlot)
	}
	// 相位：这一对必须落在本人 op37 主状态**之后**。
	mainState := -1
	for index, frame := range onFrames {
		if frame.msgID == uint16(dnfenum.CmdPacketFinishLoading) && len(frame.body) == currentFinishLoadingCharacterStateBodySize {
			mainState = index
			break
		}
	}
	if mainState < 0 {
		t.Fatal("the finish-loading tail must contain the op37 main state")
	}
	if insert <= mainState {
		t.Fatalf("roster repush index=%d must follow the op37 main state index=%d", insert, mainState)
	}
	if got := strings.Count(onLog, "game-dungeon-party-post-loading-roster-repush "); got != 1 {
		t.Fatalf("on mode roster repush events=%d want=1:\n%s", got, onLog)
	}
	if strings.Contains(onLog, "game-dungeon-party-post-loading-roster-repush-failed") {
		t.Fatalf("on mode roster repush failed:\n%s", onLog)
	}
	for _, want := range []string{
		"sequence=op153_realtime_info_then_own_op9_party_record",
		"scope=receiver_only_no_fanout_no_op11",
	} {
		if !strings.Contains(onLog, want) {
			t.Fatalf("on-mode log missing %q:\n%s", want, onLog)
		}
	}
}

// TestDungeonPartyPostLoadingRosterValveSkipsSoloParty 是 H3 的安全阀：
// 单人队伍（或会话没有活动副本运行时）时一个字节都不发。
func TestDungeonPartyPostLoadingRosterValveSkipsSoloParty(t *testing.T) {
	t.Setenv(partyDungeonPostLoadingRosterEnv, "on")
	service, leader, follower, _, _ := partyDungeonDeathFixture(t)
	logger, _ := newPeerEndpointTestLogger(t)
	service.packetLog = logger
	t.Cleanup(func() { _ = logger.Close() })
	solo := alignedcmd.PartyState{
		PartyID: 12,
		UserID:  100,
		Members: []alignedcmd.PartyMemberState{{UserID: 100, UserState: 1, HPPercent: 100, MPPercent: 100}},
	}
	storeRuntimePartyState(follower, solo)
	storeRuntimePartyState(leader, solo)
	conn := follower.conn.(*bufferConn)
	conn.write.Reset()
	service.repushDungeonPartyRosterAfterFinishLoading(follower, "test_solo_valve")
	if conn.write.Len() != 0 {
		t.Fatalf("solo party must not receive a roster repush, wrote %d bytes", conn.write.Len())
	}
	if log := testPacketLogText(t, service); strings.Contains(log, "game-dungeon-party-post-loading-roster-repush") {
		t.Fatalf("solo party must not log a roster repush:\n%s", log)
	}
}
