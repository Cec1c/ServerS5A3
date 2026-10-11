package dnfbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 批量分解 op14 刷新**覆盖哪些槽**的聚焦回归（运维第 8 条 A 半："弹一个窗口但是
// 里面的无色晶体一次又一次叠加，要写成一次性发放"）。
//
// 现场定位（逐帧，见 disjoint_compound_protocol.go 的开关注释）：
//   - `single_aggregate_ack` 已上线：`ack_count=1`，一个获得框 ✔；
//   - 但整批回包里**带 3037 的帧有两帧** —— 聚合 ACK 的"本次获得"行 + op14 里
//     358 那一行的绝对堆叠量；
//   - 而运维认定正确的**单件分解**路径（`commitCurrentDisjointItem`）只发 op26 ACK，
//     一个字节的 op14 都不发。
//
// 默认档于是把批量收敛成与单件同构：**产物只由 ACK 承载**，op14 只清来源槽。

func TestCurrentDisjointBatchRefreshShapeEnvNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: currentDisjointBatchRefreshShapeSourceOnly},
		{value: "typo", want: currentDisjointBatchRefreshShapeSourceOnly},
		{value: "source_only", want: currentDisjointBatchRefreshShapeSourceOnly},
		{value: "  SOURCE_ONLY ", want: currentDisjointBatchRefreshShapeSourceOnly},
		{value: "legacy", want: currentDisjointBatchRefreshShapeLegacy},
		{value: "  LEGACY ", want: currentDisjointBatchRefreshShapeLegacy},
		{value: "off", want: currentDisjointBatchRefreshShapeLegacy},
		{value: "0", want: currentDisjointBatchRefreshShapeLegacy},
		{value: "false", want: currentDisjointBatchRefreshShapeLegacy},
		{value: "disabled", want: currentDisjointBatchRefreshShapeLegacy},
		{value: "all_rows", want: currentDisjointBatchRefreshShapeLegacy},
	} {
		t.Run("DNFBRIDGE_DISJOINT_BATCH_REFRESH_SHAPE="+test.value, func(t *testing.T) {
			t.Setenv(currentDisjointBatchRefreshShapeEnv, test.value)
			if got := currentDisjointBatchRefreshShape(); got != test.want {
				t.Fatalf("shape=%q want %q", got, test.want)
			}
			wantRewards := test.want == currentDisjointBatchRefreshShapeLegacy
			if got := currentDisjointBatchRefreshShapeUsesRewardSlots(test.want); got != wantRewards {
				t.Fatalf("usesRewardSlots(%q)=%t want %t", test.want, got, wantRewards)
			}
		})
	}
}

// 默认档：来源槽行只覆盖被消耗的槽（去重），产物槽一个都不进清单。
func TestCurrentDisjointBatchSourceSlotRefreshesCoverOnlyConsumedSlots(t *testing.T) {
	refreshes := currentDisjointBatchSourceSlotRefreshes(currentDisjointBatchResult{
		Items: []currentDisjointBatchItem{
			{Slot: 60, Rewards: []currentDisjointRewardSlot{{Slot: 358, ItemID: 3037, Count: 14, Granted: 14}}},
			{Slot: 61, Rewards: []currentDisjointRewardSlot{{Slot: 362, ItemID: 10099773, Count: 1, Granted: 1}}},
			// 同一来源槽重复出现（客户端不该发，但拒收前先保证清单不重复）。
			{Slot: 60, Rewards: []currentDisjointRewardSlot{{Slot: 121, ItemID: 3166, Count: 1, Granted: 1}}},
		},
	})
	if len(refreshes) != 2 {
		t.Fatalf("refresh=%+v want 2（60、61，去重 + 不含 358/362/121）", refreshes)
	}
	if refreshes[0].SlotIndex != 60 || refreshes[1].SlotIndex != 61 {
		t.Fatalf("refresh 顺序=%+v want [60 61]（请求顺序）", refreshes)
	}
	// 对照：legacy 档的行清单仍然把产物槽排在前面（既有行为一字不改）。
	legacy := currentDisjointBatchSlotRefreshes(currentDisjointBatchResult{
		Items: []currentDisjointBatchItem{
			{Slot: 60, Rewards: []currentDisjointRewardSlot{{Slot: 358, ItemID: 3037, Count: 14, Granted: 14}}},
			{Slot: 61, Rewards: []currentDisjointRewardSlot{{Slot: 362, ItemID: 10099773, Count: 1, Granted: 1}}},
		},
	})
	if len(legacy) != 4 || legacy[0].SlotIndex != 358 || legacy[1].SlotIndex != 362 {
		t.Fatalf("legacy refresh=%+v want 产物槽(358,362) 在前 + 2 个来源槽", legacy)
	}
}

// 回退档（**两个**开关都写回退值）必须逐字节回到 2026-09-29 线上形状：
// 逐件 ACK + op14 含产物槽绝对值（晶块仓 358 = 30、灵魂仓 362 = 4、元素结晶一格）。
func TestCurrentDisjointBatchLegacyRefreshShapeIsTheByteExactRollback(t *testing.T) {
	t.Setenv(currentDisjointBatchAckShapeEnv, currentDisjointBatchAckShapePerItemSingleAckFresh)
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
	rows := parseCurrentItemUpdateSlotRows(t, slotRefreshes[0].Body)
	if len(rows) != len(sourceSlots)+3 {
		t.Fatalf("回退档刷新行数=%d want %d（4 来源 + 3 产物格）", len(rows), len(sourceSlots)+3)
	}
	cube := currentItemListRefreshRowForSlot(t, rows, 358)
	if cube.ItemID != 3037 || cube.Count != 30 {
		t.Fatalf("回退档晶块仓行=%+v want 3037×30", cube)
	}
	soul := currentItemListRefreshRowForSlot(t, rows, 362)
	if soul.ItemID != 10099773 || soul.Count != 4 {
		t.Fatalf("回退档灵魂仓行=%+v want 10099773×4", soul)
	}
	if element := currentItemListRefreshRowForItem(t, rows, 3166); element.Count != 4 {
		t.Fatalf("回退档元素结晶行=%+v want ×4", element)
	}
	// 回退档下带 3037 的帧 = 4 条逐件 ACK + 1 条 op14 = 5（这就是被运维否掉的形状）。
	if got := currentDisjointReplyFramesCarryingItem(t, connection.write.Bytes(), 3037); got != len(sourceSlots)+1 {
		t.Fatalf("回退档带 3037 的帧=%d want %d", got, len(sourceSlots)+1)
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
		"refresh_shape=" + currentDisjointBatchRefreshShapeLegacy,
		"refresh_rows=7",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("回退档日志缺 %q：\n%s", want, text)
		}
	}
}
