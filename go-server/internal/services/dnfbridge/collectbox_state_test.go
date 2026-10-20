package dnfbridge

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	dnfenum "longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 本文件钉住「收集箱」的运行时编排：开关极性、初始化顺序、选箱请求口径。
// 现场症状（香港 dnf90.out / packet_log）：
//
//	game-collectbox-deferred conn_id=game-000003 opcode=1021 body_len=1
//	  reason=collectbox_not_implemented
//
// 即操作者开窗时服务端一个字节都没回，客户端于是显示 xui 里的
// STR:test string1/2/3。修复后同一条请求必须换来参考实现那 3 条包。

func currentCollectboxTestCatalog(t *testing.T) currentCollectboxCatalog {
	t.Helper()
	expired, ok := parseCurrentCollectboxDate("2014-12-30 06:00:00")
	if !ok {
		t.Fatal("fixture expired date must parse")
	}
	live, ok := parseCurrentCollectboxDate("2028-12-31 06:00:00")
	if !ok {
		t.Fatal("fixture live date must parse")
	}
	return currentCollectboxCatalog{
		Path: "fixture.etc",
		Boxes: []currentCollectboxBox{
			{
				Index:             1,
				Title:             "SAO收集箱",
				BaseExpiration:    expired,
				HasBaseExpiration: true,
				SlotRows:          1,
				SlotsPerRow:       1,
				Slots:             []currentCollectboxSlot{{ItemID: 10007322, PositionCode: 1}},
			},
			{
				Index:             2,
				Title:             "兽人守护神",
				BaseExpiration:    live,
				HasBaseExpiration: true,
				SlotRows:          3,
				SlotsPerRow:       3,
				Slots: []currentCollectboxSlot{
					{ItemID: 490007143, PositionCode: 3},
					{ItemID: 490007144, PositionCode: 3},
					{ItemID: 490007145, PositionCode: 3},
				},
			},
		},
	}
}

func newCurrentCollectboxTestService(t *testing.T, catalog currentCollectboxCatalog, enabled bool) *Service {
	t.Helper()
	mode := collectboxModeOff
	if enabled {
		mode = collectboxModeReference
	}
	service := &Service{options: options{collectboxMode: mode}}
	if enabled {
		service.collectboxCatalog = catalog
		service.collectboxCatalogLoaded = true
	}
	return service
}

func newCurrentCollectboxTestSession() *gameSession {
	return &gameSession{conn: &bufferConn{}}
}

func currentCollectboxWrittenPackets(t *testing.T, session *gameSession) []dnfprotoPacketView {
	t.Helper()
	stream := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
	packets := make([]dnfprotoPacketView, 0, 4)
	for len(stream) > 0 {
		packet, rest := splitGameServerUpperPacket(t, stream)
		packets = append(packets, dnfprotoPacketView{
			classification: packet.Header.Classification,
			msgID:          packet.Header.MsgID,
			bodyHex:        hex.EncodeToString(packet.Body),
		})
		stream = rest
	}
	return packets
}

type dnfprotoPacketView struct {
	classification byte
	msgID          uint16
	bodyHex        string
}

// TestCollectboxDisabledKeepsLegacyDeferredBehaviour 钉住"一键回退"：
// options 零值（= 不经过 optionsFromConfig 的所有构造路径）= 修复前行为，
// op1021 只记 deferred，一个字节都不发。
func TestCollectboxDisabledKeepsLegacyDeferredBehaviour(t *testing.T) {
	service := newCurrentCollectboxTestService(t, currentCollectboxTestCatalog(t), false)
	session := newCurrentCollectboxTestSession()
	if err := service.handleCurrentCollectboxSelect(session, []byte{0x01}); err != nil {
		t.Fatalf("select with collectbox off: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("collectbox off wrote %d bytes, want 0", session.conn.(*bufferConn).write.Len())
	}
	if err := service.sendCurrentCollectboxInitialization(session, "select_character_after_ack", false); err != nil {
		t.Fatalf("init with collectbox off: %v", err)
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("collectbox off init wrote %d bytes, want 0", session.conn.(*bufferConn).write.Len())
	}
}

// TestCollectboxInitializationSendsVendorWireOrder 钉住厂商 9.16 的顺序与形状：
// class0/箱1..N 缓存 → class1/op1021 `01 <选中箱>` → class0/选中箱刷新，
// 且只有刷新那条的 [1] = 1；默认选中 = 最后一个未过期箱。
func TestCollectboxInitializationSendsVendorWireOrder(t *testing.T) {
	catalog := currentCollectboxTestCatalog(t)
	service := newCurrentCollectboxTestService(t, catalog, true)
	session := newCurrentCollectboxTestSession()
	if err := service.sendCurrentCollectboxInitialization(session, "select_character_after_ack", false); err != nil {
		t.Fatalf("init: %v", err)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) != 4 {
		t.Fatalf("packets=%d want 4 (2 cache + ack + refresh) %+v", len(packets), packets)
	}
	// class0/op1027 箱1（已过期）缓存：逐字节等于厂商那条 `0100000000000000`。
	if packets[0].classification != 0 || packets[0].msgID != currentCollectboxStateMsgID ||
		packets[0].bodyHex != "0100000000000000" {
		t.Fatalf("packet[0]=class%d msg%d body=%s want class0 msg%d body 0100000000000000",
			packets[0].classification, packets[0].msgID, packets[0].bodyHex, currentCollectboxStateMsgID)
	}
	// class1/op1021 选中应答：`01 <选中箱>`。
	if packets[2].classification != 1 || packets[2].msgID != uint16(dnfenum.CmdPacketSelectCollectbox) ||
		packets[2].bodyHex != "0102" {
		t.Fatalf("packet[2]=class%d msg%d body=%s want class1 msg%d body 0102",
			packets[2].classification, packets[2].msgID, packets[2].bodyHex, uint16(dnfenum.CmdPacketSelectCollectbox))
	}
	// 箱2（未过期）：缓存行与刷新行只有 [1]（选中标志）不同，其余逐字节相同。
	cache := decodeCurrentCollectboxStateBody(t, packets[1])
	refresh := decodeCurrentCollectboxStateBody(t, packets[3])
	if packets[1].msgID != currentCollectboxStateMsgID || packets[3].msgID != currentCollectboxStateMsgID {
		t.Fatalf("box2 packets msg ids=%d/%d", packets[1].msgID, packets[3].msgID)
	}
	if cache.box != 2 || refresh.box != 2 {
		t.Fatalf("box2 indexes=%d/%d want 2/2", cache.box, refresh.box)
	}
	if cache.selected || !refresh.selected {
		t.Fatalf("selected flags cache=%v refresh=%v want false/true", cache.selected, refresh.selected)
	}
	if cache.remaining == 0 {
		t.Fatal("live box remaining seconds must be positive")
	}
	if cache.remaining != refresh.remaining {
		t.Fatalf("remaining seconds differ cache=%d refresh=%d", cache.remaining, refresh.remaining)
	}
	if cache.status != 0 || cache.count != 0 || refresh.count != 0 {
		t.Fatalf("status/count cache=%d/%d refresh=%d/%d want 0/0/0/0",
			cache.status, cache.count, refresh.status, refresh.count)
	}
	if packets[1].bodyHex[:2] != "02" || packets[3].bodyHex[:2] != "02" {
		t.Fatalf("box index bytes=%s/%s want 02/02", packets[1].bodyHex[:2], packets[3].bodyHex[:2])
	}
}

type currentCollectboxDecodedState struct {
	box       int
	selected  bool
	remaining uint32
	status    byte
	count     byte
}

func decodeCurrentCollectboxStateBody(t *testing.T, packet dnfprotoPacketView) currentCollectboxDecodedState {
	t.Helper()
	raw, err := hex.DecodeString(packet.bodyHex)
	if err != nil {
		t.Fatalf("decode body %s: %v", packet.bodyHex, err)
	}
	if len(raw) != currentCollectboxStateFixedBodySize {
		t.Fatalf("body_len=%d want %d body=%s", len(raw), currentCollectboxStateFixedBodySize, packet.bodyHex)
	}
	return currentCollectboxDecodedState{
		box:       int(raw[0]),
		selected:  raw[1] == currentCollectboxSelectedFlag,
		remaining: binary.LittleEndian.Uint32(raw[2:6]),
		status:    raw[6],
		count:     raw[7],
	}
}

// TestCollectboxSelectRequestDoesNotAdoptExpiredBox 复刻香港现场那一条请求
// （body_len=1、body=01 → 指向已过期的 SAO 箱 1）：不改变选中箱，仍然回答
// `01 <选中箱>` + 刷新，保证客户端拿到真实箱子文案而不是 xui 占位串。
func TestCollectboxSelectRequestDoesNotAdoptExpiredBox(t *testing.T) {
	catalog := currentCollectboxTestCatalog(t)
	service := newCurrentCollectboxTestService(t, catalog, true)
	session := newCurrentCollectboxTestSession()
	session.collectboxInitialized = true
	session.conn.(*bufferConn).write.Reset()
	if err := service.handleCurrentCollectboxSelect(session, []byte{0x01}); err != nil {
		t.Fatalf("select: %v", err)
	}
	if session.collectboxSelectedIndex != 2 {
		t.Fatalf("selected=%d want 2 (expired box must not be adopted)", session.collectboxSelectedIndex)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) != 2 {
		t.Fatalf("packets=%d want 2 (ack + refresh) %+v", len(packets), packets)
	}
	if packets[0].classification != 1 || packets[0].msgID != currentCollectboxSelectMsgID || packets[0].bodyHex != "0102" {
		t.Fatalf("ack=class%d msg%d body=%s want class1 msg%d body 0102",
			packets[0].classification, packets[0].msgID, packets[0].bodyHex, currentCollectboxSelectMsgID)
	}
	if packets[1].classification != 0 || packets[1].msgID != currentCollectboxStateMsgID || packets[1].bodyHex[2:4] != "01" {
		t.Fatalf("refresh=class%d msg%d body=%s want class0 msg%d selected flag 01",
			packets[1].classification, packets[1].msgID, packets[1].bodyHex, currentCollectboxStateMsgID)
	}
}

// TestCollectboxSelectRequestAdoptsLiveBox 报告"未过期箱号可以被选中"：
// 请求体末字节 = 箱号（参考实现口径）。
func TestCollectboxSelectRequestAdoptsLiveBox(t *testing.T) {
	catalog := currentCollectboxTestCatalog(t)
	service := newCurrentCollectboxTestService(t, catalog, true)
	session := newCurrentCollectboxTestSession()
	session.collectboxInitialized = true
	if err := service.handleCurrentCollectboxSelect(session, []byte{0x02}); err != nil {
		t.Fatalf("select: %v", err)
	}
	if session.collectboxSelectedIndex != 2 {
		t.Fatalf("selected=%d want 2", session.collectboxSelectedIndex)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) != 2 || packets[0].bodyHex != "0102" {
		t.Fatalf("packets=%+v want ack 0102 + refresh", packets)
	}
}

// TestCollectboxOpenRequestWithoutCacheSendsWholeInitialization 保证"开窗早于
// 选角 ack 缓存"时也能拿到全部箱子状态（否则客户端只有当前箱、其余按钮仍空）。
func TestCollectboxOpenRequestWithoutCacheSendsWholeInitialization(t *testing.T) {
	catalog := currentCollectboxTestCatalog(t)
	service := newCurrentCollectboxTestService(t, catalog, true)
	session := newCurrentCollectboxTestSession()
	if err := service.handleCurrentCollectboxSelect(session, []byte{0x01}); err != nil {
		t.Fatalf("select: %v", err)
	}
	packets := currentCollectboxWrittenPackets(t, session)
	if len(packets) != 4 {
		t.Fatalf("packets=%d want 4 (whole cache + ack + refresh) %+v", len(packets), packets)
	}
	if !session.collectboxInitialized {
		t.Fatal("session must be marked initialized after the open-request fallback")
	}
}

// TestCollectboxDefaultIndexPrefersLastLiveBox 钉住默认选中口径
// （厂商参考环境：唯一未过期的箱 5 = selected_box_index=5）。
func TestCollectboxDefaultIndexPrefersLastLiveBox(t *testing.T) {
	catalog := currentCollectboxTestCatalog(t)
	if got := currentCollectboxDefaultIndex(catalog, time.Now()); got != 2 {
		t.Fatalf("default=%d want 2 (last live box)", got)
	}
	allExpired := currentCollectboxCatalog{Boxes: catalog.Boxes[:1]}
	if got := currentCollectboxDefaultIndex(allExpired, time.Now()); got != 1 {
		t.Fatalf("all-expired default=%d want 1 (last box)", got)
	}
	if got := currentCollectboxDefaultIndex(currentCollectboxCatalog{}, time.Now()); got != 0 {
		t.Fatalf("empty catalog default=%d want 0", got)
	}
}

// TestCollectboxModeAndInitNormalization 钉住两个开关的归一化与默认值
// （大小写/别名都要能一键回退，非法值必须落到安全档）。
func TestCollectboxModeAndInitNormalization(t *testing.T) {
	for _, raw := range []string{"reference", "REFERENCE", "on", "1", "true", "yes"} {
		if got := collectboxModeValue(raw); got != collectboxModeReference {
			t.Fatalf("collectboxModeValue(%q)=%q want reference", raw, got)
		}
	}
	for _, raw := range []string{"", "off", "0", "false", "no", "always-nonsense"} {
		if got := collectboxModeValue(raw); got != collectboxModeOff {
			t.Fatalf("collectboxModeValue(%q)=%q want off", raw, got)
		}
	}
	if got := collectboxInitValue("request_only"); got != collectboxInitRequestOnly {
		t.Fatalf("collectboxInitValue=%q want request_only", got)
	}
	for _, raw := range []string{"", "select_ack", "nonsense"} {
		if got := collectboxInitValue(raw); got != collectboxInitSelectAck {
			t.Fatalf("collectboxInitValue(%q)=%q want select_ack", raw, got)
		}
	}
}

// TestCollectboxModuleRoutesSelectOpcode 钉住模块注册：op1021 走真实处理器，
// op1022/1023/1024 只观测（不发明应答）。
func TestCollectboxModuleRoutesSelectOpcode(t *testing.T) {
	registry := mustGameplayModuleRegistry(collectboxGameplayModule())
	catalog := currentCollectboxTestCatalog(t)
	service := newCurrentCollectboxTestService(t, catalog, true)
	session := newCurrentCollectboxTestSession()
	session.collectboxInitialized = true

	handled, err := registry.DispatchLegacy(service, session, currentCollectboxSelectMsgID, []byte{0x02})
	if err != nil || !handled {
		t.Fatalf("dispatch op1021 handled=%v err=%v", handled, err)
	}
	if session.conn.(*bufferConn).write.Len() == 0 {
		t.Fatal("op1021 must produce the select ack + refresh")
	}

	session.conn.(*bufferConn).write.Reset()
	for _, opcode := range []uint16{
		currentCollectboxAddItemMsgID,
		currentCollectboxRemoveItemMsgID,
		currentCollectboxExtendExpiryMsgID,
	} {
		handled, err := registry.DispatchLegacy(service, session, opcode, []byte{0xAA, 0xBB})
		if err != nil || !handled {
			t.Fatalf("dispatch op%d handled=%v err=%v", opcode, handled, err)
		}
	}
	if session.conn.(*bufferConn).write.Len() != 0 {
		t.Fatalf("unverified mutation commands wrote %d bytes, want 0 (不发明应答)",
			session.conn.(*bufferConn).write.Len())
	}
}

// TestCollectboxInitializationLandsRightAfterSelectAck 钉住**注入点**：
// 走真实选角入口 sendUpperCSharpSelectInit 时，收集箱那 5 条包必须紧跟在
// class1/op4 选角 ack 之后、且在 op5 权限快照之前（厂商 9.16 实测顺序）。
// 这条用例同时是"开关关闭时零副作用"的反面：开启后前 5 条包必须逐字节如参考实现。
func TestCollectboxInitializationLandsRightAfterSelectAck(t *testing.T) {
	service, _ := newAccountRecordBootstrapFixture(t)
	session := newAccountRecordBootstrapSession()
	service.options.collectboxMode = collectboxModeReference
	service.collectboxCatalog = currentCollectboxTestCatalog(t)
	service.collectboxCatalogLoaded = true

	if err := service.sendUpperCSharpSelectInit(session, selectFirstSlotRequest()); err != nil {
		t.Fatalf("select character: %v", err)
	}
	stream := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
	selectAck, stream := splitGameServerUpperPacket(t, stream)
	if selectAck.Header.MsgID != uint16(dnfenum.CmdPacketSelectCharacter) ||
		selectAck.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("first packet must stay the select ack: class%d msg%d",
			selectAck.Header.Classification, selectAck.Header.MsgID)
	}
	wantBodies := []string{"0100000000000000", "0200", "0102", "0201"}
	wantClass := []byte{0, 0, 1, 0}
	wantMsg := []uint16{
		currentCollectboxStateMsgID,
		currentCollectboxStateMsgID,
		currentCollectboxSelectMsgID,
		currentCollectboxStateMsgID,
	}
	for idx := range wantBodies {
		packet, rest := splitGameServerUpperPacket(t, stream)
		if packet.Header.Classification != wantClass[idx] || packet.Header.MsgID != wantMsg[idx] {
			t.Fatalf("collectbox packet[%d]=class%d msg%d want class%d msg%d",
				idx, packet.Header.Classification, packet.Header.MsgID, wantClass[idx], wantMsg[idx])
		}
		if !strings.HasPrefix(hex.EncodeToString(packet.Body), wantBodies[idx]) {
			t.Fatalf("collectbox packet[%d] body=%s want prefix %s",
				idx, hex.EncodeToString(packet.Body), wantBodies[idx])
		}
		stream = rest
	}
	// 第 6 条必须已经离开收集箱块（= 收集箱包是连续的一段，且排在 op5 之前）。
	next, _ := splitGameServerUpperPacket(t, stream)
	if next.Header.MsgID == currentCollectboxStateMsgID || next.Header.MsgID == currentCollectboxSelectMsgID {
		t.Fatalf("collectbox block must end before the next bootstrap packet, got class%d msg%d",
			next.Header.Classification, next.Header.MsgID)
	}
}
