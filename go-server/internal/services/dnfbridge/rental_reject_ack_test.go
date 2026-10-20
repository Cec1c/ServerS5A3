package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 操作者清单第 10 条（无法租赁武器）修复的聚焦用例。
//
// 厂商依据：
//   - `Network\Builders\RentalWeaponPacketBuilder.cs:11-23`（`BuildSuccessAck` /
//     `BuildResultAck` / `BuildFailureAck`）
//   - `Network\Handlers\RentalHandler.cs:49/58/108`（每条分支都回包）
//   - `Network\Handlers\LuckyStarHandler.cs:125-131`（充值失败 1 字节 `00`）
//   - `Network\Handlers\LuckyStarClientNotifier.cs:44-68`（充值成功 13 字节，
//     `mode`/`quantity` 回显请求）

func TestCurrentRentalRejectAckModeNormalization(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"", currentRentalRejectAckMissing},
		{"   ", currentRentalRejectAckMissing},
		{"missing", currentRentalRejectAckMissing},
		{"MISSING", currentRentalRejectAckMissing},
		{"vendor", currentRentalRejectAckVendor},
		{" Vendor_Shapes ", currentRentalRejectAckVendor},
		{"aligned", currentRentalRejectAckVendor},
		{"off", currentRentalRejectAckOff},
		{"0", currentRentalRejectAckOff},
		{"false", currentRentalRejectAckOff},
		{"no", currentRentalRejectAckOff},
		{"legacy", currentRentalRejectAckOff},
		{"disable", currentRentalRejectAckOff},
		{"disabled", currentRentalRejectAckOff},
		// 非法值不放大行为，一律回落默认档（与既有开关约定一致）。
		{"on", currentRentalRejectAckMissing},
		{"1", currentRentalRejectAckMissing},
		{"true", currentRentalRejectAckMissing},
		{"yes", currentRentalRejectAckMissing},
		{"nonsense", currentRentalRejectAckMissing},
	}
	for _, testCase := range cases {
		t.Setenv(currentRentalRejectAckEnv, testCase.value)
		if got := currentRentalRejectAckMode(); got != testCase.want {
			t.Fatalf("%s=%q mode=%q want %q", currentRentalRejectAckEnv, testCase.value, got, testCase.want)
		}
	}
	t.Setenv(currentRentalRejectAckEnv, "off")
	if currentRentalRejectAckFillsMissing(currentRentalRejectAckMode()) {
		t.Fatal("off must not fill the missing reject branches")
	}
	if currentRentalRejectAckVendorShapes(currentRentalRejectAckMode()) {
		t.Fatal("off must not use vendor shapes")
	}
	t.Setenv(currentRentalRejectAckEnv, "vendor")
	if !currentRentalRejectAckFillsMissing(currentRentalRejectAckMode()) {
		t.Fatal("vendor must still fill the missing reject branches")
	}
	if !currentRentalRejectAckVendorShapes(currentRentalRejectAckMode()) {
		t.Fatal("vendor must use vendor shapes")
	}
	t.Setenv(currentRentalRejectAckEnv, currentRentalRejectAckMissing)
	if !currentRentalRejectAckFillsMissing(currentRentalRejectAckMode()) {
		t.Fatal("default must fill the missing reject branches")
	}
	if currentRentalRejectAckVendorShapes(currentRentalRejectAckMode()) {
		t.Fatal("default must NOT change already-replying branches")
	}
}

// 逐字节钉住厂商两个 builder 的形状。
func TestCurrentRentalAckBodiesMatchVendorBuilders(t *testing.T) {
	if got := buildCurrentRentalFailureAckBody(); !bytes.Equal(got, []byte{0x00}) {
		t.Fatalf("BuildFailureAck = %x want 00", got)
	}
	if got := buildCurrentRentalResultAckBody(0); !bytes.Equal(got, []byte{0x01, 0x00, 0x00, 0x00, 0x00}) {
		t.Fatalf("BuildResultAck(0) = %x want 0100000000", got)
	}
	if got := buildCurrentRentalResultAckBody(currentRentalVendorInventoryFullResult); !bytes.Equal(got, []byte{0x01, 0x02, 0x00, 0x00, 0x00}) {
		t.Fatalf("BuildResultAck(2) = %x want 0102000000", got)
	}
	if got := buildCurrentRentalResultAckBody(currentRentalVendorTooManyActiveItemsResult); !bytes.Equal(got, []byte{0x01, 0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("BuildResultAck(1) = %x want 0101000000", got)
	}
}

// 充值成功体：默认档必须与改动前的字面量逐字节相同；vendor 档回显请求 quantity。
// 默认档的字面量取自线上真实报文（packet_log.20260925-055809.txt，
// `SEND msg_id=1000 plain_body_len=13 ... 01 01000000 00000000 2a000000`，请求 count=31）。
func TestCurrentRentalChargeSuccessBodyDefaultIsLegacyAndVendorEchoesQuantity(t *testing.T) {
	legacy := buildCurrentRentalChargeSuccessBody(1, 31, 42, false)
	if !bytes.Equal(legacy, []byte{
		0x01, 0x00, 0x00, 0x00, // mode / charge type
		0x00, 0x00, 0x00, 0x00, // 改动前：恒 0
		0x2a, 0x00, 0x00, 0x00, // total lucky star
	}) {
		t.Fatalf("legacy charge ack body = %x", legacy)
	}
	if got := append([]byte{0x01}, legacy...); !bytes.Equal(got, []byte{
		0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x2a, 0x00, 0x00, 0x00,
	}) {
		t.Fatalf("legacy charge ack wire body = %x", got)
	}
	vendor := buildCurrentRentalChargeSuccessBody(1, 31, 42, true)
	if !bytes.Equal(vendor, []byte{
		0x01, 0x00, 0x00, 0x00,
		0x1f, 0x00, 0x00, 0x00, // 回显请求 quantity = 31
		0x2a, 0x00, 0x00, 0x00,
	}) {
		t.Fatalf("vendor charge ack body = %x", vendor)
	}
	if binary.LittleEndian.Uint32(vendor[4:8]) != 31 || binary.LittleEndian.Uint32(vendor[8:12]) != 42 {
		t.Fatalf("vendor charge ack fields = %x", vendor)
	}
}

// op999 成功应答（confirm 路径）在默认档必须逐字节不变：`01 <u32 0>`（5 字节）。
func TestCurrentRentEquipmentSuccessAckStaysByteIdentical(t *testing.T) {
	t.Setenv(currentRentalRejectAckEnv, "")
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection}
	if err := service.sendCurrentRentEquipmentResult(session, 0); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketRentEquipmentItem) {
		t.Fatalf("success ack class=%d msg=%d", packet.Header.Classification, packet.Header.MsgID)
	}
	if !bytes.Equal(packet.Body, []byte{0x01, 0x00, 0x00, 0x00, 0x00}) {
		t.Fatalf("success ack body = %x", packet.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("success ack emitted an extra packet len=%d", len(rest))
	}
	// 背包满（cancel 路径的一种）同样是厂商口径 `01 <u32 2>`，默认档不变。
	connection = &bufferConn{}
	session = &gameSession{conn: connection}
	if err := service.sendCurrentRentEquipmentResult(session, currentRentalVendorInventoryFullResult); err != nil {
		t.Fatal(err)
	}
	packet, _ = splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(packet.Body, []byte{0x01, 0x02, 0x00, 0x00, 0x00}) {
		t.Fatalf("inventory-full ack body = %x", packet.Body)
	}
}

// op999 事务前拒绝（解析失败）：改动前**一个字节都不回**；默认档补厂商的 1 字节 `00`。
func TestHandleCurrentRentEquipmentMalformedEmitsVendorFailureAckByDefault(t *testing.T) {
	t.Setenv(currentRentalRejectAckEnv, "")
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	if err := service.handleCurrentRentEquipment(session, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketRentEquipmentItem) {
		t.Fatalf("reject ack class=%d msg=%d", packet.Header.Classification, packet.Header.MsgID)
	}
	if !bytes.Equal(packet.Body, []byte{0x00}) {
		t.Fatalf("reject ack body = %x want 00", packet.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("reject ack emitted an extra packet len=%d", len(rest))
	}

	// 同样的路径在 vendor 档形状一致（事务前拒绝两种档都补 `00`）。
	t.Setenv(currentRentalRejectAckEnv, "vendor")
	connection = &bufferConn{}
	session = &gameSession{conn: connection, selectedCharacterID: 19}
	if err := service.handleCurrentRentEquipment(session, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	packet, _ = splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(packet.Body, []byte{0x00}) {
		t.Fatalf("vendor-mode reject ack body = %x want 00", packet.Body)
	}
}

// 货币不足 / 背包满两个事务拒绝分支的判据（对照厂商 `RentalHandler.cs:74-108`）。
//
//	货币不足（resultCode 1）: vendor 档 → 1 字节 `00`；默认档与 off 档 → 保留今天 `01 01000000`。
//	背包满  （resultCode 2）: 三档都一样 → `01 02000000`（本来就是厂商口径）。
func TestCurrentRentalTransactionRejectBranches(t *testing.T) {
	insufficientPoints := uint32(1)
	inventoryFull := uint32(currentRentalVendorInventoryFullResult)

	if !currentRentalTransactionRejectUsesVendorFailureAck(currentRentalRejectAckVendor, insufficientPoints) {
		t.Fatal("vendor mode must answer insufficient lucky stars with the 1-byte failure ack")
	}
	if currentRentalTransactionRejectUsesVendorFailureAck(currentRentalRejectAckVendor, inventoryFull) {
		t.Fatal("vendor mode must keep the inventory-full result ack")
	}
	for _, mode := range []string{currentRentalRejectAckMissing, currentRentalRejectAckOff} {
		if currentRentalTransactionRejectUsesVendorFailureAck(mode, insufficientPoints) {
			t.Fatalf("%s must keep today's 5-byte result ack for insufficient lucky stars", mode)
		}
		if currentRentalTransactionRejectUsesVendorFailureAck(mode, inventoryFull) {
			t.Fatalf("%s must keep today's 5-byte inventory-full result ack", mode)
		}
	}

	// 事务拒绝的两种体在默认档与 vendor 档下的逐字节形状。
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection}
	if err := service.sendCurrentRentEquipmentResult(session, insufficientPoints); err != nil {
		t.Fatal(err)
	}
	packet, _ := splitGameServerUpperPacket(t, connection.write.Bytes())
	if !bytes.Equal(packet.Body, []byte{0x01, 0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("default insufficient-points ack = %x want 0101000000", packet.Body)
	}
	if got := buildCurrentRentalFailureAckBody(); !bytes.Equal(got, []byte{0x00}) {
		t.Fatalf("vendor insufficient-points ack = %x want 00", got)
	}
}

// 一键回退：`off` 档在同样的路径上写出 0 字节，逐字节回到改动前。
func TestHandleCurrentRentEquipmentMalformedOffWritesNothing(t *testing.T) {
	t.Setenv(currentRentalRejectAckEnv, "off")
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	if err := service.handleCurrentRentEquipment(session, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("off mode wrote %d bytes: %x", connection.write.Len(), connection.write.Bytes())
	}
}

// op1000 事务前拒绝（解析失败 = 客户端 21 字节请求里 chargeType != 1）：
// 厂商 `LuckyStarHandler.cs:40` 回 1 字节 `00`；默认档补上，`off` 档写 0 字节。
func TestHandleCurrentChargeRentalPointMalformedRejectAck(t *testing.T) {
	body := make([]byte, currentRentalRequestWireSize)
	binary.LittleEndian.PutUint32(body[13:17], 2) // chargeType != 1
	binary.LittleEndian.PutUint32(body[17:21], 10)

	t.Setenv(currentRentalRejectAckEnv, "")
	connection := &bufferConn{}
	service := Service{}
	session := &gameSession{conn: connection, selectedCharacterID: 19}
	if err := service.handleCurrentChargeRentalPoint(session, body); err != nil {
		t.Fatal(err)
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if packet.Header.Classification != 1 || packet.Header.MsgID != uint16(dnfenum.CmdPacketChargeRentpoint) {
		t.Fatalf("charge reject ack class=%d msg=%d", packet.Header.Classification, packet.Header.MsgID)
	}
	if !bytes.Equal(packet.Body, []byte{0x00}) {
		t.Fatalf("charge reject ack body = %x want 00", packet.Body)
	}
	if len(rest) != 0 {
		t.Fatalf("charge reject ack emitted an extra packet len=%d", len(rest))
	}

	t.Setenv(currentRentalRejectAckEnv, "off")
	connection = &bufferConn{}
	session = &gameSession{conn: connection, selectedCharacterID: 19}
	if err := service.handleCurrentChargeRentalPoint(session, body); err != nil {
		t.Fatal(err)
	}
	if connection.write.Len() != 0 {
		t.Fatalf("off mode wrote %d bytes: %x", connection.write.Len(), connection.write.Bytes())
	}
}
