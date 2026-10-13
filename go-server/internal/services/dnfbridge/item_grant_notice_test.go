package dnfbridge

// 本文件钉住「道具入包通知」形状开关 DNFBRIDGE_ITEM_GRANT_NOTICE（见 item_grant_notice.go）。
//
// 它覆盖四件事：
//  1. 开关语义：默认（未设置/空/未知）= delta，显式 containers（及回退别名）= 今天的线格式；
//  2. 计划划分：本次真正写过的 (list, slot) 里，list 0/1/2 与 list 7 走逐槽增量
//     （list 7 由发送侧改期成 1500 ms 整容器），list 38 仍走整容器 —— 两条硬例外；
//  3. 三条奖励发放路径（op160 礼盒 / op208 魔盒 / op518 商城礼包）**默认档**下：
//     逐槽 op14 行出现、同一批奖励容器的整容器 op13 帧消失；
//  4. 同一批路径在 **containers 档**下逐包回到今天的形状（ACK + 整容器，一条 op14 都不发）。
//
// 证伪方式（父代理可复现）：把环境变量 DNFBRIDGE_ITEM_GRANT_NOTICE 设成 containers
// 再跑本文件的 `TestItemGrantNoticeDelta*`（这些用例刻意**不**调用 t.Setenv，所以外部
// 环境变量就是唯一变量），delta 断言必须全红。

import (
	"bytes"
	"context"
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	dnfrepomemory "longheng.io/server/internal/modules/dnf/repository/memory"
	"longheng.io/server/internal/platform/timequeue"
)

// pinItemGrantNoticeContainers 把形状开关钉在**回退档**：这些用例证明的是"containers
// 逐字节回到今天的线格式"，所以必须显式写开关，不能依赖默认值。
func pinItemGrantNoticeContainers(t *testing.T) {
	t.Helper()
	t.Setenv(itemGrantNoticeEnv, itemGrantNoticeContainers)
}

// itemGrantNoticePacket 是流里的一包。
type itemGrantNoticePacket struct {
	msgID          uint16
	classification byte
	body           []byte
	frame          []byte
}

// splitItemGrantNoticeStream 按生产口径拆包：class0/msg13（整容器）走 scene fixed16 信封
// （16 字节头、11..16 为 0），其余上行按服务自己的 header 档位拆。这样同一个 helper 既能
// 读 op13 整容器帧，也能读 op14 逐槽行。
func splitItemGrantNoticeStream(t *testing.T, service *Service, stream []byte) []itemGrantNoticePacket {
	t.Helper()
	packets := make([]itemGrantNoticePacket, 0, 8)
	for len(stream) > 0 {
		frameSize := itemGrantNoticeHeaderSize(t, service, stream)
		packet, rest := splitGameServerUpperPacketWithHeader(t, stream, frameSize)
		packets = append(packets, itemGrantNoticePacket{
			msgID:          packet.Header.MsgID,
			classification: packet.Header.Classification,
			body:           packet.Body,
			frame:          append([]byte(nil), stream[:len(stream)-len(rest)]...),
		})
		stream = rest
	}
	return packets
}

func itemGrantNoticeHeaderSize(t *testing.T, service *Service, data []byte) int {
	t.Helper()
	if len(data) >= dnfproto.GameServerUpperHeaderSize16 &&
		data[0] == 0 &&
		binary.LittleEndian.Uint16(data[1:3]) == uint16(dnfenum.CmdPacketLeaveParty) &&
		bytes.Equal(data[11:16], make([]byte, 5)) {
		return dnfproto.GameServerUpperHeaderSize16
	}
	return service.gameUpperHeaderSize()
}

// itemGrantNoticeContainerFrames 返回流里所有 class0/msg13 整容器帧的 (list, body)。
func itemGrantNoticeContainerFrames(packets []itemGrantNoticePacket) []byte {
	lists := make([]byte, 0, len(packets))
	for _, packet := range packets {
		if packet.classification == 0 && packet.msgID == uint16(dnfenum.CmdPacketLeaveParty) {
			lists = append(lists, packet.body[0])
		}
	}
	return lists
}

// itemGrantNoticeUpdateRow 是一条 op14 行（同一包内按出现顺序）。
type itemGrantNoticeUpdateRow struct {
	listType byte
	slot     int16
	itemID   uint32
	count    uint32
}

// itemGrantNoticeUpdateRows 拆出流里所有 class0/msg14 逐槽行。list 1 的行后面固定跟两段
// u32 长度前缀的装扮 blob（current_item_update_protocol.go:23-31），必须按真实语法走，
// 否则第二行的行首会被当成 blob 长度（现场就是这条 127/119 差一字节把客户端读崩的）。
func itemGrantNoticeUpdateRows(t *testing.T, packets []itemGrantNoticePacket) []itemGrantNoticeUpdateRow {
	t.Helper()
	rows := make([]itemGrantNoticeUpdateRow, 0, len(packets))
	for _, packet := range packets {
		if packet.classification != 0 || packet.msgID != uint16(dnfenum.CmdPacketWalkoutPartyMember) {
			continue
		}
		if len(packet.body) < 3 {
			t.Fatalf("op14 body too short: %x", packet.body)
		}
		listType := packet.body[0]
		count := int(binary.LittleEndian.Uint16(packet.body[1:3]))
		rest := packet.body[3:]
		for index := 0; index < count; index++ {
			if len(rest) < currentItemListEntryWireSize {
				t.Fatalf("op14 list=%d row %d truncated: %x", listType, index, rest)
			}
			row := rest[:currentItemListEntryWireSize]
			rest = rest[currentItemListEntryWireSize:]
			rows = append(rows, itemGrantNoticeUpdateRow{
				listType: listType,
				slot:     int16(binary.LittleEndian.Uint16(row[0:2])),
				itemID:   binary.LittleEndian.Uint32(row[2:6]),
				count:    binary.LittleEndian.Uint32(row[6:10]),
			})
			if listType != 1 {
				continue
			}
			for blob := 0; blob < currentAvatarItemListOptionalBlobCount; blob++ {
				if len(rest) < 4 {
					t.Fatalf("op14 list=1 row %d blob %d prefix truncated", index, blob)
				}
				blobLen := int(binary.LittleEndian.Uint32(rest[0:4]))
				rest = rest[4:]
				if len(rest) < blobLen {
					t.Fatalf("op14 list=1 row %d blob %d len=%d exceeds body", index, blob, blobLen)
				}
				rest = rest[blobLen:]
			}
		}
		if len(rest) != 0 {
			t.Fatalf("op14 list=%d has %d trailing bytes after %d rows", listType, len(rest), count)
		}
	}
	return rows
}

// itemGrantNoticeContainerBodyRows 从整容器（msg13）body 里取出 (slot, itemID, count) 行：
// body = u8 listType + [list 私有参数] + u16 count + rows。
func itemGrantNoticeContainerBodyRows(t *testing.T, listType byte, body []byte) []itemGrantNoticeUpdateRow {
	t.Helper()
	header := 1
	switch listType {
	case 0, 1, 2:
		header += 2
	case 12:
		header += 6
	}
	if len(body) < header+2 {
		t.Fatalf("container list=%d body too short: %x", listType, body)
	}
	count := int(binary.LittleEndian.Uint16(body[header : header+2]))
	rowSize := currentItemListEntrySizeForType(listType)
	rest := body[header+2:]
	rows := make([]itemGrantNoticeUpdateRow, 0, count)
	for index := 0; index < count; index++ {
		if len(rest) < rowSize {
			t.Fatalf("container list=%d row %d truncated", listType, index)
		}
		row := rest[:currentItemListEntryWireSize]
		rows = append(rows, itemGrantNoticeUpdateRow{
			listType: listType,
			slot:     int16(binary.LittleEndian.Uint16(row[0:2])),
			itemID:   binary.LittleEndian.Uint32(row[2:6]),
			count:    binary.LittleEndian.Uint32(row[6:10]),
		})
		rest = rest[rowSize:]
	}
	return rows
}

func requireItemGrantNoticeRow(t *testing.T, rows []itemGrantNoticeUpdateRow, listType byte, slot int16, itemID uint32, count uint32) {
	t.Helper()
	for _, row := range rows {
		if row.listType != listType || row.slot != slot {
			continue
		}
		if row.itemID != itemID || row.count != count {
			t.Fatalf("op14 row list=%d slot=%d = item %d count %d, want item %d count %d",
				listType, slot, row.itemID, row.count, itemID, count)
		}
		return
	}
	t.Fatalf("op14 row list=%d slot=%d is missing (rows=%+v)", listType, slot, rows)
}

func requireNoItemGrantNoticeRow(t *testing.T, rows []itemGrantNoticeUpdateRow, listType byte) {
	t.Helper()
	for _, row := range rows {
		if row.listType == listType {
			t.Fatalf("op14 row for list %d must not be emitted: %+v", listType, row)
		}
	}
}

// ---------------------------------------------------------------------------
// 1. 开关语义
// ---------------------------------------------------------------------------

func TestItemGrantNoticeModeDefaultsToDeltaAndFollowsEnv(t *testing.T) {
	// 默认档 = 修复档：未设置、空、无法识别的值全是 delta（"零值即修复档"）。
	for _, value := range []string{"", " ", "delta", "DELTA", " delta ", "garbage", "fixed"} {
		if got := normalizeItemGrantNoticeMode(value); got != itemGrantNoticeDelta {
			t.Fatalf("normalizeItemGrantNoticeMode(%q) = %q, want %q", value, got, itemGrantNoticeDelta)
		}
	}
	// 一键回退：只有显式写 containers 及其别名才回到今天的整容器口径。
	for _, value := range []string{"containers", " CONTAINERS ", "container", "containers_only", "full", "legacy", "current", "off", "0", "false", "no"} {
		if got := normalizeItemGrantNoticeMode(value); got != itemGrantNoticeContainers {
			t.Fatalf("normalizeItemGrantNoticeMode(%q) = %q, want %q", value, got, itemGrantNoticeContainers)
		}
	}

	t.Setenv(itemGrantNoticeEnv, "")
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s unset must default to the delta (fixed) shape", itemGrantNoticeEnv)
	}
	t.Setenv(itemGrantNoticeEnv, "containers")
	if itemGrantNoticeUsesDelta() {
		t.Fatalf("%s=containers must roll back to the container replay", itemGrantNoticeEnv)
	}
	t.Setenv(itemGrantNoticeEnv, "containers")
	if got := itemGrantNoticeMode(); got != itemGrantNoticeContainers {
		t.Fatalf("rollback mode = %q, want %q", got, itemGrantNoticeContainers)
	}
}

// ---------------------------------------------------------------------------
// 2. 计划划分（两条硬例外）
// ---------------------------------------------------------------------------

func TestItemGrantNoticePlanKeepsPetDeferredAndMedalContainerOnly(t *testing.T) {
	plan := buildItemGrantNoticePlan([]alignedcmd.ItemSlotRefresh{
		{ListType: 0, SlotIndex: 3},
		{ListType: 1, SlotIndex: 0},
		{ListType: currentPetInventoryListType, SlotIndex: 189},
		{ListType: currentGuildMedalInventoryListType, SlotIndex: 0},
		{ListType: currentGuildMedalInventoryListType, SlotIndex: 5},
		{ListType: 1, SlotIndex: 0},  // 重复项必须去重
		{ListType: 0, SlotIndex: -1}, // 非法槽位必须忽略
	})
	wantDelta := []alignedcmd.ItemSlotRefresh{
		{ListType: 0, SlotIndex: 3},
		{ListType: 1, SlotIndex: 0},
		{ListType: currentPetInventoryListType, SlotIndex: 189},
	}
	if !reflect.DeepEqual(plan.Delta, wantDelta) {
		t.Fatalf("delta plan = %+v, want %+v", plan.Delta, wantDelta)
	}
	// list 7 进 Delta 而不是 ContainerLists：它的 op14 新建行会让客户端在库存界面模块
	// 0x23082D3 闪退（current_item_list_send.go:222-230），发送侧会把 list 7 改期成
	// 1500 ms 后的整容器 —— 这条由 TestItemGrantNoticeDeltaDefersPetRowToDeferredContainer 钉住。
	if !reflect.DeepEqual(plan.ContainerLists, []byte{currentGuildMedalInventoryListType}) {
		t.Fatalf("container lists = %v, want [%d]", plan.ContainerLists, currentGuildMedalInventoryListType)
	}
	for _, refresh := range plan.Delta {
		if itemGrantNoticeListUsesContainerPath(refresh.ListType) {
			t.Fatalf("list %d must not be projected as a per-slot delta row", refresh.ListType)
		}
	}
	// 空写入 ⇒ 两个集合都空（"这次没有任何行要通知"）。
	empty := buildItemGrantNoticePlan(nil)
	if len(empty.Delta) != 0 || len(empty.ContainerLists) != 0 {
		t.Fatalf("empty plan = %+v, want both empty", empty)
	}
}

// ---------------------------------------------------------------------------
// 3. op160 礼盒（gameplay_module_booster.go）
// ---------------------------------------------------------------------------

// boosterGrantFixture 建一个"主背包 slot 75 有一个 op160 礼盒"的会话，返回写出的字节流。
func boosterGrantFixture(t *testing.T, body []byte) (dnfrepo.Group, *bufferConn, []byte) {
	t.Helper()
	catalog := mustCurrentBoosterTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:75": {ItemID: 500, Count: 2}},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := currentBoosterTestService(catalog, repositories)
	queue := &itemGrantNoticeTimerQueue{now: time.Now().UTC()}
	service.gameplayTimers = queue
	session := &gameSession{conn: connection, connID: "item-grant-notice-booster", selectedCharacterID: 19}
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketUseBoosterItem),
		body,
	); err != nil {
		t.Fatal(err)
	}
	return repositories, connection, append([]byte(nil), connection.write.Bytes()...)
}

// TestItemGrantNoticeDeltaSendsBoosterRewardRowsWithoutContainerReplay 是本批的主用例之一：
// 默认档下 op160 把"源格 0:75 + 奖励落点 0:3"发成**一条** op14（两行），并且**一个整容器
// 都不发** —— 客户端的分页高亮读的就是这条逐槽行。
func TestItemGrantNoticeDeltaSendsBoosterRewardRowsWithoutContainerReplay(t *testing.T) {
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s must default to delta", itemGrantNoticeEnv)
	}
	repositories, _, stream := boosterGrantFixture(t, []byte{75, 0})

	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13}}
	packets := splitItemGrantNoticeStream(t, service, stream)
	// ACK + 一条 op14（同一容器的两行打成一个包，行内容从仓储重读）。
	if len(packets) != 2 {
		t.Fatalf("delta stream packets = %d, want ACK + one op14 packet: %+v", len(packets), packets)
	}
	ack := packets[0]
	if ack.msgID != uint16(dnfenum.CmdPacketUseBoosterItem) || ack.body[0] != 1 {
		t.Fatalf("op160 ACK = msg %d body %x", ack.msgID, ack.body)
	}
	// 源槽 op14 行（今天那条，逐字节同形）与奖励落点行现在在同一个 op14 包里。
	rows := itemGrantNoticeUpdateRows(t, packets[1:])
	if len(rows) != 2 {
		t.Fatalf("delta rows = %+v, want the source row and the reward row", rows)
	}
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 75, 500, 1)
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 121, 600, 3)
	if lists := itemGrantNoticeContainerFrames(packets); len(lists) != 0 {
		t.Fatalf("delta mode must not replay whole containers, got lists %v", lists)
	}

	inventory, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if reward := inventory.Slots["0:121"]; reward.ItemID != 600 || reward.Count != 3 {
		t.Fatalf("reward stack = %+v, want item 600 count 3 (the projected row must be authoritative)", reward)
	}
}

// TestItemGrantNoticeContainersRollbackKeepsBoosterFullContainerStream 钉住回退档：
// ACK + 源槽单行 op14 + list 0 整容器 op13，一条奖励逐槽行都不多发。
func TestItemGrantNoticeContainersRollbackKeepsBoosterFullContainerStream(t *testing.T) {
	pinItemGrantNoticeContainers(t)
	_, _, stream := boosterGrantFixture(t, []byte{75, 0})

	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13}}
	packets := splitItemGrantNoticeStream(t, service, stream)
	if len(packets) != 3 {
		t.Fatalf("containers stream packets = %d, want ACK + one op14 + one op13: %+v", len(packets), packets)
	}
	if got := itemGrantNoticeContainerFrames(packets); !reflect.DeepEqual(got, []byte{dnfrepo.MainInventoryListType}) {
		t.Fatalf("containers mode lists = %v, want [0]", got)
	}
	rows := itemGrantNoticeUpdateRows(t, packets)
	if len(rows) != 1 {
		t.Fatalf("containers mode must keep exactly the source row, got %+v", rows)
	}
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 75, 500, 1)
	// 回退档下奖励落点**只能**从整容器里被客户端看到：0:121 那一行必须出现在 list0 帧里。
	list0 := packets[2]
	if list0.msgID != uint16(dnfenum.CmdPacketLeaveParty) || list0.body[0] != dnfrepo.MainInventoryListType {
		t.Fatalf("list0 container frame = msg %d list %d", list0.msgID, list0.body[0])
	}
	containerRows := itemGrantNoticeContainerBodyRows(t, dnfrepo.MainInventoryListType, list0.body)
	requireItemGrantNoticeRow(t, containerRows, dnfrepo.MainInventoryListType, 121, 600, 3)
	requireItemGrantNoticeRow(t, containerRows, dnfrepo.MainInventoryListType, 75, 500, 1)
}

// TestItemGrantNoticeDeltaDefersPetRowToDeferredContainer 钉住 list 7 硬例外：
// delta 档下**一条 list 7 的 op14 行都不发**（新建行会让客户端 0x23082D3 闪退），
// 改由既有的一段改期逻辑在 1500 ms 后整容器下发。
func TestItemGrantNoticeDeltaDefersPetRowToDeferredContainer(t *testing.T) {
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s must default to delta", itemGrantNoticeEnv)
	}
	catalog := mustCurrentBoosterTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:76": {ItemID: 503, Count: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := currentBoosterTestService(catalog, repositories)
	queue := &itemGrantNoticeTimerQueue{now: time.Now().UTC()}
	service.gameplayTimers = queue
	session := &gameSession{conn: connection, connID: "item-grant-notice-pet", selectedCharacterID: 19}
	body := make([]byte, currentBoosterSelectionBodySize)
	binary.LittleEndian.PutUint16(body[0:2], 76)
	binary.LittleEndian.PutUint32(body[4:8], 702)
	if err := service.handleGameCommand(session, byte(dnfenum.GameCmdCommand), uint16(dnfenum.CmdPacketUseBoosterItem), body); err != nil {
		t.Fatal(err)
	}

	immediate := append([]byte(nil), connection.write.Bytes()...)
	packets := splitItemGrantNoticeStream(t, service, immediate)
	rows := itemGrantNoticeUpdateRows(t, packets)
	requireNoItemGrantNoticeRow(t, rows, currentPetInventoryListType)
	if lists := itemGrantNoticeContainerFrames(packets); len(lists) != 0 {
		t.Fatalf("delta mode must not replay containers immediately, got lists %v", lists)
	}
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 76, uint32(^uint32(0)), 0)

	// 改期的那一条必须真的存在，且落点是 list 7 的整容器。
	if len(queue.scheduled) != 1 {
		t.Fatalf("deferred pet refresh schedules = %d, want 1 (%+v)", len(queue.scheduled), queue.scheduled)
	}
	if queue.scheduled[0].delay != currentPetInventoryRefreshDelay {
		t.Fatalf("deferred pet refresh delay = %s, want %s", queue.scheduled[0].delay, currentPetInventoryRefreshDelay)
	}
	connection.write.Reset()
	queue.fire(t)
	deferred := splitItemGrantNoticeStream(t, service, connection.write.Bytes())
	if got := itemGrantNoticeContainerFrames(deferred); !reflect.DeepEqual(got, []byte{currentPetInventoryListType}) {
		t.Fatalf("deferred pet container lists = %v, want [7]", got)
	}
	if rows := itemGrantNoticeUpdateRows(t, deferred); len(rows) != 0 {
		t.Fatalf("deferred pet refresh must be a whole container, got rows %+v", rows)
	}
	inventory, found, err := repositories.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	if pet := inventory.Slots["7:0"]; pet.ItemID != 702 {
		t.Fatalf("pet reward stack = %+v, want item 702 in list 7", pet)
	}
}

// ---------------------------------------------------------------------------
// 4. op518 商城礼包（cera_package.go）
// ---------------------------------------------------------------------------

func ceraPackageGrantStream(t *testing.T) []byte {
	t.Helper()
	catalog := mustCurrentCeraPackageTestCatalog(t)
	repositories := dnfrepomemory.NewMemoryGroup()
	ctx := context.Background()
	if err := repositories.Character.Save(ctx, dnfrepo.CharacterRecord{CharacterID: "19", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	sourceStack := dnfrepo.ItemStack{ItemID: 500, Count: 2}
	sourceEntry := currentItemListEntryFromStack(dnfrepo.MainInventoryListType, 75, sourceStack)
	sourceStack.RawEntry = append([]byte(nil), sourceEntry.data[:]...)
	if err := repositories.Inventory.Save(ctx, dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       map[string]dnfrepo.ItemStack{"0:75": sourceStack},
	}); err != nil {
		t.Fatal(err)
	}
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "account-1",
			gameUpperHeader:    gameUpperHeaderChannel13,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
		},
		pvfItemCatalog:     catalog,
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	session := &gameSession{conn: connection, connID: "item-grant-notice-cera", selectedCharacterID: 19}
	if err := service.handleGameCommand(
		session,
		byte(dnfenum.GameCmdCommand),
		uint16(dnfenum.CmdPacketOpenCerapackage),
		currentCeraPackageTestRequestBody(75, currentCeraPackageChoice{ItemID: 700, Option: 9}),
	); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), connection.write.Bytes()...)
}

// TestItemGrantNoticeDeltaSendsCeraPackageRewardRowsWithoutContainerReplay：默认档下 op518
// 把"源格 0:75 + 奖励 0:3 + 装扮 1:0"发成两条 op14（list 0 两行、list 1 一行），
// **装扮页（list 1）因此拿到逐槽行**，整容器一个都不发。
func TestItemGrantNoticeDeltaSendsCeraPackageRewardRowsWithoutContainerReplay(t *testing.T) {
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s must default to delta", itemGrantNoticeEnv)
	}
	stream := ceraPackageGrantStream(t)
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13}}
	packets := splitItemGrantNoticeStream(t, service, stream)
	if len(packets) != 3 {
		t.Fatalf("delta stream packets = %d, want ACK + list0 op14 + list1 op14: %+v", len(packets), packets)
	}
	ack := packets[0]
	if ack.msgID != uint16(dnfenum.CmdPacketOpenCerapackage) || !bytes.Equal(ack.body, []byte{1, 75, 0}) {
		t.Fatalf("op518 ACK = msg %d body %x", ack.msgID, ack.body)
	}
	rows := itemGrantNoticeUpdateRows(t, packets)
	if len(rows) != 3 {
		t.Fatalf("delta rows = %+v, want 3 rows", rows)
	}
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 75, 500, 1)
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 121, 600, 3)
	// 装扮行（list 1）的数量不在固定行的 [6:10]（那是 0），而在可选 blob / raw[0x0E..]
	// 的 amount_or_count 上 —— 这里只钉"这一格被逐槽通知到了"，数量投影由既有的
	// 装扮行投影测试负责。
	requireItemGrantNoticeRow(t, rows, 1, 0, 700, 0)
	if lists := itemGrantNoticeContainerFrames(packets); len(lists) != 0 {
		t.Fatalf("delta mode must not replay whole containers, got lists %v", lists)
	}
}

// TestItemGrantNoticeContainersRollbackKeepsCeraPackageFullContainerStream 钉住回退档：
// ACK + list0 + list1 两个整容器，零 op14。
func TestItemGrantNoticeContainersRollbackKeepsCeraPackageFullContainerStream(t *testing.T) {
	pinItemGrantNoticeContainers(t)
	stream := ceraPackageGrantStream(t)
	service := &Service{options: options{gameUpperHeader: gameUpperHeaderChannel13}}
	packets := splitItemGrantNoticeStream(t, service, stream)
	if len(packets) != 3 {
		t.Fatalf("containers stream packets = %d, want ACK + list0 + list1: %+v", len(packets), packets)
	}
	if got := itemGrantNoticeContainerFrames(packets); !reflect.DeepEqual(got, []byte{0, 1}) {
		t.Fatalf("containers mode lists = %v, want [0 1]", got)
	}
	if rows := itemGrantNoticeUpdateRows(t, packets); len(rows) != 0 {
		t.Fatalf("containers mode must not send any per-slot row, got %+v", rows)
	}
}

// ---------------------------------------------------------------------------
// 5. op208/op1128 魔盒（packageitem owner → PostActionRefreshSelectedItemContainers）
// ---------------------------------------------------------------------------

// magicBoxGrantResult 复刻 packageitem/handler.go:191-208 交给 bridge 的 Result 形状。
func magicBoxGrantResult() alignedcmd.Result {
	return alignedcmd.Result{
		Handled:   true,
		Operation: boxOpenOperationRandombox,
		UpperResponses: []alignedcmd.UpperResponse{{
			MsgID:          uint16(dnfenum.CmdPacketUseRandomboxItem),
			Body:           []byte{1, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			Classification: dnfproto.DefaultChannelClassification,
			AllowCodec:     true,
		}},
		PostActions: []alignedcmd.PostAction{alignedcmd.PostActionRefreshSelectedItemContainers},
	}
}

// itemGrantNoticeMagicBoxCommit 直接落到仓储上，等价于 packageitem 事务提交后的状态：
// 开盒扣掉 0:44 那一格，奖励落进 writes。
func itemGrantNoticeMagicBoxCommit(t *testing.T, fixture *refreshCostFixture, writes map[string]dnfrepo.ItemStack) {
	t.Helper()
	ctx := context.Background()
	record, found, err := fixture.group.Inventory.Load(ctx, "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	delete(record.Slots, "0:44")
	for key, stack := range writes {
		record.Slots[key] = stack
	}
	if err := fixture.group.Inventory.Save(ctx, record); err != nil {
		t.Fatal(err)
	}
}

// TestItemGrantNoticeDeltaReplacesMagicBoxFullContainerReplay：默认档下 op208 的奖励通知
// 是"提交前后差分出来的精确 (list, slot)"逐槽行，回包后置动作**一条整容器都不发**
// （今天这里是 5 个整容器 / 11,333 B）。
func TestItemGrantNoticeDeltaReplacesMagicBoxFullContainerReplay(t *testing.T) {
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s must default to delta", itemGrantNoticeEnv)
	}
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)

	// 生产路径第一步：分发 op208 之前装"发放前快照"。
	snapshot := fixture.service.armItemGrantSnapshot(fixture.session, fixture.group, dnfenum.CmdPacketUseRandomboxItem)
	if snapshot == nil {
		t.Fatal("delta mode must arm the pre-command grant snapshot for op208")
	}
	// 装扮奖励落 list 1（操作者报的「装扮」分页就是这一页）。
	itemGrantNoticeMagicBoxCommit(t, fixture, map[string]dnfrepo.ItemStack{
		"1:0": {ItemID: 3227, Count: 1},
	})
	// 生产路径第二步：算差分、挂 ItemSlotRefreshes。
	result := magicBoxGrantResult()
	fixture.service.applyItemGrantSnapshot(fixture.session, fixture.group, snapshot, &result)
	if len(result.ItemSlotRefreshes) != 2 {
		t.Fatalf("delta plan = %+v, want the consumed slot 0:44 and the granted slot 1:0", result.ItemSlotRefreshes)
	}
	fixture.buffer.write.Reset()
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatal(err)
	}

	packets := splitItemGrantNoticeStream(t, fixture.service, fixture.buffer.write.Bytes())
	if len(packets) != 3 {
		t.Fatalf("delta stream packets = %d, want ACK + two op14 packets: %+v", len(packets), packets)
	}
	if packets[0].msgID != uint16(dnfenum.CmdPacketUseRandomboxItem) {
		t.Fatalf("first packet msg = %d, want the box ACK", packets[0].msgID)
	}
	rows := itemGrantNoticeUpdateRows(t, packets)
	if len(rows) != 2 {
		t.Fatalf("delta rows = %+v, want two rows", rows)
	}
	requireItemGrantNoticeRow(t, rows, dnfrepo.MainInventoryListType, 44, ^uint32(0), 0)
	// This routing fixture uses the shared placeholder item ID in list 1.
	// The avatar row's +0x06 is remaining seconds (permanent zero), while
	// the repository quantity remains one. Do not confuse the two fields.
	requireItemGrantNoticeRow(t, rows, 1, 0, 3227, 0)
	inventory, found, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil || !found || inventory.Slots["1:0"].ItemID != 3227 || inventory.Slots["1:0"].Count != 1 {
		t.Fatalf("avatar durable reward changed: found=%t err=%v stack=%+v", found, err, inventory.Slots["1:0"])
	}
	if lists := itemGrantNoticeContainerFrames(packets); len(lists) != 0 {
		t.Fatalf("delta mode must not replay whole containers, got lists %v", lists)
	}
	if fixture.session.itemGrantNoticeArmed {
		t.Fatal("the grant notice plan must be consumed by the post action")
	}
}

// TestItemGrantNoticeDeltaKeepsGuildMedalListOnTheContainerPath 钉住 list 38 硬例外：
// 勋章/守护珠的写入**不发 op14 行**，仍然整容器回放（inventory/handler.go:877-883
// 明确记过"list 38 与 list 7 一样不能走 op14 增量行"）。
func TestItemGrantNoticeDeltaKeepsGuildMedalListOnTheContainerPath(t *testing.T) {
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s must default to delta", itemGrantNoticeEnv)
	}
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	snapshot := fixture.service.armItemGrantSnapshot(fixture.session, fixture.group, dnfenum.CmdPacketUseRandomboxItem)
	if snapshot == nil {
		t.Fatal("delta mode must arm the pre-command grant snapshot for op208")
	}
	itemGrantNoticeMagicBoxCommit(t, fixture, map[string]dnfrepo.ItemStack{
		"1:0":  {ItemID: 3227, Count: 1},
		"38:0": {ItemID: 3227, Count: 1},
	})
	result := magicBoxGrantResult()
	fixture.service.applyItemGrantSnapshot(fixture.session, fixture.group, snapshot, &result)
	fixture.buffer.write.Reset()
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatal(err)
	}

	packets := splitItemGrantNoticeStream(t, fixture.service, fixture.buffer.write.Bytes())
	rows := itemGrantNoticeUpdateRows(t, packets)
	requireNoItemGrantNoticeRow(t, rows, currentGuildMedalInventoryListType)
	// Permanent avatar time is zero; neither it nor the medal routing
	// exception may change the committed inventory quantities.
	requireItemGrantNoticeRow(t, rows, 1, 0, 3227, 0)
	inventory, found, err := fixture.group.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load committed reward inventory found=%t err=%v", found, err)
	}
	for _, key := range []string{"1:0", "38:0"} {
		if stack := inventory.Slots[key]; stack.ItemID != 3227 || stack.Count != 1 {
			t.Fatalf("reward quantity changed key=%s stack=%+v", key, stack)
		}
	}
	if got := itemGrantNoticeContainerFrames(packets); !reflect.DeepEqual(got, []byte{currentGuildMedalInventoryListType}) {
		t.Fatalf("list 38 must keep the whole-container path, container lists = %v, want [38]", got)
	}
}

// TestItemGrantNoticeDeltaDefersMagicBoxPetRowsToDeferredContainer：魔盒奖励落 list 7 时
// 同样只走"改期整容器"，绝不发 list 7 的 op14 行。
func TestItemGrantNoticeDeltaDefersMagicBoxPetRowsToDeferredContainer(t *testing.T) {
	if !itemGrantNoticeUsesDelta() {
		t.Fatalf("%s must default to delta", itemGrantNoticeEnv)
	}
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	queue := &itemGrantNoticeTimerQueue{now: time.Now().UTC()}
	fixture.service.gameplayTimers = queue

	snapshot := fixture.service.armItemGrantSnapshot(fixture.session, fixture.group, dnfenum.CmdPacketUseRandomboxItem)
	if snapshot == nil {
		t.Fatal("delta mode must arm the pre-command grant snapshot for op208")
	}
	itemGrantNoticeMagicBoxCommit(t, fixture, map[string]dnfrepo.ItemStack{
		"7:189": {ItemID: 3227, Count: 1},
	})
	result := magicBoxGrantResult()
	fixture.service.applyItemGrantSnapshot(fixture.session, fixture.group, snapshot, &result)
	fixture.buffer.write.Reset()
	if err := fixture.service.sendAlignedUpperResponses(fixture.session, result); err != nil {
		t.Fatal(err)
	}
	packets := splitItemGrantNoticeStream(t, fixture.service, fixture.buffer.write.Bytes())
	requireNoItemGrantNoticeRow(t, itemGrantNoticeUpdateRows(t, packets), currentPetInventoryListType)
	if lists := itemGrantNoticeContainerFrames(packets); len(lists) != 0 {
		t.Fatalf("delta mode must not replay containers immediately, got lists %v", lists)
	}
	if len(queue.scheduled) != 1 || queue.scheduled[0].delay != currentPetInventoryRefreshDelay {
		t.Fatalf("deferred pet refresh = %+v, want one %s timer", queue.scheduled, currentPetInventoryRefreshDelay)
	}
	fixture.buffer.write.Reset()
	queue.fire(t)
	deferred := splitItemGrantNoticeStream(t, fixture.service, fixture.buffer.write.Bytes())
	if got := itemGrantNoticeContainerFrames(deferred); !reflect.DeepEqual(got, []byte{currentPetInventoryListType}) {
		t.Fatalf("deferred pet container lists = %v, want [7]", got)
	}
}

// TestItemGrantNoticeContainersRollbackKeepsMagicBoxFullContainerReplay 钉住回退档：
// 没有 delta 计划时后置动作仍然是今天的五个整容器（夹具 6 包 / 11,333 B）。
func TestItemGrantNoticeContainersRollbackKeepsMagicBoxFullContainerReplay(t *testing.T) {
	pinItemGrantNoticeContainers(t)
	fixture := newRefreshCostFixture(t)
	fixture.warmUp(t)
	if snapshot := fixture.service.armItemGrantSnapshot(fixture.session, fixture.group, dnfenum.CmdPacketUseRandomboxItem); snapshot != nil {
		t.Fatal("the rollback mode must not read any extra repository state")
	}
	run := sendBoxOpenPostAction(t, fixture, boxOpenOperationRandombox, uint16(dnfenum.CmdPacketUseRandomboxItem))
	requireBoxOpenListTypes(t, run, currentSelectedItemListTypes)
	if run.packets != len(currentSelectedItemListTypes)+1 || run.bytes != 11333 {
		t.Fatalf("rollback stream = %d packets / %d bytes, want %d / 11333",
			run.packets, run.bytes, len(currentSelectedItemListTypes)+1)
	}
	if fixture.session.itemGrantNoticeArmed {
		t.Fatal("the rollback mode must never arm the delta plan")
	}
}

// itemGrantNoticeTimerQueue 是只把回调记下来的假定时器队列：让"改期整容器"这条路径
// 在单元测试里可以被显式触发（生产用 processGameplayTimeQueue）。
type itemGrantNoticeTimerQueue struct {
	now       time.Time
	scheduled []itemGrantNoticeTimer
}

type itemGrantNoticeTimer struct {
	name     string
	delay    time.Duration
	callback timequeue.Callback
}

func (q *itemGrantNoticeTimerQueue) Now() time.Time { return q.now }

func (q *itemGrantNoticeTimerQueue) ScheduleAfter(name string, delay time.Duration, callback timequeue.Callback) error {
	q.scheduled = append(q.scheduled, itemGrantNoticeTimer{name: name, delay: delay, callback: callback})
	return nil
}

func (q *itemGrantNoticeTimerQueue) Cancel(name string) bool {
	for index, timer := range q.scheduled {
		if timer.name == name {
			q.scheduled = append(q.scheduled[:index], q.scheduled[index+1:]...)
			return true
		}
	}
	return false
}

func (q *itemGrantNoticeTimerQueue) Start(context.Context) {}

func (q *itemGrantNoticeTimerQueue) fire(t *testing.T) {
	t.Helper()
	pending := q.scheduled
	q.scheduled = nil
	for _, timer := range pending {
		timer.callback(q.now.Add(timer.delay))
	}
}
