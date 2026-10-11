package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

// 批量（多件）装备分解的聚焦回归。契约来自 HK 实服 packet_log.*.txt 的全部 61 条
// type=26 RECV（27 条批量 + 34 条 9 字节单件），逐条严格解析：
//
//   - 单件：`i16 slot | u8 list=0 | i16 tool | u32 context`（5 或 9 字节体）。
//   - 批量：`i16 0xFFFF | u8 list=0 | i16 0xFFFF | u32 context | 42 32 36 01 |
//     u8 count | count × u16 slot`，长度恒等于 14+2×count。
//   - 27/27 条批量的 4 字节标签恒为 42 32 36 01，且该字节串在全库只出现这 27 次、
//     从不出现在任何非 op26 封包；同一批里 context 有 317/1000 两种取值而标签不变
//     ⇒ 类型标签，不是校验位/数量/掩码。
//   - 槽位列表按客户端勾选顺序（非升序）：3 件样本 = 49,51,50。
//
// 操作者现场交叉验证（2026-09-25）：确认框自述「共4个道具」、界面「阿拉德分解修理机」
// （= 商店 317）；dnf:10006 / character_id=4 主背包当时只剩槽 9/11/12/13 四个可分解
// 装备 ⇒ 与 `04 0900 0b00 0c00 0d00` 逐字节吻合。
//
// 【已废弃】上一轮把尾部读成"9 字节 / 72 位掩码（bit i = 槽 i）"的
// `mask_hex/mask_bit_count/mask_slots_candidate` 读法已从实现里删除，不再有任何用例
// 依赖它。

// currentDisjointBatchLiveBodies 是现场全部批量体的原文（body，去掉 13 字节头），
// 覆盖 16..62 字节、件数 1..24 的每一种现场长度。最后一条是操作者 2026-09-25 复现的
// 4 件样本（22 字节）。
func currentDisjointBatchLiveBodies() []string {
	return []string{
		"ffff00ffff3d010000423236010209000a00",                                                                                         // 18B, 2 件, 槽 9,10
		"ffff00ffff3d01000042323601020c000e00",                                                                                         // 18B, 2 件, 槽 12,14
		"ffff00ffff3d010000423236010211001200",                                                                                         // 18B, 2 件, 槽 17,18
		"ffff00ffff3d0100004232360103310033003200",                                                                                     // 20B, 3 件, 槽 49,51,50
		"ffff00ffffe803000042323601030c000a000b00",                                                                                     // 20B, 3 件, ctx=1000
		"ffff00ffff3d010000423236010409000b000c000d00",                                                                                 // 22B, 4 件, 槽 9,11,12,13（操作者复现）
		"ffff00ffff3d01000042323601040c000a000b000d00",                                                                                 // 22B, 4 件, 槽 12,10,11,13
		"ffff00ffff3d010000423236010b0a001600170009000b000c000d000e000f0010001800",                                                     // 36B, 11 件
		"ffff00ffff3d010000423236010c15000a001700160009000b000c000d000e000f0010001800",                                                 // 38B, 12 件
		"ffff00ffff3d010000423236010d0a001600170009000b000c000d000e000f001000130014001800",                                             // 40B, 13 件
		"ffff00ffff3d01000042323601100c000e000f00110013001400150017001b000a000b000d001000160018001a00",                                 // 46B, 16 件
		"ffff00ffff3d010000423236011109000c000e000f00110013001400150017001b000a000b000d001000160018001a00",                             // 48B, 17 件
		"ffff00ffff3d010000423236011309000c000e000f001100130014001500170019001b001c000a000b000d001000160018001a00",                     // 52B, 19 件
		"ffff00ffff3d01000042323601140e00100016001700190039003a003b000b000d001200130009000a000c000f001100140015001800",                 // 54B, 20 件
		"ffff00ffff3d010000423236011809000d0015001c001d001e0039003a000a000c001400160017000b000e000f00100011001200130018001a001b002000", // 62B, 24 件
	}
}

// currentDisjointBatchBody 按契约拼一条合法批量体（n>=1）。
func currentDisjointBatchBody(slots []int16, toolSlot int16, contextValue uint32) []byte {
	body := make([]byte, currentDisjointBatchMinBodySize-2+2*len(slots))
	headerSlot := currentDisjointBatchHeaderSlot
	binary.LittleEndian.PutUint16(body[0:2], uint16(headerSlot))
	body[2] = dnfrepo.MainInventoryListType
	binary.LittleEndian.PutUint16(body[3:5], uint16(toolSlot))
	binary.LittleEndian.PutUint32(body[5:9], contextValue)
	copy(body[9:13], currentDisjointBatchTag[:])
	body[13] = byte(len(slots))
	for index, slot := range slots {
		binary.LittleEndian.PutUint16(body[14+index*2:16+index*2], uint16(slot))
	}
	return body
}

// (a) 现场全部 27 条批量体的形状契约：长度 14+2n、标签恒定、件数与列表自洽，
// 且件数/槽位与客户端勾选自述一致。
func TestCurrentDisjointBatchLiveBodiesDecodeByCountAndSlotList(t *testing.T) {
	for _, text := range currentDisjointBatchLiveBodies() {
		body, err := hex.DecodeString(text)
		if err != nil {
			t.Fatal(err)
		}
		if !currentDisjointBodyIsBatchShape(body) {
			t.Fatalf("live batch body not routed to the batch branch: %s", text)
		}
		batch, err := decodeCurrentDisjointBatchRequest(body)
		if err != nil {
			t.Fatalf("live batch body rejected: %s err=%v", text, err)
		}
		count := int(body[13])
		if len(body) != 14+2*count || len(batch.Slots) != count {
			t.Fatalf("body=%s count=%d decoded=%d", text, count, len(batch.Slots))
		}
		if batch.ToolSlot != -1 {
			t.Fatalf("body=%s tool=%d want -1", text, batch.ToolSlot)
		}
		if batch.Context != 0x13d && batch.Context != 0x3e8 {
			t.Fatalf("body=%s context=%d want 317/1000", text, batch.Context)
		}
		// 槽位必须逐字节等于请求体尾部。
		for index, slot := range batch.Slots {
			if want := int16(binary.LittleEndian.Uint16(body[14+index*2 : 16+index*2])); slot != want {
				t.Fatalf("body=%s slot[%d]=%d want %d", text, index, slot, want)
			}
		}
	}
	// 操作者复现的 4 件样本逐字节：件数=4、槽位=9,11,12,13。
	live, err := hex.DecodeString("ffff00ffff3d010000423236010409000b000c000d00")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := decodeCurrentDisjointBatchRequest(live)
	if err != nil {
		t.Fatal(err)
	}
	if batch.SlotListText() != "9,11,12,13" {
		t.Fatalf("operator sample slots=%s want 9,11,12,13", batch.SlotListText())
	}
	// 3 件样本证明列表是勾选顺序而不是升序。
	unordered, err := hex.DecodeString("ffff00ffff3d0100004232360103310033003200")
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := decodeCurrentDisjointBatchRequest(unordered); err != nil {
		t.Fatal(err)
	} else if decoded.SlotListText() != "49,51,50" {
		t.Fatalf("selection order lost: %s", decoded.SlotListText())
	}
}

// (b) 畸形批量体一律回有界失败 19 且零消耗：件数与长度不符、件数 0、标签被改、
// 槽位为 0/账号共享槽/重复。
func TestCurrentDisjointBatchRejectsMalformedShapeWithoutConsuming(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, 60): {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, 62): {ItemID: 5000, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	service.packetLog = logger
	session := &gameSession{conn: connection, selectedCharacterID: 19}

	valid := currentDisjointBatchBody([]int16{60, 62}, -1, 0x13d)
	shortByTwo := append([]byte(nil), valid[:len(valid)-2]...)
	shortByTwo[13] = 2 // 件数说 2，长度只剩 1 个槽位
	tagChanged := append([]byte(nil), valid...)
	tagChanged[9] = 0x43
	zeroCount := currentDisjointBatchBody([]int16{60}, -1, 0x13d)
	zeroCount = zeroCount[:14]
	zeroCount[13] = 0
	zeroSlot := currentDisjointBatchBody([]int16{0, 62}, -1, 0x13d)
	sharedSlot := currentDisjointBatchBody([]int16{60, dnfrepo.CrystalWarehouseFirstSlot}, -1, 0x13d)
	repeatedSlot := currentDisjointBatchBody([]int16{60, 60}, -1, 0x13d)

	for name, body := range map[string][]byte{
		"count_length_mismatch": shortByTwo,
		"tag_changed":           tagChanged,
		"zero_count":            zeroCount,
		"zero_slot":             zeroSlot,
		"account_shared_slot":   sharedSlot,
		"repeated_slot":         repeatedSlot,
	} {
		if !currentDisjointBodyIsBatchShape(body) {
			t.Fatalf("%s: body must still route to the batch branch (%d bytes)", name, len(body))
		}
		if err := service.handleCurrentDisjointItem(session, body); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
		if len(trailing) != 0 || !bytes.Equal(failure.Body, []byte{0, currentDisjointFailureInvalid}) {
			t.Fatalf("%s: ACK=%x trailing=%d want the bounded 00 13", name, failure.Body, len(trailing))
		}
		connection.write.Reset()
	}
	// 畸形批量体不得消耗任何来源。
	inventory, _, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []int16{60, 62} {
		stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]
		if !exists || stack.ItemID != 5000 || stack.Count != 1 {
			t.Fatalf("malformed batch consumed slot %d: %+v exists=%t", slot, stack, exists)
		}
	}
	if len(inventory.Slots) != 2 {
		t.Fatalf("malformed batch mutated the inventory: %+v", inventory.Slots)
	}
	if account, _, _ := repositories.AccountInventory.Load(context.Background(), "account-1"); len(account.Slots) != 0 {
		t.Fatalf("malformed batch granted rewards: %+v", account.Slots)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, want := range []string{
		"EVENT kind=game-disjoint-item-batch-rejected",
		"reason=dnf current disjoint request is invalid: batch op26 tag=",
		"batch op26 count=0",
		"batch op26 repeated slot=60",
		"batch op26 slot=0 index=0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("batch rejection log missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "kind=game-disjoint-item-batch-observed") {
		t.Fatalf("retired mask-observation log still emitted:\n%s", text)
	}
}

// (c) 4 件批量（n=4，22 字节体）：单事务逐件消耗、每件一条单件成功 ACK（回显该件
// 自己的来源槽）、产物合并堆叠进同一材料行、账号晶块/灵魂仓累计正确。
func TestCurrentDisjointBatchConsumesEverySlotInOneTransaction(t *testing.T) {
	const (
		coatSlot  = int16(60) // 5000 valued_coat.equ → 14 无色小晶块
		titleSlot = int16(61) // 5200 no_value_title.equ → 1 无色小晶块（无 [value] 兜底）
		coatSlot2 = int16(62)
		title2    = int16(63)
	)
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, coatSlot):  {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, titleSlot): {ItemID: 5200, Count: 1},
		currentCeraShopInventorySlotKey(0, coatSlot2): {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, title2):    {ItemID: 5200, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{conn: connection, selectedCharacterID: 19}

	// 现场那条 22 字节体的同形请求（槽位用测试资产里的 60/61/62/63）。
	body := currentDisjointBatchBody([]int16{coatSlot, titleSlot, coatSlot2, title2}, -1, 0x13d)
	if len(body) != 22 {
		t.Fatalf("4-item batch body=%d bytes want 22", len(body))
	}
	if err := service.handleCurrentDisjointItem(session, body); err != nil {
		t.Fatal(err)
	}
	acks, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	// 【2026-09-29 断言变更】旧断言是 `len(acks)==4`（每件一条单件成功体）。
	// 它不再正确：运维现场判定「批量分解完成后发放的产出是一件一件发过来的，
	// 需要统一在一个框，不然分解多样物品会有很多次弹窗」——默认档已改成
	// `single_aggregate_ack`（**一条** ACK，行 = 整批产物按 (槽,物品) 合并后的
	// 本次获得总量）。旧形状没有被删除：`per_item_single_ack_refresh` 仍是
	// 逐字节回退档，由 TestCurrentDisjointBatchAckShapeRollback* 两条用例钉住。
	if len(acks) != 1 {
		t.Fatalf("batch ACKs=%d want 1 (single_aggregate_ack: one popup for the whole batch)", len(acks))
	}
	if len(slotRefreshes) != 1 || len(containers) != 0 {
		t.Fatalf("batch refresh=op14 %d / op13 %d want 1/0 (default ack_shape refreshes the touched slots)", len(slotRefreshes), len(containers))
	}
	// 唯一那条 ACK 回显本批第一件（请求里真实存在过的槽位），行 = 整批合并。
	aggregate := acks[0]
	if aggregate.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(aggregate.Body) == 0 || aggregate.Body[0] != 1 {
		t.Fatalf("aggregate ack=%x header=%+v", aggregate.Body, aggregate.Header)
	}
	if source := int16(binary.LittleEndian.Uint16(aggregate.Body[1:3])); source != coatSlot {
		t.Fatalf("aggregate ack echoed slot %d want %d (first decomposed slot)", source, coatSlot)
	}
	if aggregate.Body[3] != 0 || int16(binary.LittleEndian.Uint16(aggregate.Body[4:6])) != -1 {
		t.Fatalf("aggregate ack list/tool=%d/%d want 0/-1", aggregate.Body[3], int16(binary.LittleEndian.Uint16(aggregate.Body[4:6])))
	}
	rows := parseCurrentDisjointRewardRows(t, aggregate.Body)
	if len(rows) != 3 {
		t.Fatalf("aggregate rows=%+v want 3 (cube + element + soul)", rows)
	}
	elementSlot := int16(-1)
	// 晶块：四件各 14/1/14/1 ⇒ 合并成一行 granted 30（这正是"一个框里的一行"）。
	cube := currentDisjointRewardRowForItem(t, rows, 3037)
	if cube.Slot != 358 || cube.Granted != 30 {
		t.Fatalf("aggregate cube row=%+v want slot 358 granted 30 (14+1+14+1 merged)", cube)
	}
	// 元素结晶：四件各 1 ⇒ 合并成一行 granted 4。
	if element := currentDisjointRewardRowForItem(t, rows, 3166); element.Granted != 4 {
		t.Fatalf("aggregate element row=%+v want granted 4 (1+1+1+1 merged)", element)
	} else {
		elementSlot = element.Slot
	}
	// 灵魂：四件各 1 ⇒ 合并成一行 granted 4。
	if soul := currentDisjointRewardRowForItem(t, rows, 10099773); soul.Slot != 362 || soul.Granted != 4 {
		t.Fatalf("aggregate soul row=%+v want slot 362 granted 4 (1+1+1+1 merged)", soul)
	}

	ctx := context.Background()
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("inventory found=%t err=%v", found, err)
	}
	for _, source := range []int16{coatSlot, titleSlot, coatSlot2, title2} {
		if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, source)]; exists {
			t.Fatalf("source slot %d survived the batch: %+v", source, stack)
		}
	}
	// 元素结晶：4 件各 1 个，必须并进同一行（不占 4 格）。
	rowsWithElement := 0
	for _, stack := range inventory.Slots {
		if stack.ItemID == 3166 {
			rowsWithElement++
		}
	}
	if rowsWithElement != 1 {
		t.Fatalf("batch used %d material rows for the element crystals, want 1 merged row", rowsWithElement)
	}
	if stack := inventory.Slots[currentCeraShopInventorySlotKey(0, elementSlot)]; stack.ItemID != 3166 || stack.Count != 4 {
		t.Fatalf("material slot %d=%+v want 3166x4", elementSlot, stack)
	}
	account, _, err := repositories.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != 30 {
		t.Fatalf("crystal warehouse 358=%+v want 3037x30 (14+1+14+1)", stack)
	}
	if stack := account.Slots["0:362"]; stack.ItemID != 10099773 || stack.Count != 4 {
		t.Fatalf("soul warehouse 362=%+v want 10099773x4", stack)
	}
	if len(account.Slots) != 2 {
		t.Fatalf("batch touched unexpected warehouse slots: %+v", account.Slots)
	}
}

// (d) n=1 与 n=2 的批量体同样按批量路径成交（n=1 是合法批量，不是单件体）。
func TestCurrentDisjointBatchAcceptsSingleAndTwoItemLists(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, 60): {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, 61): {ItemID: 5200, Count: 1},
	}
	ctx := context.Background()
	for _, count := range []int{1, 2} {
		repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
		seedCurrentDisjointAccount(t, repositories, nil)
		connection := &bufferConn{}
		service := currentDisjointTestService(repositories, catalog)
		requested := []int16{60}
		if count == 2 {
			requested = []int16{60, 61}
		}
		body := currentDisjointBatchBody(requested, -1, 0x13d)
		if len(body) != 14+2*count {
			t.Fatalf("n=%d body=%d bytes want %d", count, len(body), 14+2*count)
		}
		if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
			t.Fatal(err)
		}
		// 【2026-09-29 断言变更】旧断言是 `len(acks)==count`（每件一条 ACK）。
		// 默认档改成 single_aggregate_ack 之后，n=1 与 n=2 都只回**一条** ACK
		// （n=1 时聚合行就等于那一件的行；op14 刷新仍然一条包内多行）。
		if acks, slotRefreshes, _ := splitCurrentDisjointBatchReplies(t, connection.write.Bytes()); len(acks) != 1 || len(slotRefreshes) != 1 {
			t.Fatalf("n=%d ACKs=%d op14=%d want 1/1 (single_aggregate_ack)", count, len(acks), len(slotRefreshes))
		}
		inventory, _, _ := repositories.Inventory.Load(ctx, "19")
		for _, slot := range requested {
			if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]; exists {
				t.Fatalf("n=%d source slot %d survived: %+v", count, slot, stack)
			}
		}
	}
}

// (e) 任一槽不合法 ⇒ 整批拒绝、零消耗（不部分成功），且日志带 offending slot。
func TestCurrentDisjointBatchRejectsWholeListWhenOneSlotIsIllegal(t *testing.T) {
	const (
		coatSlot  = int16(60) // 合法：5000
		emptySlot = int16(61) // 非法：槽里没有东西
		titleSlot = int16(62) // 合法：5200
	)
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, coatSlot):  {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, titleSlot): {ItemID: 5200, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	ctx := context.Background()

	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger

	body := currentDisjointBatchBody([]int16{coatSlot, emptySlot, titleSlot}, -1, 0x13d)
	if err := service.handleCurrentDisjointItem(session, body); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(failure.Body, []byte{0, currentDisjointFailureInvalid}) {
		t.Fatalf("illegal-member batch ACK=%x trailing=%d want the bounded 00 13", failure.Body, len(trailing))
	}
	// 整批零消耗：合法的那两件也必须原样还在，账号仓一个字节没动。
	inventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	for slot, want := range map[int16]int64{coatSlot: 5000, titleSlot: 5200} {
		stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]
		if !exists || stack.ItemID != want || stack.Count != 1 {
			t.Fatalf("whole-batch rejection still consumed slot %d: %+v exists=%t", slot, stack, exists)
		}
	}
	if len(inventory.Slots) != 2 {
		t.Fatalf("whole-batch rejection mutated the inventory: %+v", inventory.Slots)
	}
	account, _, _ := repositories.AccountInventory.Load(ctx, "account-1")
	if len(account.Slots) != 0 {
		t.Fatalf("whole-batch rejection granted rewards: %+v", account.Slots)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, want := range []string{
		"EVENT kind=game-disjoint-item-batch-rejected",
		"count=3",
		"slots=60,61,62",
		"slot=61",
		"reason=slot=61: dnf current disjoint source item is invalid",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("whole-batch rejection log missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "kind=game-disjoint-item-batch-committed") {
		t.Fatalf("rejected batch logged as committed:\n%s", text)
	}
	// 单件路径未被批量分支污染：同一个空槽单独请求仍然是"只有那一件回 19"。
	connection.write.Reset()
	if err := service.handleCurrentDisjointItem(session, currentDisjointItemTestBody(emptySlot, 0, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	single, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(single.Body, []byte{0, currentDisjointFailureInvalid}) {
		t.Fatalf("single-item ACK=%x trailing=%d", single.Body, len(trailing))
	}
}

// (f0) 批量里混进时装（`[hat avatar]`）⇒ 整批拒绝、零消耗：装备 op26 的"不是装扮"
// 判据与单件路径共用同一份 resolver，不允许"跳过装扮、其余照分解"。
func TestCurrentDisjointBatchRejectsWholeListWhenAnAvatarIsSelected(t *testing.T) {
	const (
		coatSlot   = int16(60) // 5000 普通装备（可分解）
		avatarSlot = int16(64) // 5100 `[hat avatar]`：装备分解必须拒绝
	)
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, coatSlot):   {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, avatarSlot): {ItemID: 5100, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	ctx := context.Background()

	if err := service.handleCurrentDisjointItem(session, currentDisjointBatchBody([]int16{coatSlot, avatarSlot}, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	failure, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(failure.Body, []byte{0, currentDisjointFailureInvalid}) {
		t.Fatalf("avatar-in-batch ACK=%x trailing=%d want the bounded 00 13", failure.Body, len(trailing))
	}
	inventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, coatSlot)]; !exists || stack.ItemID != 5000 || stack.Count != 1 {
		t.Fatalf("avatar-in-batch consumed the legal coat: %+v exists=%t", stack, exists)
	}
	if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, avatarSlot)]; !exists || stack.ItemID != 5100 {
		t.Fatalf("avatar-in-batch consumed the avatar: %+v exists=%t", stack, exists)
	}
	if account, _, _ := repositories.AccountInventory.Load(ctx, "account-1"); len(account.Slots) != 0 {
		t.Fatalf("avatar-in-batch granted rewards: %+v", account.Slots)
	}
	// 时装走 op202（currentAvatarDisjointRewards）不是这条链路，装备 op26 的单件路径
	// 同样必须拒绝它。
	connection.write.Reset()
	if err := service.handleCurrentDisjointItem(session, currentDisjointItemTestBody(avatarSlot, 0, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	single, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(single.Body, []byte{0, currentDisjointFailureInvalid}) {
		t.Fatalf("single avatar op26 ACK=%x trailing=%d want 00 13", single.Body, len(trailing))
	}
}

// (f) 重复提交已消耗的整批：整批回 19、零消耗、不重复发放。
func TestCurrentDisjointBatchReplayIsRejectedWithoutDoubleGrant(t *testing.T) {
	const (
		coatSlot  = int16(60)
		titleSlot = int16(62)
	)
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, coatSlot):  {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, titleSlot): {ItemID: 5200, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	ctx := context.Background()
	body := currentDisjointBatchBody([]int16{coatSlot, titleSlot}, -1, 0x13d)

	if err := service.handleCurrentDisjointItem(session, body); err != nil {
		t.Fatal(err)
	}
	// 【2026-09-29 断言变更】旧断言是 `len(acks)==2`（每件一条 ACK）；默认档
	// single_aggregate_ack 只回一条。这条用例真正要钉的是"重放整批被拒、不重复
	// 发放"，下面那些断言（零刷新行、回 0x13、仓库数量不翻倍）逐条未变。
	if acks, slotRefreshes, _ := splitCurrentDisjointBatchReplies(t, connection.write.Bytes()); len(acks) != 1 || len(slotRefreshes) != 1 {
		t.Fatalf("first batch ACKs=%d op14=%d want 1/1 (single_aggregate_ack)", len(acks), len(slotRefreshes))
	}
	connection.write.Reset()

	// 同一条请求再发一次：来源槽已经不存在 ⇒ 整批 19，且不产生第二份产物。
	if err := service.handleCurrentDisjointItem(session, body); err != nil {
		t.Fatal(err)
	}
	// 整批拒绝时**一个刷新行都不能发**（零消耗就不该动界面）。
	if _, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes()); len(slotRefreshes) != 0 || len(containers) != 0 {
		t.Fatalf("rejected replay emitted refresh rows: op14 %d / op13 %d", len(slotRefreshes), len(containers))
	}
	replay, trailing := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(trailing) != 0 || !bytes.Equal(replay.Body, []byte{0, currentDisjointFailureInvalid}) {
		t.Fatalf("replayed batch ACK=%x trailing=%d want the bounded 00 13", replay.Body, len(trailing))
	}
	account, _, err := repositories.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != 15 {
		t.Fatalf("crystal warehouse 358=%+v want 3037x15 (no double grant)", stack)
	}
	if stack := account.Slots["0:362"]; stack.ItemID != 10099773 || stack.Count != 2 {
		t.Fatalf("soul warehouse 362=%+v want 10099773x2 (no double grant)", stack)
	}
	inventory, _, _ := repositories.Inventory.Load(ctx, "19")
	for _, slot := range []int16{coatSlot, titleSlot} {
		if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]; exists {
			t.Fatalf("consumed source slot %d came back: %+v", slot, stack)
		}
	}
}

// (g) 成功日志：count/slots/reward_rows/units/ack_shape 全部落盘，供操作者复测判读。
func TestCurrentDisjointBatchCommittedLogReportsAckShape(t *testing.T) {
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, 60): {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, 62): {ItemID: 5000, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19},
		currentDisjointBatchBody([]int16{60, 62}, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, want := range []string{
		"EVENT kind=game-disjoint-item-batch-committed",
		"count=2",
		"slots=60,62",
		"reward_rows=6",
		"units=32", // (14+1+1)×2
		"tool_slot=-1",
		"context=317",
		// 上一轮的 ack_shape=per_item_single_ack / per_item_single_ack_refresh 已被
		// 现场判定为"客户端一条都不采纳 + 每件各弹一次获得框"；默认档改成
		// "**一条**聚合 ACK + op14 受影响槽位刷新"，并在日志里标出条数。
		// 【2026-09-29 断言变更】这两个期望值随默认档一起改；旧的
		// `ack_shape=per_item_single_ack_refresh`/`ack_count=2` 由回退档用例钉住
		// （TestCurrentDisjointBatchAckShapeRollbackSendsPerItemAcksWithRefresh）。
		"ack_shape=single_aggregate_ack",
		"ack_count=1",
		// 【2026-09-29 断言变更（第二次）】刷新行数从 5 改成 2。
		// 旧值 5 = 2 个来源槽 + 3 个产物格（晶块仓 358、灵魂仓 362、主背包材料段元素
		// 结晶一格）。默认档 DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE=source_only 之后，
		// 产物**只**由聚合 ACK 的"本次获得"行承载（与单件分解路径同构：那条路径一个
		// 字节的 op14 都不发），op14 只负责清空被消耗的来源槽 ⇒ 行数 = 来源槽数 = 2。
		// 理由与逐帧证据见 disjoint_compound_protocol.go 的开关注释；旧字节形状由
		// TestCurrentDisjointBatchLegacyRefreshShapeIsTheByteExactRollback 钉住。
		"refresh_shape=source_only",
		"refresh_rows=2",
		"refresh_lists=0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("batch commit log missing %q in:\n%s", want, text)
		}
	}
}

// maxCurrentDisjointRewardUnits 给出"同一件来源里某产物 id 的数量上界"：解析结果里
// 该 id 每次 roll 的数量之和；解析结果里没有该 id 时退化为 1（真档的随机 roll 可能
// 产出解析快照里未出现的同族产物，例如随机掷出的有色小晶块）。
func maxCurrentDisjointRewardUnits(rewards []currentDisjointReward, itemID uint32) int64 {
	var total int64
	for _, reward := range rewards {
		if reward.ItemID == itemID {
			total += int64(reward.Count)
		}
	}
	if total <= 0 {
		return 1
	}
	return total
}

// (h) 真实 PVF：用运行时 Script.pvf 造一条两件批量请求（槽 9/10，与现场那条 18 字节
// 批量体逐字节同形），两件都是真档里存在的普通装备（400330100 是无 [value]/[price] 的
// 称号，靠既有兜底产出无色小晶块；10010 是操作者线上背包里的布甲上衣）。验证批量路径
// 在真档上的校验/产物解析与单件一致，整批回一条聚合 ACK，产物合并堆叠。
//
//	运行：DNFBRIDGE_REAL_PVF_SMOKE=<runtime>\data\dnf\Script.pvf go test -count=1 \
//		-run RealScriptPVFDisjointBatch ./internal/services/dnfbridge
func TestRealScriptPVFDisjointBatchMatchesSingleItemPath(t *testing.T) {
	t.Setenv("DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE", "")
	t.Setenv("DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE", "")
	pvfPath := strings.TrimSpace(os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE"))
	if pvfPath == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE to the runtime Script.pvf")
	}
	archive, err := platformpvf.Open(pvfPath)
	if err != nil {
		t.Fatal(err)
	}
	// 两件真档装备：400330100 = `[equipment type] [title name]` 且无 [value]/[price]
	// （既有真档用例 TestRealPVFDisjointPlacesRewardsInRuntimeSegments 已钉死这一点）；
	// 10010 = 布甲上衣（操作者线上背包里的实际装备）。
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadCurrentDisjointPVFConfig(archive)
	if err != nil {
		t.Fatal(err)
	}
	itemIDs := []uint32{400330100, 10010}
	sourceSlots := []int16{9, 10}
	resolvedPerItem := make([][]currentDisjointReward, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		definition, err := catalog.ResolveItem(itemID)
		if err != nil {
			t.Fatalf("runtime item %d: %v", itemID, err)
		}
		if definition.Kind != dungeonDropItemEquipment {
			t.Fatalf("runtime item %d kind=%q want equipment", itemID, definition.Kind)
		}
		document, err := parseDungeonCardPVFDocument(archive, definition.PVFPath)
		if err != nil {
			t.Fatalf("runtime item %d document: %v", itemID, err)
		}
		if currentDisjointDocumentIsAvatar(document) {
			t.Fatalf("runtime item %d is an avatar; op26 must reject it", itemID)
		}
		rewards, err := currentEquipmentDisjointRewards(config, document)
		if err != nil || len(rewards) == 0 {
			t.Fatalf("runtime item %d rewards=%+v err=%v", itemID, rewards, err)
		}
		resolvedPerItem = append(resolvedPerItem, rewards)
	}

	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, sourceSlots[0]): {ItemID: 400330100, Count: 1},
		currentCeraShopInventorySlotKey(0, sourceSlots[1]): {ItemID: 10010, Count: 1},
	}
	repositories := mustCurrentNPCShopRepositories(t, 0, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	service := currentDisjointTestService(repositories, catalog)
	connection := &bufferConn{}
	body := currentDisjointBatchBody(sourceSlots, -1, 0x13d)
	// 与现场那条 18 字节批量体逐字节同形（`02 0900 0a00` = 2 件、槽 9/10）。
	if len(body) != 18 || hex.EncodeToString(body) != "ffff00ffff3d010000423236010209000a00" {
		t.Fatalf("real-PVF batch body=%s want the live 18-byte shape", hex.EncodeToString(body))
	}
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19}, body); err != nil {
		t.Fatal(err)
	}
	acks, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	if len(acks) != 1 || len(slotRefreshes) != 1 || len(containers) != 0 {
		t.Fatalf("real-PVF batch ACKs=%d op14=%d op13=%d want 1/1/0", len(acks), len(slotRefreshes), len(containers))
	}
	var aggregateRows []currentDisjointRewardRow
	for index, ack := range acks {
		if len(ack.Body) < 3 || ack.Body[0] != 1 {
			t.Fatalf("real-PVF aggregate ACK=%x", ack.Body)
		}
		if got := int16(binary.LittleEndian.Uint16(ack.Body[1:3])); got != sourceSlots[index] {
			t.Fatalf("real-PVF ack[%d] echoed slot %d want %d", index, got, sourceSlots[index])
		}
		rows := parseCurrentDisjointRewardRows(t, ack.Body)
		aggregateRows = rows
		if len(rows) == 0 {
			t.Fatalf("real-PVF ack[%d] has no reward rows", index)
		}
		// 每一件都必须产出无色小晶块并落在账号晶块仓 358；数量落在真档解析出的区间内
		// （[additional result expand] 带概率，所以只断言确定性部分与上界）。
		cube := currentDisjointRewardRowForItem(t, rows, config.CubeItemID)
		if cube.Slot != 358 || cube.Granted < uint32(len(sourceSlots)) {
			t.Fatalf("real-PVF ack[%d] cube row=%+v want both sources merged at slot 358", index, cube)
		}
		seen := make(map[int16]bool)
		for _, row := range rows {
			// 真档的产物集合带随机 roll（无色小晶块之外还会掷出有色小晶块/元素结晶/
			// 灵魂，且同一 id 可能被掷中多次），所以这里断言的是**结构性不变量**：
			// 每一行都必须是可解析的堆叠品（发放器本身也以此为准），且获得量在
			// 1..堆叠上限之间（同一 id 多次掷中会合并成一行，故用 1..上限×行内重复数
			// 的弱上界即可）。
			definition, err := catalog.ResolveItem(row.ItemID)
			if err != nil || definition.Kind != dungeonDropItemStackable {
				t.Fatalf("real-PVF ack[%d] row=%+v is not a resolvable stackable: %v", index, row, err)
			}
			var limit int64
			for _, rewards := range resolvedPerItem {
				limit += maxCurrentDisjointRewardUnits(rewards, row.ItemID)
			}
			if seen[row.Slot] {
				t.Fatalf("real-PVF aggregate repeated slot %d", row.Slot)
			}
			seen[row.Slot] = true
			if row.Granted == 0 || int64(row.Granted) > limit {
				t.Fatalf("real-PVF ack[%d] row=%+v outside 1..%d", index, row,
					limit)
			}
		}
	}
	inventory, _, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range sourceSlots {
		if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]; exists {
			t.Fatalf("real-PVF batch left source slot %d: %+v", slot, stack)
		}
	}
	account, _, err := repositories.AccountInventory.Load(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if stack := account.Slots["0:358"]; stack.ItemID != int64(config.CubeItemID) || stack.Count < 2 {
		t.Fatalf("real-PVF crystal warehouse 358=%+v want both items' cubes merged into one stack", stack)
	}
	for _, row := range aggregateRows {
		key := currentCeraShopInventorySlotKey(0, row.Slot)
		stack := inventory.Slots[key]
		if accountStack, ok := account.Slots[key]; ok {
			stack = accountStack
		}
		if stack.ItemID != int64(row.ItemID) || stack.Count != int64(row.Granted) {
			t.Fatalf("real-PVF ACK row=%+v differs from committed stack=%+v", row, stack)
		}
	}
}

// splitAllGameServerUpperPackets 由 joust_event_switch_test.go 提供：批量回包是
// 聚合成功 ACK 加槽位刷新，用例用它把一次写入里的连续包逐条切开校验。
