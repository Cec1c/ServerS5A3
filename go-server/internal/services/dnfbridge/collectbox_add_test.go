package dnfbridge

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	dnfcollectbox "longheng.io/server/internal/modules/dnf/collectbox"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 本文件钉住「收集箱 · 把宝珠放进去」这条链（客户端 class1/op1022）。
//
// 现场缺陷（操作者原话）：「收集箱直接无法交互 无法放置宝珠」。香港
// /opt/dnf90/logs/packet_log.txt 全量历史里 op1022 一条都没有，只有 4× op1021
// 与 2× op1024，且每条 op1021 都换来
//
//	EVENT kind=game-collectbox-deferred opcode=1021 body_len=1
//	  reason=collectbox_not_implemented
//
// 也就是说：服务端一个字节都不回，客户端连"放宝珠"这一步都到不了。
// 本文件的前两条用例就是这条缺陷由红转绿的门槛。

// currentCollectboxAddTestBody 按参考实现口径拼 op1022 请求体。
func currentCollectboxAddTestBody(boxIndex, sourceSlot uint16, itemID uint32) []byte {
	body := make([]byte, currentCollectboxAddBodySize)
	binary.LittleEndian.PutUint16(body[0:2], boxIndex)
	binary.LittleEndian.PutUint16(body[2:4], sourceSlot)
	binary.LittleEndian.PutUint32(body[4:8], itemID)
	return body
}

// currentCollectboxAddTestItemID 取夹具箱 2 的第一个 PVF 槛位物品 id。
func currentCollectboxAddTestItemID(t *testing.T) uint32 {
	t.Helper()
	box, ok := currentCollectboxTestCatalog(t).Box(2)
	if !ok || len(box.Slots) == 0 || box.Slots[0].ItemID <= 0 {
		t.Fatal("fixture box 2 must declare at least one PVF slot item")
	}
	return uint32(box.Slots[0].ItemID)
}

// newCurrentCollectboxAddTestService 复用 premium 测试运行期（角色 19 + 内存仓储），
// 打开收集箱两个开关并注入夹具目录。
func newCurrentCollectboxAddTestService(t *testing.T, mutation string) (*Service, *gameSession, dnfrepo.Group, *bufferConn) {
	t.Helper()
	service, session, repositories, connection := newPremiumServiceTestRuntime(t)
	service.options.collectboxMode = collectboxModeReference
	service.options.collectboxMutation = mutation
	service.collectboxCatalog = currentCollectboxTestCatalog(t)
	service.collectboxCatalogLoaded = true
	return service, session, repositories, connection
}

func seedCurrentCollectboxAddBag(t *testing.T, repositories dnfrepo.Group, slots map[string]dnfrepo.ItemStack) {
	t.Helper()
	if err := repositories.Inventory.Save(context.Background(), dnfrepo.InventoryRecord{
		CharacterID: "19",
		Slots:       slots,
	}); err != nil {
		t.Fatal(err)
	}
}

func currentCollectboxLoadSlots(t *testing.T, repositories dnfrepo.Group) map[string]dnfrepo.ItemStack {
	t.Helper()
	record, found, err := repositories.Inventory.Load(context.Background(), "19")
	if err != nil || !found {
		t.Fatalf("load inventory found=%t err=%v", found, err)
	}
	return record.Slots
}

// currentCollectboxStatePacketForBox 在已写出的包里找该箱的 class0/op1027 刷新。
func currentCollectboxStatePacketForBox(t *testing.T, packets []dnfprotoPacketView, boxIndex byte) ([]byte, bool) {
	t.Helper()
	for _, packet := range packets {
		if packet.classification != 0 || packet.msgID != currentCollectboxStateMsgID {
			continue
		}
		raw, err := hex.DecodeString(packet.bodyHex)
		if err != nil {
			t.Fatalf("decode %s: %v", packet.bodyHex, err)
		}
		if len(raw) >= currentCollectboxStateFixedBodySize && raw[0] == boxIndex {
			return raw, true
		}
	}
	return nil, false
}

func currentCollectboxPacketMsgIDs(packets []dnfprotoPacketView) string {
	ids := make([]string, 0, len(packets))
	for _, packet := range packets {
		ids = append(ids, strconv.Itoa(int(packet.msgID)))
	}
	return strings.Join(ids, ",")
}

// TestCollectboxAddMovesBeadIntoBoxAndAcks 是**本缺陷的主回归**：
// 走真实模块派发（客户端 class1/op1022 落到的那条路），打开开关后一条 op1022 必须
// 换来 ①背包那一格被刷新 ②class0/op1027 里出现该宝珠 ③class1/op1022 成功应答 `01`，
// 并且宝珠真的落库。
//
// 刻意经过 mustGameplayModuleRegistry 而不是直接调 handler：pre-fix 的缺陷正是
// "op1022 被路由到只观测的分支"，只测 handler 会漏掉这类回归。
func TestCollectboxAddMovesBeadIntoBoxAndAcks(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	itemID := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(itemID), Count: 1},
	})
	session.collectboxInitialized = true
	session.collectboxSelectedIndex = 2
	connection.write.Reset()

	registry := mustGameplayModuleRegistry(collectboxGameplayModule())
	handled, err := registry.DispatchLegacy(service, session, currentCollectboxAddItemMsgID,
		currentCollectboxAddTestBody(2, 30, itemID))
	if err != nil {
		t.Fatalf("dispatch op1022: %v", err)
	}
	if !handled {
		t.Fatal("op1022 must be owned by the collectbox module")
	}

	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) == 0 {
		t.Fatal("op1022 must answer; 修复前这里一个字节都不发（这就是操作者看到的『放不进去』）")
	}
	// ③ 最后一条必须是 op1022 的 class1 成功应答，body 固定 `01`。
	ack := packets[len(packets)-1]
	if ack.classification != 1 || ack.msgID != currentCollectboxAddItemMsgID || ack.bodyHex != "01" {
		t.Fatalf("ack = class%d msg%d body=%s want class1 msg%d body 01",
			ack.classification, ack.msgID, ack.bodyHex, currentCollectboxAddItemMsgID)
	}
	// ② 必须有一条 class0/op1027 把该箱标成"有 1 件"，且条目 id 就是那颗宝珠。
	raw, ok := currentCollectboxStatePacketForBox(t, packets, 2)
	if !ok {
		t.Fatalf("no class0/op1027 refresh for box 2 in msg ids [%s]", currentCollectboxPacketMsgIDs(packets))
	}
	// 选中标志照实写：箱 2 就是本会话选中箱，所以必须是 1。
	if raw[1] != currentCollectboxSelectedFlag {
		t.Fatalf("box 2 selected flag = %d want %d", raw[1], currentCollectboxSelectedFlag)
	}
	if raw[7] != 1 {
		t.Fatalf("box 2 item_count = %d want 1 (body=%s)", raw[7], hex.EncodeToString(raw))
	}
	if len(raw) < currentCollectboxStateFixedBodySize+4 {
		t.Fatalf("box 2 body too short: %s", hex.EncodeToString(raw))
	}
	if got := binary.LittleEndian.Uint32(raw[8:12]); got != itemID {
		t.Fatalf("box 2 item id = %d want %d (body=%s)", got, itemID, hex.EncodeToString(raw))
	}
	// ① 背包那一格必须已经被刷新掉（宝珠离开背包）。
	if !currentCollectboxHasPacket(packets, 0, uint16(dnfenum.CmdPacketWalkoutPartyMember)) {
		t.Fatalf("no inventory slot refresh in msg ids [%s]", currentCollectboxPacketMsgIDs(packets))
	}
	// 落库：重登后仍能读到。
	slots := currentCollectboxLoadSlots(t, repositories)
	if _, still := slots["0:30"]; still {
		t.Fatalf("bead must leave the bag, slots = %+v", slots)
	}
	if got := dnfcollectbox.RegisteredItemIDs(dnfrepo.InventoryRecord{Slots: slots}, 2); len(got) != 1 || got[0] != int32(itemID) {
		t.Fatalf("registered = %v want [%d]", got, itemID)
	}
}

func TestCollectboxAddSecondBeadKeepsCompleteImmediateState(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	box, _ := service.collectboxCatalog.Box(2)
	if len(box.Slots) < 2 {
		t.Fatal("fixture needs two beads")
	}
	first, second := uint32(box.Slots[0].ItemID), uint32(box.Slots[1].ItemID)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(first), Count: 1},
		"0:31": {ItemID: int64(second), Count: 2},
	})
	session.collectboxSelectedIndex = 2
	for index, item := range []uint32{first, second} {
		connection.write.Reset()
		if err := service.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(2, uint16(30+index), item)); err != nil {
			t.Fatal(err)
		}
		raw, ok := currentCollectboxStatePacketForBox(t, currentCollectboxWrittenPackets(t, session), 2)
		if !ok || int(raw[7]) != index+1 {
			t.Fatalf("insert %d state=%x", index, raw)
		}
		if binary.LittleEndian.Uint32(raw[8:12]) != first {
			t.Fatalf("first bead disappeared: %x", raw)
		}
		if index == 1 && binary.LittleEndian.Uint32(raw[12:16]) != second {
			t.Fatalf("second bead missing: %x", raw)
		}
	}
	slots := currentCollectboxLoadSlots(t, repositories)
	if slots["0:31"].Count != 1 || len(dnfcollectbox.RegisteredItemIDs(dnfrepo.InventoryRecord{Slots: slots}, 2)) != 2 {
		t.Fatalf("persisted slots=%+v", slots)
	}
}

func currentCollectboxHasPacket(packets []dnfprotoPacketView, classification byte, msgID uint16) bool {
	for _, packet := range packets {
		if packet.classification == classification && packet.msgID == msgID {
			return true
		}
	}
	return false
}

// TestCollectboxAddSurvivesRelogin 把"重登后保留"钉死：用**新的** Service 实例
// 走真实读库路径，同一角色必须还能在箱子里看到那颗宝珠。
func TestCollectboxAddSurvivesRelogin(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	itemID := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(itemID), Count: 1},
	})
	session.collectboxInitialized = true
	connection.write.Reset()
	if err := service.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(2, 30, itemID)); err != nil {
		t.Fatalf("add: %v", err)
	}

	// 重登：同一份仓储，全新 Service / session（缓存全空）。
	relogin := &Service{
		options: options{
			accountID:          "account-1",
			collectboxMode:     collectboxModeReference,
			collectboxMutation: collectboxMutationReference,
		},
		repositoryProvider: func() (dnfrepo.Group, bool) { return repositories, true },
	}
	relogin.collectboxCatalog = currentCollectboxTestCatalog(t)
	relogin.collectboxCatalogLoaded = true
	reloginSession := &gameSession{conn: &bufferConn{}, connID: "relogin", selectedCharacterID: 19}
	box, ok := relogin.collectboxCatalog.Box(2)
	if !ok {
		t.Fatal("fixture box 2 missing")
	}
	ids := relogin.currentCollectboxRegisteredItemIDs(reloginSession, box)
	if len(ids) != 1 || ids[0] != int32(itemID) {
		t.Fatalf("after relogin registered = %v want [%d]", ids, itemID)
	}
}

// TestCollectboxAddObserveOnlyKeepsLegacyBehaviour 钉住"一键回退"：
// DNFBRIDGE_COLLECTBOX_MUTATION=observe_only 时必须恢复修复前行为
// （不发包、不搬物品），只留观测日志。
func TestCollectboxAddObserveOnlyKeepsLegacyBehaviour(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationObserveOnly)
	itemID := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(itemID), Count: 1},
	})
	connection.write.Reset()
	if err := service.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(2, 30, itemID)); err != nil {
		t.Fatalf("add: %v", err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("observe_only wrote %d bytes, want 0", connection.write.Len())
	}
	slots := currentCollectboxLoadSlots(t, repositories)
	if slots["0:30"].ItemID != int64(itemID) {
		t.Fatalf("observe_only must not move the bead, slots = %+v", slots)
	}
	if got := dnfcollectbox.RegisteredItemIDs(dnfrepo.InventoryRecord{Slots: slots}, 2); len(got) != 0 {
		t.Fatalf("observe_only must not register, got %v", got)
	}
}

// TestCollectboxAddRejectsUnverifiedBodies 钉住"不猜偏移"：长度不对或字段非法的
// 请求体必须回失败应答（`00 <code>`）而不是静默丢，否则客户端会一直等。
func TestCollectboxAddRejectsUnverifiedBodies(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	itemID := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(itemID), Count: 1},
	})
	long := append(currentCollectboxAddTestBody(2, 30, itemID), 0)
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "short", body: []byte{2, 0, 30, 0}},
		{name: "long", body: long},
		{name: "zero item", body: currentCollectboxAddTestBody(2, 30, 0)},
		{name: "zero box", body: currentCollectboxAddTestBody(0, 30, itemID)},
	} {
		connection.write.Reset()
		if err := service.handleCurrentCollectboxAdd(session, test.body); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		packets := currentCollectboxWrittenPackets(t, session)
		if len(packets) != 1 || packets[0].classification != 1 ||
			packets[0].msgID != currentCollectboxAddItemMsgID ||
			!strings.HasPrefix(packets[0].bodyHex, "00") {
			t.Fatalf("%s: packets = %+v want one class1/op%d failure ack",
				test.name, packets, currentCollectboxAddItemMsgID)
		}
	}
}

// TestCollectboxAddRejectsBeadOutsidePVF 钉住服务端权威：不在该箱 PVF 槛位上的
// 物品即使客户端硬发也必须被拒，且背包不动。
func TestCollectboxAddRejectsBeadOutsidePVF(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	const foreignItemID uint32 = 999999
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(foreignItemID), Count: 1},
	})
	connection.write.Reset()
	if err := service.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(2, 30, foreignItemID)); err != nil {
		t.Fatalf("add: %v", err)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) == 0 || !strings.HasPrefix(packets[len(packets)-1].bodyHex, "00") {
		t.Fatalf("packets = %+v want failure ack", packets)
	}
	if slots := currentCollectboxLoadSlots(t, repositories); slots["0:30"].ItemID != int64(foreignItemID) {
		t.Fatalf("rejected add must leave the bag untouched, slots = %+v", slots)
	}
}

// TestCollectboxAddBodyParserMatchesReferenceLayout 钉住请求读数口径
// （86JP CollectionBoxHandler.HandleInsertCollectBoxItem：u16 box / u16 slot / u32 item）。
func TestCollectboxAddBodyParserMatchesReferenceLayout(t *testing.T) {
	request, ok := parseCurrentCollectboxAddRequest(currentCollectboxAddTestBody(5, 42, 490007145))
	if !ok || request.BoxIndex != 5 || request.SourceSlot != 42 || request.ItemID != 490007145 {
		t.Fatalf("request = %+v ok=%t", request, ok)
	}
	if _, ok := parseCurrentCollectboxAddRequest(nil); ok {
		t.Fatal("empty body must not parse")
	}
}

// TestCollectboxModuleRoutesAddOpcode 钉住模块注册：op1022 走真实处理器（开启时），
// op1023/op1024 仍然只观测。
func TestCollectboxModuleRoutesAddOpcode(t *testing.T) {
	registry := mustGameplayModuleRegistry(collectboxGameplayModule())
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	itemID := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(itemID), Count: 1},
	})
	connection.write.Reset()

	handled, err := registry.DispatchLegacy(service, session, currentCollectboxAddItemMsgID,
		currentCollectboxAddTestBody(2, 30, itemID))
	if err != nil || !handled {
		t.Fatalf("dispatch op1022 handled=%t err=%v", handled, err)
	}
	if connection.write.Len() == 0 {
		t.Fatal("op1022 must produce the add ack + refresh when the switch is on")
	}

	// op1023/op1024 请求体形态未取证 —— 保持"只观测、不发明应答"。
	connection.write.Reset()
	for _, opcode := range []uint16{
		currentCollectboxRemoveItemMsgID,
		currentCollectboxExtendExpiryMsgID,
	} {
		handled, err := registry.DispatchLegacy(service, session, opcode, []byte{0xAA, 0xBB})
		if err != nil || !handled {
			t.Fatalf("dispatch op%d handled=%t err=%v", opcode, handled, err)
		}
	}
	if connection.write.Len() != 0 {
		t.Fatalf("unverified mutation commands wrote %d bytes, want 0", connection.write.Len())
	}
}

// TestCollectboxMutationSwitchNormalization 钉住新开关的归一化与默认值。
func TestCollectboxMutationSwitchNormalization(t *testing.T) {
	for _, raw := range []string{"reference", "REFERENCE", "on", "1", "true", "yes"} {
		if got := collectboxMutationValue(raw); got != collectboxMutationReference {
			t.Fatalf("collectboxMutationValue(%q)=%q want reference", raw, got)
		}
	}
	for _, raw := range []string{"", "observe_only", "off", "0", "false", "no", "nonsense"} {
		if got := collectboxMutationValue(raw); got != collectboxMutationObserveOnly {
			t.Fatalf("collectboxMutationValue(%q)=%q want observe_only", raw, got)
		}
	}
	// 零值（测试/探针里的 Service 字面量）= 修复前行为。
	if (&Service{}).collectboxMutationEnabled() {
		t.Fatal("zero-value Service must keep the pre-fix observe-only behaviour")
	}
	// 主开关 off 时，mutation 开关不能单独打开。
	off := &Service{options: options{
		collectboxMode:     collectboxModeOff,
		collectboxMutation: collectboxMutationReference,
	}}
	if off.collectboxMutationEnabled() {
		t.Fatal("collectbox off must force mutation off")
	}
}

// TestCollectboxAddRejectsExpiredBoxAtBridge 钉住过期箱在桥这一层就被拒
// （夹具箱 1 的 [base expiration date] 在过去）。
func TestCollectboxAddRejectsExpiredBoxAtBridge(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	box, ok := service.collectboxCatalog.Box(1)
	if !ok || !box.HasBaseExpiration || box.BaseExpiration.After(time.Now()) {
		t.Fatalf("fixture box 1 must be an expired box, got %+v", box)
	}
	if len(box.Slots) == 0 || box.Slots[0].ItemID <= 0 {
		t.Fatal("fixture box 1 must declare a PVF slot item")
	}
	itemID := uint32(box.Slots[0].ItemID)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{
		"0:30": {ItemID: int64(itemID), Count: 1},
	})
	connection.write.Reset()
	if err := service.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(1, 30, itemID)); err != nil {
		t.Fatalf("add: %v", err)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) == 0 || !strings.HasPrefix(packets[len(packets)-1].bodyHex, "00") {
		t.Fatalf("packets = %+v want failure ack for the expired box", packets)
	}
	if slots := currentCollectboxLoadSlots(t, repositories); slots["0:30"].ItemID != int64(itemID) {
		t.Fatalf("expired box must not accept items, slots = %+v", slots)
	}
}

// TestCollectboxAddOwnerUnavailableIsReported 钉住没有仓储时回失败应答而不是 panic。
func TestCollectboxAddOwnerUnavailableIsReported(t *testing.T) {
	service := &Service{options: options{
		collectboxMode:     collectboxModeReference,
		collectboxMutation: collectboxMutationReference,
	}}
	service.collectboxCatalog = currentCollectboxTestCatalog(t)
	service.collectboxCatalogLoaded = true
	session := &gameSession{conn: &bufferConn{}, selectedCharacterID: 19}
	if err := service.handleCurrentCollectboxAdd(session,
		currentCollectboxAddTestBody(2, 30, currentCollectboxAddTestItemID(t))); err != nil {
		t.Fatalf("add without repositories: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("missing repositories must still answer the client")
	}
}

// TestCollectboxAddMissingSourceItemIsAnswered 钉住源物品不存在时也回失败应答
// （客户端不会一直等），且背包不被改动。
func TestCollectboxAddMissingSourceItemIsAnswered(t *testing.T) {
	service, session, repositories, connection := newCurrentCollectboxAddTestService(t, collectboxMutationReference)
	itemID := currentCollectboxAddTestItemID(t)
	seedCurrentCollectboxAddBag(t, repositories, map[string]dnfrepo.ItemStack{})
	connection.write.Reset()
	if err := service.handleCurrentCollectboxAdd(session, currentCollectboxAddTestBody(2, 30, itemID)); err != nil {
		t.Fatalf("add: %v", err)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) != 1 || packets[0].classification != 1 ||
		packets[0].msgID != currentCollectboxAddItemMsgID ||
		!strings.HasPrefix(packets[0].bodyHex, "00") {
		t.Fatalf("packets = %+v want one failure ack", packets)
	}
	if slots := currentCollectboxLoadSlots(t, repositories); len(slots) != 0 {
		t.Fatalf("rejected add must not write anything, slots = %+v", slots)
	}
}
