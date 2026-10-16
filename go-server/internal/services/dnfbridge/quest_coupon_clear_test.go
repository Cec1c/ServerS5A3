package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
)

// Matching-client native action IDs: gm quest clear 169, lv clear 179.
const (
	testQuestClearCouponItemID = 10164630
	testQuestClearCouponPath   = "stackable/10164001/10164630.stk"
	testQuestClearCouponSlot   = int16(87)
)

func testQuestClearCouponRequestBody(slot int16, action uint32) []byte {
	body := make([]byte, currentQuestClearCouponBodySize)
	binary.LittleEndian.PutUint16(body[0:2], uint16(slot))
	body[2] = dnfrepo.MainInventoryListType
	binary.LittleEndian.PutUint32(body[7:11], action)
	return body
}

func testQuestClearCouponPVFSource() dungeonDropCatalogTestSource {
	return dungeonDropCatalogTestSource{
		"monster/monster.lst":     "",
		"equipment/equipment.lst": "",
		"stackable/stackable.lst": "10164630 `10164001/10164630.stk`\n" +
			"10301410 `10301001/10301410.stk`\n" +
			"22000001 `10164001/plain_material.stk`\n",
		"stackable/10164001/10164630.stk": "[name] `Lv1~90主线任务完成券`\n" +
			"[stackable type] `[etc]` 1\n[stack limit] 1\n[minimum level] 90\n" +
			"[action type]\n`[gm quest clear]`\n[/action type]\n[use action packet]\n0\n",
		"stackable/10301001/10301410.stk": "[name] `Lv1~90主线/成就任务完成券`\n" +
			"[stackable type] `[etc]` 1\n[stack limit] 1\n" +
			"[action type]\n`[lv clear]` 90\n[/action type]\n",
		"stackable/10164001/plain_material.stk": "[name] `Plain`\n[stackable type] `[material]`\n[stack limit] 1000\n",
	}
}

// newQuestClearCouponTestService 造一个 Lv90 角色：三条激活主线（100 等级 20、
// 102 等级 90、106 等级 35）+ 背包第 87 格一张完成券。
func newQuestClearCouponTestService(t *testing.T, couponCount int64) (*Service, *gameSession, dnfrepo.Group) {
	t.Helper()
	ctx := context.Background()
	repositories := dnfrepomemory.NewMemoryGroup()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{
		CharacterID: "17",
		AccountID:   defaultAccountPrefix + "1",
		Job:         "2",
		Level:       90,
		Stats:       map[string]int64{"grow_type": 0, "sp": 999, "exp": 54321},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "17",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "active", ProgressValue: 5},
			102: {Status: "active", ProgressValue: 1},
			106: {Status: "active", ProgressValue: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "17",
		Slots: map[string]dnfrepo.ItemStack{
			"0:87": {
				ItemID:   testQuestClearCouponItemID,
				Count:    couponCount,
				RawEntry: make([]byte, currentItemListEntryWireSize),
				Extra: map[string]string{
					"item_kind": "stackable",
					"pvf_path":  testQuestClearCouponPath,
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(testQuestClearCouponPVFSource())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{
		options:            options{gameUpperBodyCodec: gameUpperBodyCodecPlain},
		pvfItemCatalog:     catalog,
		questCatalog:       buildAutoCompleteMainBridgeCatalog(t),
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{
		conn:                &bufferConn{},
		connID:              "quest-clear-coupon",
		selectedCharacterID: 17,
	}
	return service, session, repositories
}

// TestCurrentQuestClearCouponCompletesMainStoryWithNoRewardAndConsumesOne 是
// "右键使用 10164630 ⇒ 自动完成到 Lv90 为止的主线任务（含 [sub] 子任务）、无奖励"
// 的端到端契约。
func TestCurrentQuestClearCouponCompletesMainStoryWithNoRewardAndConsumesOne(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	before, found, err := repositories.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}

	handled, err := service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(testQuestClearCouponSlot, 169))
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("op515 action 169 was not claimed by the completion-coupon handler")
	}

	stored, found, err := repositories.Quest.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load quest found=%t err=%v", found, err)
	}
	// 完成券是"整段主线"口径：申请的 100/106、**从未接过的**后续主线 101
	// （[level] 30、[pre required quest] 100）以及 102（[level] 90，正好等于券的
	// 上限 90）都会被写成 completed；150 作为 100 的 `[sub]` 子任务一并完成。
	for _, questID := range []int64{100, 101, 102, 106} {
		state := stored.States[questID]
		if state.Status != "completed" || state.ProgressValue != 0 {
			t.Fatalf("main story quest %d state=%+v, want completed/0", questID, state)
		}
		if state.Extra["reward_state"] != "granted" ||
			state.Extra["auto_complete_reason"] != "main_story_level_clear_no_reward" ||
			state.Extra["auto_complete_level_cutoff"] != "90" {
			t.Fatalf("main story quest %d extra=%+v", questID, state.Extra)
		}
	}
	if state := stored.States[101]; state.Extra["pvf_path"] != "n_quest/epic_next.qst" ||
		state.Extra["quest_type"] != "[meet npc]" {
		t.Fatalf("manufactured row must carry only the PVF identity: %+v", state.Extra)
	}
	// `[sub]` 闭包：150 的 [main quest] 是 100 ⇒ 100 被券清掉时 150 必须一并完成，
	// 否则客户端会保留"父已完成、子仍可接取"的死节点。
	if state := stored.States[150]; state.Status != "completed" ||
		state.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" ||
		state.Extra["main_quest_id"] != "100" || state.Extra["reward_state"] != "granted" {
		t.Fatalf("linked sub quest 150 state=%+v", state)
	}
	// 支线任务永远不在这条规则里，连行都不能被制造出来。
	if state, exists := stored.States[103]; exists {
		t.Fatalf("side quest row was manufactured: %+v", state)
	}

	after, found, err := repositories.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("completion coupon changed the character record (no-reward contract): before=%+v after=%+v", before, after)
	}

	inventory, found, err := repositories.Inventory.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if _, exists := inventory.Slots["0:87"]; exists {
		t.Fatalf("completion coupon was not consumed: %+v", inventory.Slots["0:87"])
	}

	stream := session.conn.(*bufferConn).write.Bytes()
	ack, rest := splitGameServerUpperPacket(t, stream)
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketUseStackableAction) ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("op515 ack header=%+v", ack.Header)
	}
	wantAck := []byte{1, 87, 0, 0, 169, 0, 0, 0}
	if !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("op515 ack body=%x want=%x", ack.Body, wantAck)
	}
	if len(rest) == 0 {
		t.Fatal("completion coupon success is missing the post-commit item/quest projections")
	}
}

// TestCurrentQuestClearCouponNoopKeepsTheCouponWhenNothingCanBeCleared 证明
// "没有可完成任务"时是幂等空操作，不消耗道具，但仍然回包（否则玩家看到的还是
// 最初那个"右键使用无反应"）。
func TestCurrentQuestClearCouponNoopKeepsTheCouponWhenNothingCanBeCleared(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 2)
	// 这条主线区间里的每条主线（含 102，[level] 90 = 券的上限）以及 100 的
	// `[sub]` 子任务 150 都已经完成 ⇒ 完成券无事可做。
	if err := repositories.Quest.Save(ctx, dnfrepo.QuestRecord{
		CharacterID: "17",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "completed"},
			101: {Status: "completed"},
			102: {Status: "completed"},
			106: {Status: "completed"},
			150: {Status: "completed"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	handled, err := service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(testQuestClearCouponSlot, 169))
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	for _, questID := range []int64{100, 101, 102, 106, 150} {
		state := stored.States[questID]
		if state.Status != "completed" || state.Extra["auto_complete_reason"] != "" {
			t.Fatalf("no-op coupon rewrote already-completed quest %d: %+v", questID, state)
		}
	}
	inventory, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if stack := inventory.Slots["0:87"]; stack.Count != 2 {
		t.Fatalf("no-op coupon was consumed: %+v", stack)
	}
	ack, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	wantAck := []byte{0, 17, 87, 0, 0, 169}
	if !bytes.Equal(ack.Body, wantAck) {
		t.Fatalf("no-op ack body=%x want=%x", ack.Body, wantAck)
	}
}

// TestCurrentQuestClearCouponRejectsForeignActionIndexAndForeignItem 覆盖两条
// fail-closed 边界：damage-font 的 action 164 必须继续走原路由；非完成券道具
// 落在该格时零写入、零回包。
func TestCurrentQuestClearCouponRejectsForeignActionIndexAndForeignItem(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)

	handled, err := service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(testQuestClearCouponSlot, 164))
	if err != nil || handled {
		t.Fatalf("damage-font action 164 must fall through: handled=%t err=%v", handled, err)
	}
	if len(session.conn.(*bufferConn).write.Bytes()) != 0 {
		t.Fatal("foreign action index produced a packet")
	}

	inventory, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	stack := inventory.Slots["0:87"]
	stack.ItemID = 22000001
	inventory.Slots["0:87"] = stack
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}

	handled, err = service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(testQuestClearCouponSlot, 169))
	if err != nil || !handled {
		t.Fatalf("foreign item must be claimed then rejected: handled=%t err=%v", handled, err)
	}
	if len(session.conn.(*bufferConn).write.Bytes()) != 0 {
		t.Fatal("foreign item produced a packet instead of failing closed")
	}
	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if state := stored.States[100]; state.Status != "active" || state.ProgressValue != 5 {
		t.Fatalf("foreign item mutated a quest row: %+v", state)
	}
	after, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if after.Slots["0:87"].Count != 1 {
		t.Fatalf("foreign item was consumed: %+v", after.Slots["0:87"])
	}
}

// TestCurrentQuestClearCouponAcceptsTheLevelClearFamilyThroughItsNativeAction
// 固定 "真档里 [lv clear] 只有一个成员且声明 90" 这个前提：本实现按与
// [gm quest clear] 完全相同的口径处理它（同一个 handler、同一个 planner、
// 同一个 cutoff = 券的 PVF 上限 90）。
func TestCurrentQuestClearCouponAcceptsTheLevelClearFamilyThroughItsNativeAction(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	inventory, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	stack := inventory.Slots["0:87"]
	stack.ItemID = 10301410
	inventory.Slots["0:87"] = stack
	if err := repositories.Inventory.Save(ctx, inventory); err != nil {
		t.Fatal(err)
	}
	handled, err := service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(testQuestClearCouponSlot, 179))
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if state := stored.States[100]; state.Status != "completed" {
		t.Fatalf("[lv clear] coupon did not complete the level-20 main story quest: %+v", state)
	}
}

func TestCurrentQuestClearCouponCutoffIsCappedAtTheRealPVFMainStoryCeiling(t *testing.T) {
	cases := []struct {
		level int
		want  int
	}{
		{level: 1, want: 0},
		{level: 2, want: 2},
		{level: 40, want: 40},
		{level: 89, want: 89},
		{level: 90, want: 90},
		{level: 200, want: 90},
	}
	for _, test := range cases {
		if got := dnfquest.CouponClearCutoff(test.level); got != test.want {
			t.Fatalf("CouponClearCutoff(%d)=%d want=%d", test.level, got, test.want)
		}
	}
	// 两条入口的上限来自各自的文案：券是 PVF 说明里的 90，任务手册"任务完成"
	// 按钮是客户端确认框按"等级-1"算出来的 89。
	if dnfquest.CouponMainQuestClearMaxLevel != 90 || currentAutoCompleteMainQuestMaxLevel != 89 {
		t.Fatalf("coupon ceiling=%d manual ceiling=%d, want 90/89",
			dnfquest.CouponMainQuestClearMaxLevel, currentAutoCompleteMainQuestMaxLevel)
	}
}

// TestCurrentQuestClearCouponDebugCommandIsObservedOnly 固定 op176 只读契约：
// 它被解析并记日志，但一个字节都不改（否则 `//lvclear` 就成了免费清任务后门）。
func TestCurrentQuestClearCouponDebugCommandIsObservedOnly(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	command := []byte("//lvclear 90")
	body := make([]byte, 4, 4+len(command))
	binary.LittleEndian.PutUint32(body[0:4], uint32(len(command)))
	body = append(body, command...)

	handled, err := service.handleCurrentQuestClearCouponDebugCommand(session, body)
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if state := stored.States[100]; state.Status != "active" || state.ProgressValue != 5 {
		t.Fatalf("op176 debug console mutated a quest row: %+v", state)
	}
	inventory, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Slots["0:87"].Count != 1 {
		t.Fatalf("op176 debug console consumed the coupon: %+v", inventory.Slots["0:87"])
	}
	if len(session.conn.(*bufferConn).write.Bytes()) != 0 {
		t.Fatal("op176 observation replied to the client")
	}
	level, matched := parseCurrentQuestClearCouponDebugCommand("  //lvclear   90 ")
	if !matched || level != 90 {
		t.Fatalf("parse lvclear level=%d matched=%t", level, matched)
	}
	if _, matched := parseCurrentQuestClearCouponDebugCommand("//lvclear"); matched {
		t.Fatal("bodyless lvclear must not match")
	}
}

// TestCurrentQuestClearCouponConsumptionIsFailClosedWithoutRepository Wires the
// bridge to a service whose repository provider is missing: the request must be
// claimed, logged, and leave zero writes and zero packets.
func TestCurrentQuestClearCouponConsumptionIsFailClosedWithoutRepository(t *testing.T) {
	service, session, _ := newQuestClearCouponTestService(t, 1)
	service.repositoryProvider = func() (dnfrepo.Group, bool) { return dnfrepo.Group{}, false }
	handled, err := service.handleCurrentQuestClearCouponUse(session, testQuestClearCouponRequestBody(testQuestClearCouponSlot, 169))
	if err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if len(session.conn.(*bufferConn).write.Bytes()) != 0 {
		t.Fatal("repository-less request produced a packet")
	}
}

func TestCurrentQuestClearCouponAckUsesNativeActionField(t *testing.T) {
	for _, action := range []uint32{169, 179} {
		got := buildCurrentQuestClearCouponAck(87, action)
		if !bytes.Equal(got, []byte{1, 87, 0, 0, byte(action), 0, 0, 0}) {
			t.Fatalf("native action %d ack=%x", action, got)
		}
	}
}
