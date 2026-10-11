package dnfbridge

// 本文件钉住 FIX ②（`login_switch_inventory_scene_ready_replay.go`）的不变量：
// **换装栏物品容器（list 1）与 list 3 一样，在 typed op24 场景提交之后、同一个
// 客户端 type1345 边界上再交付一次**，并且带的是操作者那 11 行（槽 54..64）。
//
// 现场依据（本轮只读 SQL，char 6）：
//
//	dnf_inventory_items 里 `1:<slot>` 恰好 11 行，槽位 54..64，
//	与客户端 op1428 登记的 11 个 value（54,62,61,57,59,63,58,64,60,56,55）一一对应 ⇒
//	换装栏面板 = op1428 的 map × list 1 容器；而 list 1 此前只有"提交之前"那一次引导。
//
//	1:54 101520658  1:55 101520658  1:56 101520658  1:57 101520658  1:58 101520671
//	1:59 101530518  1:60 101530603  1:61 101530603  1:62 101530607  1:63 101530612  1:64 101530612

import (
	"context"
	"strings"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// liveSwitchInventoryRows 是操作者那 11 行换装栏物品（槽位 → item_id，现场 SQL）。
func liveSwitchInventoryRows() []liveSceneReadyEquipmentRow31 {
	return []liveSceneReadyEquipmentRow31{
		{54, 101520658, 0}, {55, 101520658, 0}, {56, 101520658, 0}, {57, 101520658, 0},
		{58, 101520671, 0}, {59, 101530518, 0}, {60, 101530603, 0}, {61, 101530603, 0},
		{62, 101530607, 0}, {63, 101530612, 0}, {64, 101530612, 0},
	}
}

// seedLiveSwitchInventory 把上面 11 行写进角色的 list 1（`1:<slot>` 键）。
func seedLiveSwitchInventory(t *testing.T, service *Service) {
	t.Helper()
	repositories, ok := service.repositoryGroup()
	if !ok {
		t.Fatalf("fixture service has no repository group")
	}
	slots := make(map[string]dnfrepo.ItemStack)
	for _, row := range liveSwitchInventoryRows() {
		key := "1:" + itoaForSwitchTest(int(row.slot))
		slots[key] = dnfrepo.ItemStack{ItemID: row.itemID, Count: 1}
	}
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "6",
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}
}

func itoaForSwitchTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// switchInventorySceneReadyFrames 从下行流里挑出 list 1 的容器帧（class0/op13，
// 包体首字节 = list type 1），返回字节偏移与包体。
func switchInventorySceneReadyFrames(t *testing.T, stream []byte) (offsets []int, bodies [][]byte) {
	t.Helper()
	offset := 0
	for len(stream) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, stream)
		rawLen := len(stream) - len(rest)
		if packet.Header.Classification == 0 &&
			packet.Header.MsgID == uint16(13) &&
			len(packet.Body) >= 3 && packet.Body[0] == currentSwitchInventoryListType {
			offsets = append(offsets, offset)
			bodies = append(bodies, append([]byte(nil), packet.Body...))
		}
		offset += rawLen
		stream = rest
	}
	return offsets, bodies
}

// switchInventoryDriveToSceneReady 把会话推到"typed op24 已提交 + 客户端 type1345 已到"。
func switchInventoryDriveToSceneReady(t *testing.T, service *Service, session *gameSession) {
	t.Helper()
	if err := service.sendCurrentSelectInventoryBootstrap(
		session,
		"initial_town_before_final_transition_after_actor_before_full_player_state",
	); err != nil {
		t.Fatal(err)
	}
	transitionBody, err := buildCurrentSceneTransitionBody(31, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.sendCurrentTownTransitionLocked(session, 6, 31, 2, transitionBody); err != nil {
		t.Fatal(err)
	}
	if err := service.handleLegacyGamePacket(session, buildLegacyTownSceneReadyAckPacket()); err != nil {
		t.Fatalf("legacy type1345 boundary: %v", err)
	}
}

// TestSwitchInventoryContainerReachesTheClientAfterTheSceneCommit - 主用例：list 1
// 在 typed op24 **之后**交付一次，且带着操作者那 11 行（槽 54..64，item 与现场 SQL 相同）。
func TestSwitchInventoryContainerReachesTheClientAfterTheSceneCommit(t *testing.T) {
	t.Setenv(loginSwitchInventorySceneReadyReplayEnv, "")
	t.Setenv(loginEquipmentSceneReadyReplayEnv, "")
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, logPath := newSceneReadyReplayFixture(t)
	seedLiveSwitchInventory(t, service)
	armLoginRouteUpToOp24(session)

	switchInventoryDriveToSceneReady(t, service, session)
	stream := append([]byte(nil), connection.write.Bytes()...)

	offsets, bodies := switchInventorySceneReadyFrames(t, stream)
	if len(bodies) != 2 {
		t.Fatalf("list 1 delivered %d times, want 2 (pre-commit bootstrap + post-commit replay)", len(bodies))
	}
	commitOffset := sceneReadyReplayPacketOffsetOfMsgID(t, stream, currentSceneTransitionMsgID)
	if commitOffset < 0 {
		t.Fatalf("typed op24 commit packet missing from the stream")
	}
	if !(offsets[0] < commitOffset && commitOffset < offsets[1]) {
		t.Fatalf("list-1 offsets %v vs typed op24 offset %d: want pre < commit < post",
			offsets, commitOffset)
	}
	// 同一台构建器 ⇒ 提交后那一份与提交前那一份**逐字节相同**。
	if string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("post-commit list-1 container differs from the pre-commit one:\n pre=%x\npost=%x",
			bodies[0], bodies[1])
	}
	body := bodies[1]
	entrySize := currentItemListEntrySizeForType(currentSwitchInventoryListType)
	wantRows := len(liveSwitchInventoryRows())
	slots, items, rows, auditOK := loginSwitchInventoryContainerWireRowAudit(body, entrySize)
	if !auditOK || rows != wantRows {
		t.Fatalf("list-1 row audit ok=%t rows=%d, want true/%d (body_len=%d entry_size=%d head=%x)",
			auditOK, rows, wantRows, len(body), entrySize, body[:16])
	}
	if got := strings.Join(slots, ","); got != "54,55,56,57,58,59,60,61,62,63,64" {
		t.Fatalf("list-1 slots = %s, want 54..64 (head=%x)", got, body[:24])
	}
	wantItems := make([]string, 0, wantRows)
	for _, row := range liveSwitchInventoryRows() {
		wantItems = append(wantItems, itoaForSwitchTest(int(row.itemID)))
	}
	if got := strings.Join(items, ","); got != strings.Join(wantItems, ",") {
		t.Fatalf("list-1 items = %s, want %s", got, strings.Join(wantItems, ","))
	}

	log := sceneReadyReplayTestLog(t, service, logPath)
	if !strings.Contains(log, loginSwitchInventorySceneReadyReplaySentEvent) {
		t.Fatalf("missing %s in log:\n%s", loginSwitchInventorySceneReadyReplaySentEvent, log)
	}
	if !strings.Contains(log, "list=1") || !strings.Contains(log, "rows=11") {
		t.Fatalf("event must record list 1 with 11 rows:\n%s", log)
	}
	if !strings.Contains(log, "slots_sent=54,55,56,57,58,59,60,61,62,63,64") {
		t.Fatalf("event must record the operator's 11 list-1 slots:\n%s", log)
	}
	// 同一族的汇总事件：两个 list 都出现（list 3 由既有修复回填、list 1 由本条回填）。
	if !strings.Contains(log, loginPostCommitContainerListsEvent) ||
		!strings.Contains(log, "lists=3,1") {
		t.Fatalf("missing %s lists=3,1:\n%s", loginPostCommitContainerListsEvent, log)
	}
	if !strings.Contains(log, "list_3_rows=31") || !strings.Contains(log, "list_1_rows=11") {
		t.Fatalf("summary event must name both lists' row counts:\n%s", log)
	}
}

// TestSwitchInventorySceneReadyReplayRollbackKeepsTodaysBytes - 一键回退：list 1
// 一个字节都不发（只有 op24 之前那一次引导），而 list 3 那条既有修复**不受影响**。
func TestSwitchInventorySceneReadyReplayRollbackKeepsTodaysBytes(t *testing.T) {
	t.Setenv(loginSwitchInventorySceneReadyReplayEnv, loginSwitchInventorySceneReadyReplayBeforeTransitionOnly)
	t.Setenv(loginEquipmentSceneReadyReplayEnv, "")
	t.Setenv(skillSwitchInventoryModeEnv, skillSwitchInventoryBeforeTransitionTier)
	service, session, connection, logPath := newSceneReadyReplayFixture(t)
	seedLiveSwitchInventory(t, service)
	armLoginRouteUpToOp24(session)

	switchInventoryDriveToSceneReady(t, service, session)
	stream := append([]byte(nil), connection.write.Bytes()...)

	commitOffset := sceneReadyReplayPacketOffsetOfMsgID(t, stream, currentSceneTransitionMsgID)
	offsets, bodies := switchInventorySceneReadyFrames(t, stream)
	afterCommit := 0
	for _, offset := range offsets {
		if offset > commitOffset {
			afterCommit++
		}
	}
	if afterCommit != 0 {
		t.Fatalf("rollback tier delivered %d list-1 containers after the commit; want 0", afterCommit)
	}
	if len(bodies) == 0 {
		t.Fatalf("rollback tier must keep today's pre-commit list-1 bootstrap; found none")
	}
	// list 3 的既有修复不受本开关影响：提交之后仍然有那一份装备容器。
	equipmentOffsets, _, _ := sceneReadyReplayEquipmentContainers(t, stream)
	equipmentAfter := 0
	for _, offset := range equipmentOffsets {
		if offset > commitOffset {
			equipmentAfter++
		}
	}
	if equipmentAfter != 1 {
		t.Fatalf("list-3 post-commit replay = %d, want 1 (must not be affected by the list-1 switch)",
			equipmentAfter)
	}
	log := sceneReadyReplayTestLog(t, service, logPath)
	if !strings.Contains(log, loginSwitchInventorySceneReadyReplaySuppressedEvent) ||
		!strings.Contains(log, "switch="+loginSwitchInventorySceneReadyReplayBeforeTransitionOnly) {
		t.Fatalf("rollback tier event missing/mislabelled:\n%s", log)
	}
}
