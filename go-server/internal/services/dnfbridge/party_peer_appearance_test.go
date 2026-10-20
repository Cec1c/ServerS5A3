package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// 本文件钉住「组队队友的时装（克隆装扮）在两个方向、以及过场重投影后都必须带上
// LinkItemId」这条修复。夹具全部是**线上真行**（2026-09-28 13:36/13:45 组队会话：
// packet_log.txt 的 `game-dungeon-party-peer-actor-send` +
// `dnf_equipment_entries` / `dnf_equipment_entry_extra` 原值），不发明任何形状。
//
// 线上原值：
//
//	leader 角色 3 = test2  (dnf:10001) 8 件普通装扮(415550045…) + 称号，无克隆件
//	member 角色 6 = test78 (dnf:10011) 8 件稀有克隆装扮(42601/42603/42605/42609/
//	       42611/42615/42607/42613)，每个穿戴行的 Extra 带
//	       clone_avatar_appearance_id = 被克隆件；另有皮肤/光环/武器装扮/武器/称号/名片
//
// 修前（线上字节）：两个方向的 15/14 行外观记录 LinkItemId **全是 0**。
// 修后：克隆行带上克隆件自己的 ItemID，普通行仍然是 0（逐字节不变）。

// peerAppearanceCloneRow 是角色 6 的穿戴槽 → (克隆件, 被克隆件) 真值表。
// 来源：dnf_equipment_entry_extra.clone_avatar_appearance_id 与 packet_log 里
// mode0 外观行的 display 值逐条对齐。
type peerAppearanceCloneRow struct {
	slot    int16
	cloneID int64
	source  int64
}

var peerAppearanceCloneRows = []peerAppearanceCloneRow{
	{slot: 0, cloneID: 42601, source: 101550581},
	{slot: 1, cloneID: 42603, source: 101560738},
	{slot: 2, cloneID: 42605, source: 101570490},
	{slot: 3, cloneID: 42609, source: 101500759},
	{slot: 4, cloneID: 42611, source: 101510923},
	{slot: 5, cloneID: 42615, source: 101540674},
	{slot: 6, cloneID: 42607, source: 101520562},
	{slot: 7, cloneID: 42613, source: 101530518},
}

// peerAppearanceMemberNameTagItemID 是角色 6 的名片（装备槽 30 = 2747069，
// 角色记录里的 name_tag_item_id；线上 mode0 外观行落在槽 28）。
const peerAppearanceMemberNameTagItemID = 2747069

func peerAppearanceEntryKey(slot int16) string {
	return strconv.Itoa(int(slot))
}

func peerAppearanceNewEntry(slot int16, itemID int64, extra map[string]string) dnfrepo.EquipmentEntry {
	merged := map[string]string{"current_exe_equipment_type": strconv.Itoa(int(slot))}
	for key, value := range extra {
		merged[key] = value
	}
	return dnfrepo.EquipmentEntry{
		SlotIndex: slot,
		ItemID:    itemID,
		RawEntry:  make([]byte, currentItemListEntryWireSize),
		Extra:     merged,
	}
}

// peerAppearanceMemberRecord 造角色 6（test78）的穿戴记录：8 件克隆装扮 + 皮肤
// (101580204) + 光环 (101590068) + 武器装扮 (101600023) + 光环动画件 (101590023) +
// 武器 (101040493) + 称号 (400330106) + 名片 (2747069，穿戴槽 30)。
func peerAppearanceMemberRecord() dnfrepo.EquipmentRecord {
	entries := map[string]dnfrepo.EquipmentEntry{}
	for _, row := range peerAppearanceCloneRows {
		entries[peerAppearanceEntryKey(row.slot)] = peerAppearanceNewEntry(row.slot, row.cloneID, map[string]string{
			dnfrepo.CloneAvatarAppearanceExtraKey: strconv.FormatInt(row.source, 10),
		})
	}
	ordinary := []struct {
		slot   int16
		itemID int64
	}{
		{8, 101580204}, {9, 101590068}, {10, 101600023}, {11, 101590023},
		{12, 101040493}, {13, 400330106}, {30, peerAppearanceMemberNameTagItemID},
	}
	for _, row := range ordinary {
		entries[peerAppearanceEntryKey(row.slot)] = peerAppearanceNewEntry(row.slot, row.itemID, nil)
	}
	return dnfrepo.EquipmentRecord{CharacterID: "6", Entries: entries}
}

// peerAppearanceLeaderRecord 造角色 3（test2）的穿戴记录：8 件普通装扮 + 称号，
// 一件克隆件都没有（真值：dnf_equipment_entries 里没有任何 clone_avatar_appearance_id）。
func peerAppearanceLeaderRecord() dnfrepo.EquipmentRecord {
	slots := []struct {
		slot   int16
		itemID int64
	}{
		{0, 415550045}, {1, 415560054}, {2, 415570038}, {3, 415500046},
		{4, 415510038}, {5, 415540052}, {6, 415520041}, {7, 415530038},
		{8, 415580009}, {13, 400330105},
	}
	entries := map[string]dnfrepo.EquipmentEntry{}
	for _, row := range slots {
		entries[peerAppearanceEntryKey(row.slot)] = peerAppearanceNewEntry(row.slot, row.itemID, nil)
	}
	return dnfrepo.EquipmentRecord{CharacterID: "3", Entries: entries}
}

// peerAppearanceCharacter 造与穿戴记录配套的角色记录（名片统计值在这里，与线上一致：
// mode0 外观行的名片来自角色 stats，不是穿戴槽 30）。
func peerAppearanceCharacter(record dnfrepo.EquipmentRecord) dnfrepo.CharacterRecord {
	character := dnfrepo.CharacterRecord{
		CharacterID: record.CharacterID,
		AccountID:   "dnf:1",
		Name:        "test" + record.CharacterID,
		Job:         "0",
		Level:       90,
	}
	if record.CharacterID == "6" {
		character.Name = "test78"
		character.Stats = map[string]int64{
			"name_tag_item_id":     peerAppearanceMemberNameTagItemID,
			"name_tag_expire_time": 0,
		}
	}
	if record.CharacterID == "3" {
		character.Name = "test2"
		character.Job = "14"
	}
	return character
}

// peerAppearanceMode0Body 走**真实的**队友投影路径：`sendDungeonPartyPeerActorState`
// 用的就是 buildCurrentSceneObjectListBodyForSessionInContextStrict，而它内部落在
// buildCurrentActorMode0AppearanceSummaryFromEquipment 上。
func peerAppearanceMode0Body(t *testing.T, record dnfrepo.EquipmentRecord) []byte {
	t.Helper()
	repos := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	character := peerAppearanceCharacter(record)
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	if err := repos.Equipment.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
	charID := uint16(6)
	if record.CharacterID == "3" {
		charID = 3
	}
	service := &Service{repositoryProvider: func() (dnfrepo.Group, bool) { return repos, true }}
	session := &gameSession{selectedCharacterID: charID}
	body, err := service.buildCurrentSceneObjectListBodyForSessionInContextStrict(
		ctx, session, charID, character.Name, character, true, currentSceneObjectContext)
	if err != nil {
		t.Fatalf("peer mode0 body: %v", err)
	}
	return body
}

// peerAppearanceRowForTest 是 19 字节外观行里我们关心的三个字段。
type peerAppearanceRowForTest struct {
	display uint32
	link    uint32
}

// peerAppearanceRowsForTest 从 mode0 体里解出 19 字节外观行（slot → display/link）。
func peerAppearanceRowsForTest(t *testing.T, body []byte) map[int]peerAppearanceRowForTest {
	t.Helper()
	_, tailStart, ok := currentSceneObjectLevelForLog(body)
	if !ok {
		t.Fatalf("mode0 tail start not found body_len=%d", len(body))
	}
	tail := body[tailStart:]
	end, ok := currentSceneObjectEquipSummaryEnd(tail, 6)
	if !ok {
		t.Fatalf("mode0 appearance table unparsable tail=%x", tail)
	}
	count := int(tail[6])
	rows := map[int]peerAppearanceRowForTest{}
	pos := 7
	for idx := 0; idx < count; idx++ {
		if pos+currentSceneObjectEquipSummaryRowBytes > end {
			t.Fatalf("row %d overruns table pos=%d end=%d", idx, pos, end)
		}
		row := tail[pos : pos+currentSceneObjectEquipSummaryRowBytes]
		rows[int(row[0])] = peerAppearanceRowForTest{
			display: binary.LittleEndian.Uint32(row[1:5]),
			link:    binary.LittleEndian.Uint32(row[10:14]),
		}
		pos += currentSceneObjectEquipSummaryRowBytes
	}
	return rows
}

// TestPeerAppearanceCloneLinkIsProjectedBothDirections 是本次修复的黄金用例：
//
//	方向 A（队员 → 队长）：角色 6 的克隆时装，8 行都必须 display=被克隆件、link=克隆件；
//	方向 B（队长 → 队员）：角色 3 没有克隆件，所有行 link 必须仍是 0（不受影响）。
//
// 两个方向走的是同一个 builder（receiver 只决定 owner 字节），所以这里对两份真值各建
// 一次体，断言"两个方向各自该有的字节"——也就是这条投影**对称且完整**。
func TestPeerAppearanceCloneLinkIsProjectedBothDirections(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)

	memberRows := peerAppearanceRowsForTest(t, peerAppearanceMode0Body(t, peerAppearanceMemberRecord()))
	for _, row := range peerAppearanceCloneRows {
		got, ok := memberRows[int(row.slot)]
		if !ok {
			t.Fatalf("slot %d missing from peer fashion table rows=%v", row.slot, memberRows)
		}
		if got.display != uint32(row.source) {
			t.Fatalf("slot %d display=%d want cloned source %d", row.slot, got.display, row.source)
		}
		if got.link != uint32(row.cloneID) {
			t.Fatalf("slot %d LinkItemId=%d want the clone itself %d (peer fashion renders as the transparent clone item)",
				row.slot, got.link, row.cloneID)
		}
	}
	// 普通件（皮肤 8 / 光环 9 / 武器装扮 10 / 光环动画 11 / 武器 12 / 称号 13 / 名片 28）
	// 必须保持 link=0，字节不受影响。
	for _, slot := range []int{8, 9, 10, 11, 12, 13, currentNameTagAppearanceSlot} {
		if got := memberRows[slot].link; got != 0 {
			t.Fatalf("ordinary slot %d LinkItemId=%d want 0 (bytes must not change)", slot, got)
		}
	}

	leaderRows := peerAppearanceRowsForTest(t, peerAppearanceMode0Body(t, peerAppearanceLeaderRecord()))
	if len(leaderRows) != currentActorMode0AppearanceSlotCount {
		t.Fatalf("leader appearance rows=%d want %d", len(leaderRows), currentActorMode0AppearanceSlotCount)
	}
	for slot, row := range leaderRows {
		if row.link != 0 {
			t.Fatalf("leader slot %d LinkItemId=%d want 0 (no clone avatar is worn)", slot, row.link)
		}
	}
	// 两个方向都必须真的把时装发出去（这正是操作者看到的"模型在、时装不在"的反面）。
	if got := leaderRows[3].display; got != 415500046 {
		t.Fatalf("leader slot 3 display=%d want 415500046", got)
	}
}

// TestPeerAppearanceCloneLinkSurvivesSceneTransition 钉住"过场/换房间重投影"：
// 队友 actor 在新场景里被重新建一次，外观行必须还是带链接的那一份（换房间不改穿戴
// 记录，只改 run key，所以这里对同一份真值再建一次体并逐字节比对）。
func TestPeerAppearanceCloneLinkSurvivesSceneTransition(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)

	record := peerAppearanceMemberRecord()
	first := peerAppearanceMode0Body(t, record)
	// 过场（op45 换房间 / op37 补发）走的是同一个投影函数：同一份穿戴记录再建一次。
	second := peerAppearanceMode0Body(t, record)
	if string(first) != string(second) {
		t.Fatalf("scene-transition re-projection differs:\n first=%x\nsecond=%x", first, second)
	}
	rows := peerAppearanceRowsForTest(t, second)
	for _, row := range peerAppearanceCloneRows {
		if got := rows[int(row.slot)].link; got != uint32(row.cloneID) {
			t.Fatalf("after scene transition slot %d LinkItemId=%d want %d", row.slot, got, row.cloneID)
		}
	}
	// 过场后外观行仍然是 15 行（14 + 名片），武器装扮（槽 10）也在。
	if len(rows) != currentActorMode0AppearanceSlotCount+1 {
		t.Fatalf("after scene transition appearance rows=%d want %d", len(rows), currentActorMode0AppearanceSlotCount+1)
	}
	if rows[10].display != 101600023 {
		t.Fatalf("after scene transition weapon-avatar display=%d want 101600023", rows[10].display)
	}
	if rows[currentNameTagAppearanceSlot].display != peerAppearanceMemberNameTagItemID {
		t.Fatalf("after scene transition name-tag display=%d want %d",
			rows[currentNameTagAppearanceSlot].display, peerAppearanceMemberNameTagItemID)
	}
}

// TestPeerAppearanceCloneLinkRollbackReproducesTodaysBytes 钉住回退档：
// DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK=off 时，场景对象外观行的 LinkItemId 回到 0，
// **其余字节逐字节相同**（只有每个克隆行的那 4 个链接字节从克隆件 id 变成 0）。
func TestPeerAppearanceCloneLinkRollbackReproducesTodaysBytes(t *testing.T) {
	record := peerAppearanceMemberRecord()

	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)
	fixed := peerAppearanceMode0Body(t, record)

	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOff)
	rollback := peerAppearanceMode0Body(t, record)

	if len(fixed) != len(rollback) {
		t.Fatalf("rollback body_len=%d want %d", len(rollback), len(fixed))
	}
	// 逐字节比对：改动的偏移必须**全部**落在克隆行的 LinkItemId 那 4 个字节里，
	// 别的一个字节都不许动。（克隆件 id 都是 0x0000A6xx，高位字节本来就相同，
	// 所以这里断言的是"改动偏移集合 ⊆ 链接字段"，比数个数更有意义。）
	_, tailStart, ok := currentSceneObjectLevelForLog(fixed)
	if !ok {
		t.Fatalf("fixed body tail start unreadable body_len=%d", len(fixed))
	}
	linkOffsets := map[int]bool{}
	cloneRowIndex := map[int]bool{}
	for _, row := range peerAppearanceCloneRows {
		cloneRowIndex[int(row.slot)] = true
	}
	tableRows := peerAppearanceRowsForTest(t, fixed)
	if len(tableRows) != currentActorMode0AppearanceSlotCount+1 {
		t.Fatalf("fixed appearance rows=%d want %d", len(tableRows), currentActorMode0AppearanceSlotCount+1)
	}
	// 行序 = 槽位升序（buildCurrentActorMode0AppearanceSummaryFromEquipment 按槽写表）。
	orderedSlots := []int{}
	for slot := 0; slot < currentActorMode0AppearanceSlotCount; slot++ {
		orderedSlots = append(orderedSlots, slot)
	}
	orderedSlots = append(orderedSlots, currentNameTagAppearanceSlot)
	for idx, slot := range orderedSlots {
		if !cloneRowIndex[slot] {
			continue
		}
		base := tailStart + 6 + 1 + idx*currentSceneObjectEquipSummaryRowBytes
		for offset := base + 10; offset < base+14; offset++ {
			linkOffsets[offset] = true
		}
	}
	differing := 0
	for offset := range fixed {
		if fixed[offset] == rollback[offset] {
			continue
		}
		differing++
		if !linkOffsets[offset] {
			t.Fatalf("rollback changed a non-link byte at offset %d: fixed=%#x rollback=%#x",
				offset, fixed[offset], rollback[offset])
		}
	}
	if differing == 0 {
		t.Fatal("rollback body is identical to the fixed body: the switch does not gate the clone link")
	}
	rollbackRows := peerAppearanceRowsForTest(t, rollback)
	for slot, row := range rollbackRows {
		if row.link != 0 {
			t.Fatalf("rollback slot %d LinkItemId=%d want 0 (today's bytes)", slot, row.link)
		}
	}
	// 显示件（被克隆件）在回退档下也必须保持原样：回退只关链接，不关显示替换。
	for _, row := range peerAppearanceCloneRows {
		if got := rollbackRows[int(row.slot)].display; got != uint32(row.source) {
			t.Fatalf("rollback slot %d display=%d want %d", row.slot, got, row.source)
		}
	}

	// 没有克隆件的角色在开关两档下必须逐字节相同。
	leader := peerAppearanceLeaderRecord()
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)
	leaderFixed := peerAppearanceMode0Body(t, leader)
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOff)
	leaderRollback := peerAppearanceMode0Body(t, leader)
	if string(leaderFixed) != string(leaderRollback) {
		t.Fatalf("non-clone actor bytes changed between switch positions:\nfixed=%x\nroll =%x", leaderFixed, leaderRollback)
	}
}

// TestPeerAppearanceCloneLinkObeysParentSwitches 钉住"父开关优先"：
// DNFBRIDGE_CLONE_AVATAR_APPEARANCE / _ROW_CLEAR_ID 任一回退，场景对象外观行也必须
// 回到 0（三条载体同时回退，逐字节同改动前）。
func TestPeerAppearanceCloneLinkObeysParentSwitches(t *testing.T) {
	record := peerAppearanceMemberRecord()
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)

	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOff)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	for slot, row := range peerAppearanceRowsForTest(t, peerAppearanceMode0Body(t, record)) {
		if row.link != 0 {
			t.Fatalf("clone-avatar rollback slot %d LinkItemId=%d want 0", slot, row.link)
		}
	}

	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOff)
	for slot, row := range peerAppearanceRowsForTest(t, peerAppearanceMode0Body(t, record)) {
		if row.link != 0 {
			t.Fatalf("row-clear-id rollback slot %d LinkItemId=%d want 0", slot, row.link)
		}
	}
}

// TestPeerAppearanceCloneLinkSwitchRejectsUnknownValue 钉住非法值回退到修复档并报 bad。
func TestPeerAppearanceCloneLinkSwitchRejectsUnknownValue(t *testing.T) {
	t.Setenv(currentPeerAppearanceCloneLinkEnv, "maybe")
	enabled, raw, bad := currentPeerAppearanceCloneLinkSetting()
	if !enabled || !bad || raw != "maybe" {
		t.Fatalf("setting = enabled=%t raw=%q bad=%t, want true/maybe/true", enabled, raw, bad)
	}
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceLegacy)
	if enabled, raw, bad = currentPeerAppearanceCloneLinkSetting(); enabled || bad || raw != cloneAvatarAppearanceLegacy {
		t.Fatalf("legacy setting = enabled=%t raw=%q bad=%t, want false/legacy/false", enabled, raw, bad)
	}
	if mode := currentPeerAppearanceCloneLinkMode(); mode != cloneAvatarAppearanceOff {
		t.Fatalf("legacy mode=%q want %q", mode, cloneAvatarAppearanceOff)
	}
}

// TestPeerAppearanceJudgementEventCarriesBothDirections 钉住判读事件口径：
// 事件必须能直接回答"发了哪些部件、克隆链接写了几行"，下一次线上复测只看
// packet_log.txt 就能判定，不需要再解包。
func TestPeerAppearanceJudgementEventCarriesBothDirections(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)
	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOn)

	composition, ok := currentSceneAppearanceBodyCompositionForLog(peerAppearanceMode0Body(t, peerAppearanceMemberRecord()))
	if !ok {
		t.Fatal("appearance composition unreadable")
	}
	if composition.Rows != currentActorMode0AppearanceSlotCount+1 {
		t.Fatalf("composition rows=%d want %d", composition.Rows, currentActorMode0AppearanceSlotCount+1)
	}
	if composition.LinkRows != len(peerAppearanceCloneRows) {
		t.Fatalf("composition link rows=%d want %d", composition.LinkRows, len(peerAppearanceCloneRows))
	}
	components := strings.Join(currentSceneAppearanceSlotComponents(composition.Slots), ",")
	for _, want := range []string{"3:coat", "10:weapon_avatar", "12:weapon", "28:name_tag"} {
		if !strings.Contains(components, want) {
			t.Fatalf("component names=%q want %q", components, want)
		}
	}
	if got := currentPeerActorAppearanceIncluded(); !strings.Contains(got, "appearance_link_item_id") {
		t.Fatalf("included=%q want appearance_link_item_id", got)
	}
	if got := currentPeerActorAppearanceOmitted(); !strings.Contains(got, "noti2_roster_appearance_table") {
		t.Fatalf("omitted=%q want the roster carrier to be named as omitted", got)
	}

	t.Setenv(currentPeerAppearanceCloneLinkEnv, cloneAvatarAppearanceOff)
	rollback, ok := currentSceneAppearanceBodyCompositionForLog(peerAppearanceMode0Body(t, peerAppearanceMemberRecord()))
	if !ok {
		t.Fatal("rollback appearance composition unreadable")
	}
	if rollback.LinkRows != 0 {
		t.Fatalf("rollback link rows=%d want 0", rollback.LinkRows)
	}
}

// TestPeerAppearanceEventIsEmittedByThePeerActorSender 是端到端那一条：走真实的
// `sendDungeonPartyPeerActorState`（副本队友投影的发送处），把角色 100 的克隆时装投给
// 角色 99，然后在 packet_log 里读那条判读事件 —— 下一次线上复测的判据就是它。
func TestPeerAppearanceEventIsEmittedByThePeerActorSender(t *testing.T) {
	t.Setenv(cloneAvatarAppearanceEnv, cloneAvatarAppearanceOn)
	t.Setenv(cloneAvatarRowClearIDEnv, cloneAvatarAppearanceOn)

	run := func(t *testing.T, switchValue string) string {
		t.Helper()
		t.Setenv(currentPeerAppearanceCloneLinkEnv, switchValue)
		logPath := filepath.Join(t.TempDir(), "packet.log")
		logger, err := openPacketLogger(logPath)
		if err != nil {
			t.Fatal(err)
		}
		service, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
		repos, ok := service.repositoryGroup()
		if !ok {
			t.Fatal("fixture repository group unavailable")
		}
		record := peerAppearanceMemberRecord()
		record.CharacterID = "100"
		if err := repos.Equipment.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		service.packetLog = logger
		if err := service.sendDungeonPartyPeerActorState(leader, follower, "test_peer_appearance_event"); err != nil {
			t.Fatalf("send peer actor state: %v", err)
		}
		if err := logger.Close(); err != nil {
			t.Fatal(err)
		}
		logged, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(logged)
	}

	fixed := run(t, cloneAvatarAppearanceOn)
	line := ""
	for _, candidate := range strings.Split(fixed, "\n") {
		if strings.Contains(candidate, "kind=game-party-peer-actor-appearance") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("judgement event missing from packet log\n%s", fixed)
	}
	t.Logf("peer appearance event (fix): %s", line)
	for _, field := range []string{
		"receiver_char_id=99",
		"peer_char_id=100",
		"appearance_rows=14",
		"appearance_link_rows=8",
		"clone_link_env=" + currentPeerAppearanceCloneLinkEnv,
		"clone_link_mode=on",
		"appearance_components=mode0_appearance_table",
		"included=",
		"omitted=",
		// 2026-09-28 夜追加：第二条外观载体（被投影角色自己的 USERINFO subtype0 外观列表）。
		// 修复档下它必须**已经不在 omitted 里**、并且事件点名它已经送出/送了多少行。
		"appearance_subtype0_env=" + currentPeerAppearanceSubtype0Env,
		"appearance_subtype0_mode=on",
		"appearance_subtype0_sent=true",
		"peer_userinfo_carrier=peer_userinfo_subtype0_appearance_list_sent",
	} {
		if !strings.Contains(line, field) {
			t.Fatalf("judgement event is missing %q\nline=%s", field, line)
		}
	}
	if strings.Contains(line, "omitted=noti2_roster_appearance_table(login_only_carrier),") {
		t.Fatalf("fixed judgement event still lists the roster appearance carrier as omitted\nline=%s", line)
	}
	if !strings.Contains(line, "appearance_subtype0_link_rows=8") {
		t.Fatalf("the peer's own subtype0 appearance list must carry the 8 clone links\nline=%s", line)
	}

	rollback := run(t, cloneAvatarAppearanceOff)
	rollbackLine := ""
	for _, candidate := range strings.Split(rollback, "\n") {
		if strings.Contains(candidate, "kind=game-party-peer-actor-appearance") {
			rollbackLine = candidate
			break
		}
	}
	if !strings.Contains(rollbackLine, "appearance_link_rows=0") || !strings.Contains(rollbackLine, "clone_link_mode=off") {
		t.Fatalf("rollback judgement event must report 0 linked rows with the switch off\nline=%s", rollbackLine)
	}
	// 本用例只回退 `DNFBRIDGE_PEER_APPEARANCE_CLONE_LINK`；第二条载体的开关
	// （`DNFBRIDGE_PEER_APPEARANCE_SUBTYPE0_LIST`）在自己的用例里钉（见
	// party_peer_appearance_subtype0_test.go）。
	if !strings.Contains(rollbackLine, "appearance_subtype0_mode=on") {
		t.Fatalf("clone-link rollback must not disturb the independent subtype0 switch\nline=%s", rollbackLine)
	}
}
