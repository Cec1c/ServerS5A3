package dnfbridge

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 批量分解收尾：**"ACK 之外必须把界面拉回权威状态"** 的聚焦回归。
//
// 现场判定（2026-09-25，同一会话 packet_log + 客户端补丁 trace + 90CN.dll 反汇编）：
// 上一轮的 `ack_shape=per_item_single_ack`（逐件回单件成功体、不补刷新）在实服
// **客户端一条 ACK 都没采纳**——
//   - 服务端：`game-disjoint-item-batch-committed count=4 slots=9,11,12,13
//     reward_rows=9 units=63 ack_shape=per_item_single_ack`，4 条 op26 已发出
//     （pkt_seq 131..134）；
//   - 客户端：14 秒后操作者再点一次批量，90CN.dll trace 仍自述
//     `equipment-disjoint batch queued selected=4 tool=-1 context=317`，服务端收到的
//     仍是同一条 `ffff00ffff3d010000423236010409000b000c000d00`
//     ⇒ 4 件装备在界面上**一件都没消失**（不是"只读第一条"）。
//
// 所以回包必须补一次"槽位内容以仓储为准"的下发：默认档用 op14 受影响槽位行
// （来源槽已从仓储删除 ⇒ 空行 `itemID=0xFFFFFFFF, count=0`；产物槽 ⇒ 权威最终堆叠量），
// 兜底档用 op13 主背包整容器下发。两个档都由 DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE
// 选择，日志写 `ack_shape=` / `ack_count=` / `refresh_rows=`。

// currentItemListRefreshRow 是一条下发物品行里本用例要断言的三元组。
type currentItemListRefreshRow struct {
	Slot   int16
	ItemID uint32
	Count  uint32
}

// currentItemListEmptyItemID 是"这一格现在是空的"标记（仓库里没有该槽 ⇒ 客户端清格）。
// 与 title_book.go / current_item_list_send.go 用的是同一个常量。
const currentItemListEmptyItemID = uint32(0xFFFFFFFF)

// splitCurrentDisjointBatchReplies 把一次批量分解的写入按 msg_id 分类：
// op26 = 逐件成功 ACK，op14 = 受影响槽位的权威刷新行，op13 = 主背包整容器下发。
func splitCurrentDisjointBatchReplies(t *testing.T, data []byte) (acks, slotRefreshes, containers []dnfproto.ChannelPacket) {
	t.Helper()
	for _, packet := range splitAllGameServerUpperPackets(t, data) {
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketDisjointItem):
			acks = append(acks, packet)
		case uint16(dnfenum.CmdPacketWalkoutPartyMember):
			slotRefreshes = append(slotRefreshes, packet)
		case uint16(dnfenum.CmdPacketLeaveParty):
			containers = append(containers, packet)
		default:
			t.Fatalf("批量分解写了预期之外的包 msg_id=%d body=%x", packet.Header.MsgID, packet.Body)
		}
	}
	return acks, slotRefreshes, containers
}

// parseCurrentItemUpdateSlotRows 解析 op14 体：`u8 list | u16 count | count × 0x77 行`，
// 每行 `u16 槽 | u32 物品 | u32 数量`（与 patchCore 的字节偏移逐字对应）。
func parseCurrentItemUpdateSlotRows(t *testing.T, body []byte) []currentItemListRefreshRow {
	t.Helper()
	if len(body) < 3 {
		t.Fatalf("op14 体过短：%x", body)
	}
	if body[0] != dnfrepo.MainInventoryListType {
		t.Fatalf("op14 list=%d want %d", body[0], dnfrepo.MainInventoryListType)
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	if len(body) != 3+count*currentItemListEntryWireSize {
		t.Fatalf("op14 体长=%d 行数=%d want %d", len(body), count, 3+count*currentItemListEntryWireSize)
	}
	rows := make([]currentItemListRefreshRow, 0, count)
	for index := 0; index < count; index++ {
		offset := 3 + index*currentItemListEntryWireSize
		rows = append(rows, currentItemListRefreshRow{
			Slot:   int16(binary.LittleEndian.Uint16(body[offset : offset+2])),
			ItemID: binary.LittleEndian.Uint32(body[offset+2 : offset+6]),
			Count:  binary.LittleEndian.Uint32(body[offset+6 : offset+10]),
		})
	}
	return rows
}

// parseCurrentMainInventoryContainerRows 解析 op13 主背包整容器体：
// `u8 list=0 | u16 页容量 | u16 行数 | 行数 × 0x77 行`。
//
// 注意：整容器下发走 sendCurrentSceneItemList ⇒ **16 字节头**
// （`current_scene_fixed16_zero_prefix`），而用例的分包器按本 fixture 的 13 字节
// classification-0 头切包 ⇒ packet.Body 前面会多出 3 个头尾巴字节。这里按
// "list 字节 + 行数 + 体长自洽"定位真正的体起点，而不是死认下标 0。
func parseCurrentMainInventoryContainerRows(t *testing.T, packet dnfproto.ChannelPacket) []currentItemListRefreshRow {
	t.Helper()
	body := packet.Body
	offset := -1
	for _, candidate := range []int{0, 3} {
		if len(body) < candidate+5 || body[candidate] != dnfrepo.MainInventoryListType {
			continue
		}
		count := int(binary.LittleEndian.Uint16(body[candidate+3 : candidate+5]))
		if len(body) == candidate+5+count*currentItemListEntryWireSize {
			if offset != -1 {
				t.Fatalf("整容器体起点有歧义：%d 与 %d 都自洽", offset, candidate)
			}
			offset = candidate
		}
	}
	if offset < 0 {
		t.Fatalf("整容器体长=%d 与任何起点都对不上（头 8 字节 %x）", len(body), body[:min(8, len(body))])
	}
	count := int(binary.LittleEndian.Uint16(body[offset+3 : offset+5]))
	rows := make([]currentItemListRefreshRow, 0, count)
	for index := 0; index < count; index++ {
		rowOffset := offset + 5 + index*currentItemListEntryWireSize
		rows = append(rows, currentItemListRefreshRow{
			Slot:   int16(binary.LittleEndian.Uint16(body[rowOffset : rowOffset+2])),
			ItemID: binary.LittleEndian.Uint32(body[rowOffset+2 : rowOffset+6]),
			Count:  binary.LittleEndian.Uint32(body[rowOffset+6 : rowOffset+10]),
		})
	}
	return rows
}

// currentItemListRefreshRowForSlot 按槽位取行；同一槽位出现多次即判失败
// （"来源槽被空行清掉"和"产物槽带权威量"不允许混在同一条槽上）。
func currentItemListRefreshRowForSlot(t *testing.T, rows []currentItemListRefreshRow, slot int16) currentItemListRefreshRow {
	t.Helper()
	found := currentItemListRefreshRow{Slot: slot}
	count := 0
	for _, row := range rows {
		if row.Slot == slot {
			found = row
			count++
		}
	}
	if count == 0 {
		t.Fatalf("刷新里没有槽 %d 的行：%+v", slot, rows)
	}
	if count > 1 {
		t.Fatalf("刷新里槽 %d 出现 %d 次：%+v", slot, count, rows)
	}
	return found
}

// currentItemListRefreshRowForItem 按物品 id 取行（产物行按槽位排序，位置不固定）。
func currentItemListRefreshRowForItem(t *testing.T, rows []currentItemListRefreshRow, itemID uint32) currentItemListRefreshRow {
	t.Helper()
	for _, row := range rows {
		if row.ItemID == itemID {
			return row
		}
	}
	t.Fatalf("刷新里没有物品 %d 的行：%+v", itemID, rows)
	return currentItemListRefreshRow{}
}

// currentDisjointBatchRefreshAssets 是下面几条用例共用的 4 件批量资产：
// 两件 5000（valued_coat，14 无色小晶块）+ 两件 5200（无 [value] 称号，1 无色小晶块），
// 产物固定落在晶块仓 358、灵魂仓 362 与主背包材料段。
func currentDisjointBatchRefreshAssets(t *testing.T) (dnfrepo.Group, *pvfDungeonDropCatalog, []int16) {
	t.Helper()
	sourceSlots := []int16{60, 61, 62, 63}
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, 60): {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, 61): {ItemID: 5200, Count: 1},
		currentCeraShopInventorySlotKey(0, 62): {ItemID: 5000, Count: 1},
		currentCeraShopInventorySlotKey(0, 63): {ItemID: 5200, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	return repositories, catalog, sourceSlots
}

// (i) 默认档：选中的 4 个槽位在回包后必须以**空行**出现在 op14 刷新里（原装备 id 不再
// 出现在任何下发物品行），同时产物行（晶块/元素结晶/灵魂）带权威最终数量出现；
// 刷新必须排在逐件 ACK **之后**。
func TestCurrentDisjointBatchRefreshClearsConsumedSlotsAndCarriesRewards(t *testing.T) {
	repositories, catalog, sourceSlots := currentDisjointBatchRefreshAssets(t)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	session := &gameSession{conn: connection, selectedCharacterID: 19}

	if err := service.handleCurrentDisjointItem(session, currentDisjointBatchBody(sourceSlots, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	acks, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	// 【2026-09-29 断言变更】旧断言是 `len(acks)==len(sourceSlots)`（逐件一条 ACK）。
	// 它不再正确：默认档是 single_aggregate_ack —— **一条** ACK 承载整批产物的合并
	// 行，客户端因此只弹一个获得框（运维要求"统一在一个框"）。逐件形状仍然存在，
	// 由回退档用例逐字节钉住。下面关于"来源槽必须空行""产物行与仓储一致""刷新排在
	// ACK 之后"的断言一条未改。
	if len(acks) != 1 {
		t.Fatalf("op26 ACK=%d want 1 (single_aggregate_ack)", len(acks))
	}
	if len(containers) != 0 {
		t.Fatalf("默认档不该发整容器下发：%d 条", len(containers))
	}
	if len(slotRefreshes) != 1 {
		t.Fatalf("op14 槽位刷新=%d want 1（主背包一条包内多行）", len(slotRefreshes))
	}
	// 顺序：ACK 在前、刷新在后（刷新行是绝对量，必须最后落）。
	order := splitAllGameServerUpperPackets(t, connection.write.Bytes())
	if got := order[len(order)-1].Header.MsgID; got != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
		t.Fatalf("最后一条包 msg_id=%d want op14 刷新", got)
	}
	// 唯一那条 ACK：回显本批第一件，行 = 整批合并后的产物（晶块 14+1+14+1=30）。
	if got := int16(binary.LittleEndian.Uint16(acks[0].Body[1:3])); got != sourceSlots[0] {
		t.Fatalf("aggregate ack 回显槽 %d want %d（必须回第一件）", got, sourceSlots[0])
	}
	aggregateRows := parseCurrentDisjointRewardRows(t, acks[0].Body)
	if cube := currentDisjointRewardRowForItem(t, aggregateRows, 3037); cube.Slot != 358 || cube.Granted != 30 {
		t.Fatalf("聚合 ACK 晶块行=%+v want slot 358 granted 30（整批合并）", cube)
	}

	rows := parseCurrentItemUpdateSlotRows(t, slotRefreshes[0].Body)
	// ① 被消耗的来源槽：必须是空行（0xFFFFFFFF / 0），且**不得**再带原装备 id。
	for _, slot := range sourceSlots {
		row := currentItemListRefreshRowForSlot(t, rows, slot)
		if row.ItemID != currentItemListEmptyItemID || row.Count != 0 {
			t.Fatalf("来源槽 %d 刷新行=%+v want 空行 0xFFFFFFFF/0", slot, row)
		}
	}
	for _, row := range rows {
		if row.ItemID == 5000 || row.ItemID == 5200 {
			t.Fatalf("被分解的装备仍出现在下发物品行里：%+v", row)
		}
	}
	// ② 【2026-09-29 断言变更（第二次）】默认档 DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE
	// = source_only：op14 **只**含来源槽行，产物槽一行都不发（旧断言要求晶块仓 358、
	// 灵魂仓 362、元素结晶都各有一行）。理由：产物已经在同一回包的那一条聚合 ACK 里
	// 以"本次获得"给过客户端 —— 这正是**单件分解**（`commitCurrentDisjointItem`
	// 只发 ACK、不发任何 op14，运维认定正确）的工作方式；把产物槽再放进 op14 只会让
	// 同一件道具在同一批回包里出现两次（ACK 的 granted + op14 的绝对量）。
	// 下面同时钉死"整批回包里带 3037 的帧恰好一条"，这就是运维报的"一次又一次叠加"
	// 在字节层的反面判据。
	if len(rows) != len(sourceSlots) {
		t.Fatalf("默认档刷新行数=%d want %d（=来源槽数）：%+v", len(rows), len(sourceSlots), rows)
	}
	for _, row := range rows {
		if row.ItemID == 3037 || row.ItemID == 10099773 || row.ItemID == 3166 {
			t.Fatalf("默认档 op14 不得再带产物行（产物由 ACK 承载）：%+v", row)
		}
	}
	if got := currentDisjointReplyFramesCarryingItem(t, connection.write.Bytes(), 3037); got != 1 {
		t.Fatalf("整批回包里带 3037 的帧=%d want 1（只有那条聚合 ACK）", got)
	}
	// ③ 去重后行数 = 4 来源槽（产物格不进清单）。
	if len(rows) != len(sourceSlots) {
		t.Fatalf("刷新行数=%d want %d：%+v", len(rows), len(sourceSlots), rows)
	}
	// ④ 仓储终态与下发一致：来源槽已删；产物仍按 ACK 的"本次获得"落到晶块仓
	// （刷新里没有它的行，所以这里以 ACK 行 + 仓储作为真源）。
	ctx := context.Background()
	inventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range sourceSlots {
		if stack, exists := inventory.Slots[currentCeraShopInventorySlotKey(0, slot)]; exists {
			t.Fatalf("来源槽 %d 仍在仓储里：%+v", slot, stack)
		}
	}
	account, _, err := repositories.AccountInventory.Load(ctx, "account-1")
	if err != nil {
		t.Fatal(err)
	}
	accountCube := currentDisjointRewardRowForItem(t, aggregateRows, 3037)
	if stack := account.Slots["0:358"]; stack.ItemID != 3037 || stack.Count != int64(accountCube.Granted) {
		t.Fatalf("晶块仓仓储=%+v 与聚合 ACK 的本次获得 %d 不一致", stack, accountCube.Granted)
	}
}

// currentDisjointReplyFramesCarryingItem 数一次批量分解的**整段下行**里有多少帧
// 带了指定产物 id：op26 ACK 看"本次获得"行，op14 看物品行。它把"同一件产物在一批
// 回包里出现几次"变成可断言的数字（运维报的"一次又一次叠加"）。
func currentDisjointReplyFramesCarryingItem(t *testing.T, data []byte, itemID uint32) int {
	t.Helper()
	frames := 0
	for _, packet := range splitAllGameServerUpperPackets(t, data) {
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketDisjointItem):
			for _, row := range parseCurrentDisjointRewardRows(t, packet.Body) {
				if row.ItemID == itemID {
					frames++
					break
				}
			}
		case uint16(dnfenum.CmdPacketWalkoutPartyMember):
			for _, row := range parseCurrentItemUpdateSlotRows(t, packet.Body) {
				if row.ItemID == itemID {
					frames++
					break
				}
			}
		}
	}
	return frames
}

// (j) 一键回退：per_item_single_ack 必须**逐字节回到上一轮线上行为**——
// 只有 N 条 op26 ACK，一个字节的刷新都不多发。
func TestCurrentDisjointBatchAckShapeRollbackSendsOnlyPerItemAcks(t *testing.T) {
	t.Setenv(currentDisjointBatchAckShapeEnv, currentDisjointBatchAckShapePerItemSingleAck)
	if shape := currentDisjointBatchAckShape(); shape != currentDisjointBatchAckShapePerItemSingleAck {
		t.Fatalf("shape=%q want %q", shape, currentDisjointBatchAckShapePerItemSingleAck)
	}
	repositories, catalog, sourceSlots := currentDisjointBatchRefreshAssets(t)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19},
		currentDisjointBatchBody(sourceSlots, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	acks, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	if len(acks) != len(sourceSlots) || len(slotRefreshes) != 0 || len(containers) != 0 {
		t.Fatalf("回退档回包=ACK%d/op14 %d/op13 %d want %d/0/0",
			len(acks), len(slotRefreshes), len(containers), len(sourceSlots))
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
		"ack_shape=" + currentDisjointBatchAckShapePerItemSingleAck,
		"ack_count=4",
		"refresh_rows=0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("回退档日志缺 %q：\n%s", want, text)
		}
	}
}

// (k) 兜底档：one_ack_container_refresh 只回第一件的 ACK，随后用 op13 主背包整容器
// 下发把"哪些槽是空的"整表重建（被分解的槽不再出现在行里，产物行在场）。
func TestCurrentDisjointBatchOneAckContainerRefreshDropsConsumedSlots(t *testing.T) {
	t.Setenv(currentDisjointBatchAckShapeEnv, currentDisjointBatchAckShapeOneAckContainerRefresh)
	repositories, catalog, sourceSlots := currentDisjointBatchRefreshAssets(t)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19},
		currentDisjointBatchBody(sourceSlots, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	acks, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	if len(acks) != 1 || len(slotRefreshes) != 0 || len(containers) != 1 {
		t.Fatalf("兜底档回包=ACK%d/op14 %d/op13 %d want 1/0/1", len(acks), len(slotRefreshes), len(containers))
	}
	if got := int16(binary.LittleEndian.Uint16(acks[0].Body[1:3])); got != sourceSlots[0] {
		t.Fatalf("单条 ACK 回显槽 %d want %d（必须回第一件）", got, sourceSlots[0])
	}
	rows := parseCurrentMainInventoryContainerRows(t, containers[0])
	// 整表里不能再出现"被分解的装备"这一行：来源槽要么不在，要么是空行。
	for _, slot := range sourceSlots {
		for _, row := range rows {
			if row.Slot == slot && row.ItemID != currentItemListEmptyItemID {
				t.Fatalf("整容器下发仍带被分解的来源槽 %d：%+v", slot, row)
			}
		}
	}
	for _, row := range rows {
		if row.ItemID == 5000 || row.ItemID == 5200 {
			t.Fatalf("整容器下发仍带被分解的装备 id：%+v", row)
		}
	}
	// 产物在场：晶块仓 358 与元素结晶（主背包行）。
	if cube := currentItemListRefreshRowForSlot(t, rows, 358); cube.ItemID != 3037 || cube.Count != 30 {
		t.Fatalf("整容器下发晶块仓行=%+v want 3037×30", cube)
	}
	if element := currentItemListRefreshRowForItem(t, rows, 3166); element.Count != 4 {
		t.Fatalf("整容器下发元素结晶行=%+v want ×4", element)
	}
}

// (l) 开关归一化：只有显式写出**三个**已知取值才走那一档；未知/空/拼错一律回到
// **默认新方案**（fail-safe 到"会刷新界面 + 只弹一个框"的那一档，而不是回到已知
// 无效的旧形状）。
//
// 【2026-09-29 断言变更】旧表里空值/拼错期望的是 per_item_single_ack_refresh
// （当时的默认档）。默认档已改成 single_aggregate_ack，因此这两行的期望随之改变；
// 显式写出旧值的两行（per_item_single_ack / per_item_single_ack_refresh）期望不变
// ——它们仍然是逐字节回退档，由 (j) 与 (j2) 两条用例把回包字节钉死。
func TestCurrentDisjointBatchAckShapeEnvNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: currentDisjointBatchAckShapeSingleAggregate},
		{value: "typo", want: currentDisjointBatchAckShapeSingleAggregate},
		{value: "single_aggregate_ack", want: currentDisjointBatchAckShapeSingleAggregate},
		{value: "  SINGLE_AGGREGATE_ACK ", want: currentDisjointBatchAckShapeSingleAggregate},
		{value: "per_item_single_ack_refresh", want: currentDisjointBatchAckShapePerItemSingleAckFresh},
		{value: "  PER_ITEM_SINGLE_ACK_REFRESH ", want: currentDisjointBatchAckShapePerItemSingleAckFresh},
		{value: "per_item_single_ack", want: currentDisjointBatchAckShapePerItemSingleAck},
		{value: "Per_Item_Single_Ack", want: currentDisjointBatchAckShapePerItemSingleAck},
		{value: "one_ack_container_refresh", want: currentDisjointBatchAckShapeOneAckContainerRefresh},
	} {
		t.Run("DNFBRIDGE_DISJOINT_BATCH_ACK_SHAPE="+test.value, func(t *testing.T) {
			t.Setenv(currentDisjointBatchAckShapeEnv, test.value)
			if got := currentDisjointBatchAckShape(); got != test.want {
				t.Fatalf("shape=%q want %q", got, test.want)
			}
		})
	}
	// 档位谓词与 ACK 条数：默认档与兜底档都收成一条；逐件两档仍然一件一条。
	t.Setenv(currentDisjointBatchAckShapeEnv, "")
	if got := currentDisjointBatchAckShapeAckCount(4, currentDisjointBatchAckShape()); got != 1 {
		t.Fatalf("默认档 ACK 条数=%d want 1（整批合并成一个框）", got)
	}
	if got := currentDisjointBatchAckShapeAckCount(4, currentDisjointBatchAckShapePerItemSingleAckFresh); got != 4 {
		t.Fatalf("回退档 per_item_single_ack_refresh ACK 条数=%d want 4", got)
	}
	if got := currentDisjointBatchAckShapeAckCount(4, currentDisjointBatchAckShapePerItemSingleAck); got != 4 {
		t.Fatalf("回退档 per_item_single_ack ACK 条数=%d want 4", got)
	}
	if got := currentDisjointBatchAckShapeAckCount(4, currentDisjointBatchAckShapeOneAckContainerRefresh); got != 1 {
		t.Fatalf("兜底档 ACK 条数=%d want 1", got)
	}
	if got := currentDisjointBatchAckShapeAckCount(1, currentDisjointBatchAckShapeOneAckContainerRefresh); got != 1 {
		t.Fatalf("兜底档单件批量 ACK 条数=%d want 1", got)
	}
	if !currentDisjointBatchAckShapeUsesSlotRefresh(currentDisjointBatchAckShape()) ||
		currentDisjointBatchAckShapeUsesContainerRefresh(currentDisjointBatchAckShape()) {
		t.Fatal("默认档必须补 op14 槽位行、不补整容器下发")
	}
	if !currentDisjointBatchAckShapeUsesSlotRefresh(currentDisjointBatchAckShapePerItemSingleAckFresh) {
		t.Fatal("回退档 per_item_single_ack_refresh 必须补 op14 槽位行")
	}
	if currentDisjointBatchAckShapeUsesSlotRefresh(currentDisjointBatchAckShapePerItemSingleAck) ||
		currentDisjointBatchAckShapeUsesContainerRefresh(currentDisjointBatchAckShapePerItemSingleAck) {
		t.Fatal("回退档 per_item_single_ack 不得补任何刷新")
	}
}

// (j2) 回退字面量 per_item_single_ack_refresh 必须**逐字节**回到 2026-09-25..29 的
// 线上形状：N 条单件 ACK（第 i 条回显请求里的第 i 个来源槽、只带那一件自己的产物行）
// + 一条 op14 受影响槽位刷新 + 日志字段 `ack_shape=per_item_single_ack_refresh
// ack_count=4`。这是"运维要的聚合框"与"一键回退"之间的分界线：回退档的字节必须
// 能在测试里看到。
func TestCurrentDisjointBatchAckShapeRollbackSendsPerItemAcksWithRefresh(t *testing.T) {
	t.Setenv(currentDisjointBatchAckShapeEnv, currentDisjointBatchAckShapePerItemSingleAckFresh)
	if shape := currentDisjointBatchAckShape(); shape != currentDisjointBatchAckShapePerItemSingleAckFresh {
		t.Fatalf("shape=%q want %q", shape, currentDisjointBatchAckShapePerItemSingleAckFresh)
	}
	// 【2026-09-29 追加】"2026-09-25..29 线上形状"是**两个**开关的乘积：ACK 逐件
	// 那一条之外，op14 当时还带产物槽行。要逐字节回到那一天必须两个都写回退值
	// （见 disjoint_compound_protocol.go 的 refresh-shape 注释与报告开关表）。
	t.Setenv(currentDisjointBatchRefreshShapeEnv, currentDisjointBatchRefreshShapeLegacy)
	repositories, catalog, sourceSlots := currentDisjointBatchRefreshAssets(t)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	logPath := filepath.Join(t.TempDir(), "packet.log")
	logger, err := openPacketLogger(logPath)
	if err != nil {
		t.Fatal(err)
	}
	service.packetLog = logger
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19},
		currentDisjointBatchBody(sourceSlots, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	acks, slotRefreshes, containers := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	if len(acks) != len(sourceSlots) || len(slotRefreshes) != 1 || len(containers) != 0 {
		t.Fatalf("回退档回包=ACK%d/op14 %d/op13 %d want %d/1/0",
			len(acks), len(slotRefreshes), len(containers), len(sourceSlots))
	}
	// 逐件形状：第 i 条 ACK 回显第 i 个来源槽，且只带这一件自己的 granted。
	wantPerItem := []uint32{14, 1, 14, 1}
	for index, ack := range acks {
		if ack.Header.MsgID != uint16(dnfenum.CmdPacketDisjointItem) || len(ack.Body) == 0 || ack.Body[0] != 1 {
			t.Fatalf("ack[%d]=%x header=%+v", index, ack.Body, ack.Header)
		}
		if got := int16(binary.LittleEndian.Uint16(ack.Body[1:3])); got != sourceSlots[index] {
			t.Fatalf("ack[%d] 回显槽 %d want %d（请求顺序）", index, got, sourceSlots[index])
		}
		rows := parseCurrentDisjointRewardRows(t, ack.Body)
		if cube := currentDisjointRewardRowForItem(t, rows, 3037); cube.Slot != 358 || cube.Granted != wantPerItem[index] {
			t.Fatalf("ack[%d] 晶块行=%+v want slot 358 granted %d（只算这一件）", index, cube, wantPerItem[index])
		}
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
		"ack_shape=" + currentDisjointBatchAckShapePerItemSingleAckFresh,
		"ack_count=4",
		// 显式回退档（两个开关都写回退值）下 4 件来源槽 + 3 个产物格（晶块仓 358、
		// 灵魂仓 362、元素结晶一格）= 7，与 2026-09-25..29 线上形状逐字节相同。
		"refresh_shape=" + currentDisjointBatchRefreshShapeLegacy,
		"refresh_rows=7",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("回退档日志缺 %q：\n%s", want, text)
		}
	}
}

// (m) 批量产物的落点可能与"刚腾空的来源槽"重合（同一事务里 PlacePickupItem 可以把
// [material] 放进被分解的那一格）。刷新清单必须**先把产物槽排进去**并去重，这样
// "来源槽被产物重新占用"时拿到的是产物行（权威量），不会被刷成空行。
//
// (m1) 单变量钉住顺序与去重（不依赖运行时落点策略）；
// (m2) 端到端跑一次真请求，断言行内容与仓储终态逐字一致（无论落点是否重合都成立）。
func TestCurrentDisjointBatchRefreshOrdersRewardSlotsBeforeVacatedSourceSlots(t *testing.T) {
	const slot = int16(121)
	refreshes := currentDisjointBatchSlotRefreshes(currentDisjointBatchResult{
		Items: []currentDisjointBatchItem{{
			Slot: slot,
			Rewards: []currentDisjointRewardSlot{
				{Slot: slot, ItemID: 3166, Count: 4, Granted: 4},
				{Slot: 358, ItemID: 3037, Count: 30, Granted: 30},
			},
		}},
	})
	// 去重后 2 条：重合槽只出现一次，且必须排在来源槽位置之前（产物优先）。
	if len(refreshes) != 2 {
		t.Fatalf("refresh=%+v want 2 (重合槽去重)", refreshes)
	}
	if refreshes[0].SlotIndex != slot || refreshes[1].SlotIndex != 358 {
		t.Fatalf("refresh 顺序=%+v want [%d 358]（产物槽在前）", refreshes, slot)
	}
	for _, refresh := range refreshes {
		if refresh.ListType != dnfrepo.MainInventoryListType {
			t.Fatalf("refresh list=%d want 0", refresh.ListType)
		}
	}
	// 同一槽位重复出现（多件产物落同一格）也必须只算一行。
	deduped := currentDisjointBatchSlotRefreshes(currentDisjointBatchResult{
		Items: []currentDisjointBatchItem{
			{Slot: 60, Rewards: []currentDisjointRewardSlot{{Slot: 358, ItemID: 3037, Count: 14, Granted: 14}}},
			{Slot: 61, Rewards: []currentDisjointRewardSlot{{Slot: 358, ItemID: 3037, Count: 1, Granted: 1}}},
		},
	})
	if len(deduped) != 3 {
		t.Fatalf("refresh=%+v want 3 (358,60,61)", deduped)
	}
}

// (m2) 端到端：无论运行时把产物放回哪一格，下发行都必须等于仓储终态
// （来源槽腾空 ⇒ 空行；被产物重新占用 ⇒ 产物行）。
func TestCurrentDisjointBatchRefreshRowMatchesRepositoryFinalState(t *testing.T) {
	const sourceSlot = int16(121)
	slots := map[string]dnfrepo.ItemStack{
		currentCeraShopInventorySlotKey(0, sourceSlot): {ItemID: 5000, Count: 1},
	}
	repositories, catalog := mustCurrentDisjointPlacementAssets(t, slots)
	seedCurrentDisjointAccount(t, repositories, nil)
	connection := &bufferConn{}
	service := currentDisjointTestService(repositories, catalog)
	if err := service.handleCurrentDisjointItem(&gameSession{conn: connection, selectedCharacterID: 19},
		currentDisjointBatchBody([]int16{sourceSlot}, -1, 0x13d)); err != nil {
		t.Fatal(err)
	}
	_, slotRefreshes, _ := splitCurrentDisjointBatchReplies(t, connection.write.Bytes())
	if len(slotRefreshes) != 1 {
		t.Fatalf("op14 槽位刷新=%d want 1", len(slotRefreshes))
	}
	rows := parseCurrentItemUpdateSlotRows(t, slotRefreshes[0].Body)
	ctx := context.Background()
	inventory, _, err := repositories.Inventory.Load(ctx, "19")
	if err != nil {
		t.Fatal(err)
	}
	stack, occupied := inventory.Slots[currentCeraShopInventorySlotKey(0, sourceSlot)]
	row := currentItemListRefreshRowForSlot(t, rows, sourceSlot)
	if occupied {
		if row.ItemID != uint32(stack.ItemID) || row.Count != uint32(stack.Count) {
			t.Fatalf("来源槽被产物占用时下发=%+v 仓储=%+v（必须以仓储终态为准）", row, stack)
		}
		if row.ItemID == currentItemListEmptyItemID {
			t.Fatalf("来源槽被产物占用却下发了空行：%+v", row)
		}
		return
	}
	if row.ItemID != currentItemListEmptyItemID || row.Count != 0 {
		t.Fatalf("来源槽腾空后下发=%+v want 空行", row)
	}
	// 【2026-09-29 断言变更（第二次）】旧断言是"腾空分支下元素结晶必须另有一行
	// （否则产物在界面上就丢了）"，它属于产物槽进 op14 的旧档
	// （DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE=legacy）。默认档 source_only 下
	// 产物由聚合 ACK 的"本次获得"行承载，op14 里**不该**再有产物行 —— 否则同一件
	// 道具在同一批回包里出现两次。所以要断言的是反面：产物不在 op14 里出现，
	// 且来源槽行仍然是权威空行。
	if len(rows) != 1 || rows[0].Slot != sourceSlot {
		t.Fatalf("默认档刷新只应有来源槽一行：%+v", rows)
	}
	for _, row := range rows {
		if row.ItemID == 3166 {
			t.Fatalf("默认档 op14 不得再带产物行（元素结晶）：%+v", row)
		}
	}
}
