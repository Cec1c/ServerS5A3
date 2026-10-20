package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/config"
)

// 本文件固定任务手册"任务完成"(op1429, ENUM_CMDPACKET_CLEAR_QUEST_TICKET) 的口径：
//
//   - 默认（DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP 未设置/为真）与客户端确认框
//     「自动完成 Lv89 为止的主线任务，且无任务奖励」一致，走整段主线口径；
//   - DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP=0 回退到"只完成当前激活主线"；
//   - **两种口径都不消耗任何实物券、不发任何奖励**——它是客户端免费按钮，
//     不是券。
const testQuestClearTicketSelectorZero = 0

// dispatchQuestClearTicket 走真实的 legacy 分发路径（handleGameCommand →
// gameplayModule 的 op1429 handler），与现场封包一致。
func dispatchQuestClearTicket(t *testing.T, service *Service, session *gameSession, selector uint32) {
	t.Helper()
	body := make([]byte, currentAutoCompleteMainQuestRequestSize)
	binary.LittleEndian.PutUint32(body, selector)
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketClearQuestTicket),
		body,
	); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentAutoCompleteMainQuestSweepsTheWholeMainStoryByDefault(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	if got := service.questClearTicketScope(); got != "main_story_sweep" {
		t.Fatalf("default quest clear ticket scope=%q", got)
	}
	beforeCharacter, found, err := repositories.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load character found=%t err=%v", found, err)
	}

	dispatchQuestClearTicket(t, service, session, testQuestClearTicketSelectorZero)

	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	// 100/106 是激活主线；101（[level] 30、[pre required quest] 100）玩家从没接过，
	// 也在整段主线口径里；150 是 100 的 `[sub]` 子任务，必须随父任务一并完成。
	// 102 是 [level] 90，超出按钮的 89 上限（券的上限才是 90）；103 是支线。
	for _, questID := range []int64{100, 101, 106} {
		state := stored.States[questID]
		if state.Status != "completed" || state.ProgressValue != 0 {
			t.Fatalf("quest %d state=%+v, want completed/0", questID, state)
		}
		if state.Extra["reward_state"] != "granted" ||
			state.Extra["auto_complete_reason"] != "main_story_level_clear_no_reward" ||
			state.Extra["auto_complete_level_cutoff"] != "89" {
			t.Fatalf("quest %d extra=%+v", questID, state.Extra)
		}
	}
	if state := stored.States[150]; state.Status != "completed" ||
		state.Extra["auto_complete_reason"] != "main_quest_swept_sub_closure_no_reward" ||
		state.Extra["main_quest_id"] != "100" || state.Extra["reward_state"] != "granted" {
		t.Fatalf("linked sub quest 150 state=%+v", state)
	}
	if state := stored.States[102]; state.Status != "active" || state.ProgressValue != 1 {
		t.Fatalf("level-90 epic must stay active: %+v", state)
	}
	if state, exists := stored.States[103]; exists {
		t.Fatalf("side quest row was manufactured: %+v", state)
	}

	afterCharacter, found, err := repositories.Character.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("reload character found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(beforeCharacter, afterCharacter) {
		t.Fatalf("task-manual completion changed the character record (no-reward contract):\nbefore=%+v\nafter=%+v",
			beforeCharacter, afterCharacter)
	}

	ack, rest := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if ack.Header.MsgID != uint16(dnfenum.CmdPacketClearQuestTicket) ||
		ack.Header.Classification != dnfproto.DefaultChannelClassification ||
		len(ack.Body) != currentAutoCompleteMainQuestResponseSize {
		t.Fatalf("op1429 ack header=%+v body=%x", ack.Header, ack.Body)
	}
	if got := binary.LittleEndian.Uint32(ack.Body[0:4]); got != testQuestClearTicketSelectorZero {
		t.Fatalf("request selector echo=%d", got)
	}
	if got := binary.LittleEndian.Uint32(ack.Body[4:8]); got != 106 {
		t.Fatalf("highest completed quest=%d want=106", got)
	}
	if got := binary.LittleEndian.Uint32(ack.Body[8:12]); got != 89 || ack.Body[12] != 1 {
		t.Fatalf("cutoff/committed body=%x", ack.Body)
	}
	if len(rest) == 0 {
		t.Fatal("task-manual completion is missing the post-commit quest snapshots")
	}
}

// TestCurrentAutoCompleteMainQuestTaskManualButtonConsumesNoCoupon 是本轮新增的
// 硬断言：op1429 是**客户端的免费按钮**，不是实物券，因此它一个物品字节都不能动。
// 背包里那张 10164630 必须原样留在 0:87（连整条 InventoryRecord 都要逐字段相同）。
func TestCurrentAutoCompleteMainQuestTaskManualButtonConsumesNoCoupon(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	before, found, err := repositories.Inventory.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	coupon := before.Slots["0:87"]
	if coupon.ItemID != testQuestClearCouponItemID || coupon.Count != 1 {
		t.Fatalf("fixture coupon=%+v", coupon)
	}

	dispatchQuestClearTicket(t, service, session, testQuestClearTicketSelectorZero)

	after, found, err := repositories.Inventory.Load(ctx, "17")
	if err != nil || !found {
		t.Fatalf("reload inventory found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("op1429 mutated the inventory record:\nbefore=%+v\nafter=%+v", before, after)
	}
	if stack := after.Slots["0:87"]; stack.ItemID != testQuestClearCouponItemID || stack.Count != 1 {
		t.Fatalf("op1429 consumed or changed the coupon: %+v", stack)
	}
	// 背包列表刷新都不该出现（op13/op14 = CmdPacketWalkoutPartyMember 家族）：
	// 物品没变就没有可刷新的行。
	stream := session.conn.(*bufferConn).write.Bytes()
	for len(stream) > 0 {
		packet, next := splitGameServerUpperPacket(t, stream)
		stream = next
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketWalkoutPartyMember) {
			t.Fatalf("op1429 sent an item-container refresh: msg_id=%d body=%x", packet.Header.MsgID, packet.Body)
		}
	}
}

func TestCurrentAutoCompleteMainQuestFallsBackToActiveOnlyWhenTheSwitchIsOff(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	service.options.questClearTicketActiveOnly = true
	if got := service.questClearTicketScope(); got != "active_only" {
		t.Fatalf("rollback scope=%q", got)
	}
	beforeInventory, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}

	dispatchQuestClearTicket(t, service, session, testQuestClearTicketSelectorZero)

	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	for _, questID := range []int64{100, 106} {
		if state := stored.States[questID]; state.Status != "completed" {
			t.Fatalf("active quest %d state=%+v", questID, state)
		}
	}
	if state, exists := stored.States[101]; exists {
		t.Fatalf("active-only switch manufactured quest 101: %+v", state)
	}
	if state := stored.States[102]; state.Status != "active" {
		t.Fatalf("out-of-range epic was touched: %+v", state)
	}
	afterInventory, _, err := repositories.Inventory.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeInventory, afterInventory) {
		t.Fatalf("rollback path mutated the inventory:\nbefore=%+v\nafter=%+v", beforeInventory, afterInventory)
	}
	ack, _ := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if got := binary.LittleEndian.Uint32(ack.Body[4:8]); got != 106 || ack.Body[12] != 1 {
		t.Fatalf("rollback ack body=%x", ack.Body)
	}
}

// TestCurrentAutoCompleteMainQuestNothingToCompleteKeepsTheCommittedZeroShape 固定
// 2026-09-25 现场第二次点击的回包形状：selector 原样回填、highest=0、committed=0，
// 不再补发任务快照，且库里的行一个字节都不动。
func TestCurrentAutoCompleteMainQuestNothingToCompleteKeepsTheCommittedZeroShape(t *testing.T) {
	ctx := context.Background()
	service, session, repositories := newQuestClearCouponTestService(t, 1)
	completed := dnfrepo.QuestRecord{
		CharacterID: "17",
		States: map[int64]dnfrepo.QuestState{
			100: {Status: "completed"},
			101: {Status: "completed"},
			102: {Status: "active", ProgressValue: 1},
			105: {Status: "completed"},
			106: {Status: "completed"},
			// 150 是 100 的 `[sub]`：它没完成时 sweep 会把它补完，所以"真的一件都
			// 不剩"必须把它也算上（否则这条用例测的是自愈而不是 no-op 回包形状）。
			150: {Status: "completed"},
		},
	}
	if err := repositories.Quest.Save(ctx, completed); err != nil {
		t.Fatal(err)
	}

	dispatchQuestClearTicket(t, service, session, testQuestClearTicketSelectorZero)

	stored, _, err := repositories.Quest.Load(ctx, "17")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, completed) {
		t.Fatalf("nothing-to-complete wrote to the quest record:\nbefore=%+v\nafter=%+v", completed, stored)
	}
	ack, trailing := splitGameServerUpperPacket(t, session.conn.(*bufferConn).write.Bytes())
	if len(ack.Body) != currentAutoCompleteMainQuestResponseSize ||
		binary.LittleEndian.Uint32(ack.Body[0:4]) != testQuestClearTicketSelectorZero ||
		binary.LittleEndian.Uint32(ack.Body[4:8]) != 0 ||
		binary.LittleEndian.Uint32(ack.Body[8:12]) != 89 ||
		ack.Body[12] != 0 {
		t.Fatalf("nothing-to-complete ack body=%x", ack.Body)
	}
	if len(trailing) != 0 {
		t.Fatalf("nothing-to-complete must not replay quest snapshots: trailing=%x", trailing)
	}
}

func TestQuestClearTicketScopeSwitchReadsTheEnvironment(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: "main_story_sweep"},
		{value: "true", want: "main_story_sweep"},
		{value: "1", want: "main_story_sweep"},
		{value: "yes", want: "main_story_sweep"},
		{value: "on", want: "main_story_sweep"},
		{value: "false", want: "active_only"},
		{value: "0", want: "active_only"},
		{value: "no", want: "active_only"},
		{value: "off", want: "active_only"},
	} {
		t.Run("DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP="+test.value, func(t *testing.T) {
			t.Setenv("DNFBRIDGE_QUEST_CLEAR_TICKET_SWEEP", test.value)
			service := &Service{options: optionsFromConfig(config.ServiceConfig{})}
			if got := string(service.questClearTicketScope()); got != test.want {
				t.Fatalf("scope=%q want=%q", got, test.want)
			}
		})
	}
	// 零值 options 是测试与探针里的构造方式：它必须保持线上默认的 sweep 口径。
	if got := string((&Service{}).questClearTicketScope()); got != "main_story_sweep" {
		t.Fatalf("zero-value options scope=%q", got)
	}
	if got := string((*Service)(nil).questClearTicketScope()); got != "main_story_sweep" {
		t.Fatalf("nil service scope=%q", got)
	}
	if got := string((&Service{options: options{questClearTicketActiveOnly: true}}).questClearTicketScope()); got != "active_only" {
		t.Fatalf("explicit rollback scope=%q", got)
	}
}
