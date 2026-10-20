package dnfbridge

import (
	"bytes"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfproto "longheng.io/server/internal/modules/dnf/protocol"
)

// 强化直播（576 家族）的回归钉子：注册了、有分类、有具名日志、并且**默认档一个字节
// 都不发**（与 86JP/A21/厂商 9.16 逐字节同形）；只有显式开
// DNFBRIDGE_UPGRADE_ROOM_REPLY=echo_failure 才回那条形状未证实的受控失败 ACK。
//
// 现场报文（packet_log.txt:2202-2205，2026-09-25 19:40:06.761 / 19:40:08.879）：
//
//	RECV kind=game-legacy cmd=1 type=576 runtime_cmd_name=ENUM_CMDPACKET_CREATE_UPGRADE_ROOM
//	     body_len=4 hex=01400211000000d234ded26700790bb000
//	     body_len=4 hex=01400211000000a4b9c1a36900790bb000
//
// 去掉 11 字节包头后是 `u16 seq(=header_seq) + payload`，payload 两次都是 `79 0b`。
var currentUpgradeRoomCreateRoomBody = []byte{0x67, 0x00, 0x79, 0x0b}

func TestUpgradeRoomFamilyIsRegisteredAsBlockedMigration(t *testing.T) {
	for _, opcode := range upgradeRoomFamilyCommands {
		if _, ok := dnfenum.LookupAlignedCommand(uint16(opcode)); ok {
			t.Fatalf("opcode %d must not be treated as a reusable aligned command", opcode)
		}
		blocked, ok := dnfenum.LookupBlockedMigration(uint16(opcode))
		if !ok {
			t.Fatalf("opcode %d is missing from the blocked-migration registry", opcode)
		}
		if blocked.Domain != dnfenum.AlignedDomainPackage {
			t.Fatalf("opcode %d domain = %q, want %q", opcode, blocked.Domain, dnfenum.AlignedDomainPackage)
		}
	}
	if !isUpgradeRoomCommand(uint16(dnfenum.CmdPacketCreateUpgradeRoom)) {
		t.Fatal("type 576（强化直播建房）must be recognized by isUpgradeRoomCommand")
	}
	if isUpgradeRoomCommand(uint16(dnfenum.CmdPacketMoveItemspace)) {
		t.Fatal("op19 must not be classified as the upgrade-room family")
	}
}

func TestUpgradeRoomCreateRoomFollowsReferenceAndSendsNothingByDefault(t *testing.T) {
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			upgradeRoomReplyMode: normalizeUpgradeRoomReplyMode(
				upgradeRoomReplyReferenceDrop,
			),
		},
	}
	session := &gameSession{
		conn:                connection,
		connID:              "upgrade-room-reference-drop",
		channel:             channelcatalog.Channel{ID: 253},
		selectedCharacterID: 3,
	}
	handled, err := service.handleUpgradeRoomCommand(
		session,
		uint16(dnfenum.CmdPacketCreateUpgradeRoom),
		append([]byte(nil), currentUpgradeRoomCreateRoomBody...),
	)
	if err != nil {
		t.Fatalf("handleUpgradeRoomCommand error = %v", err)
	}
	if !handled {
		t.Fatal("type 576 must be handled by the registered upgrade-room owner")
	}
	if got := connection.write.Bytes(); len(got) != 0 {
		t.Fatalf("reference drop must not write a packet, wrote %x", got)
	}
}

func TestUpgradeRoomEchoFailureIsOptInAndBounded(t *testing.T) {
	connection := &bufferConn{}
	service := &Service{
		options: options{
			accountID:          "dnf:1",
			gameUpperHeader:    gameUpperHeaderServer16,
			gameUpperBodyCodec: gameUpperBodyCodecPlain,
			upgradeRoomReplyMode: normalizeUpgradeRoomReplyMode(
				upgradeRoomReplyEchoFailure,
			),
		},
	}
	session := &gameSession{
		conn:                connection,
		connID:              "upgrade-room-echo-failure",
		channel:             channelcatalog.Channel{ID: 253},
		selectedCharacterID: 3,
	}
	handled, err := service.handleUpgradeRoomCommand(
		session,
		uint16(dnfenum.CmdPacketCreateUpgradeRoom),
		append([]byte(nil), currentUpgradeRoomCreateRoomBody...),
	)
	if err != nil {
		t.Fatalf("handleUpgradeRoomCommand error = %v", err)
	}
	if !handled {
		t.Fatal("type 576 must be handled")
	}
	packet, rest := splitGameServerUpperPacket(t, connection.write.Bytes())
	if len(rest) != 0 {
		t.Fatalf("echo failure must write exactly one packet, trailing=%x", rest)
	}
	if packet.Header.MsgID != uint16(dnfenum.CmdPacketCreateUpgradeRoom) ||
		packet.Header.Classification != dnfproto.DefaultChannelClassification {
		t.Fatalf("echo failure header=%+v", packet.Header)
	}
	const codecPrefix = 3
	if len(packet.Body) != codecPrefix+1 || !bytes.Equal(packet.Body[:codecPrefix], []byte{0, 0, 0}) ||
		packet.Body[codecPrefix] != currentUpgradeRoomFailureCode {
		t.Fatalf("echo failure body=%x, want codec prefix + 0x%02X", packet.Body, currentUpgradeRoomFailureCode)
	}
}

func TestUpgradeRoomReplyModeNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: upgradeRoomReplyReferenceDrop},
		{value: "reference_drop", want: upgradeRoomReplyReferenceDrop},
		{value: "off", want: upgradeRoomReplyReferenceDrop},
		{value: "echo_failure", want: upgradeRoomReplyEchoFailure},
		{value: "ACK", want: upgradeRoomReplyEchoFailure},
	} {
		if got := normalizeUpgradeRoomReplyMode(test.value); got != test.want {
			t.Fatalf("normalizeUpgradeRoomReplyMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestEquipRefreshSwitchNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: equipMoveContainerRefreshSuppressed},
		{value: "suppressed", want: equipMoveContainerRefreshSuppressed},
		{value: "full", want: equipMoveContainerRefreshFull},
		{value: "legacy", want: equipMoveContainerRefreshFull},
	} {
		if got := normalizeEquipMoveContainerRefreshMode(test.value); got != test.want {
			t.Fatalf("normalizeEquipMoveContainerRefreshMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: equipAppearanceRefreshMode0},
		{value: "mode0", want: equipAppearanceRefreshMode0},
		{value: "legacy", want: equipAppearanceRefreshMode0},
		{value: "scene_object", want: equipAppearanceRefreshMode0},
		{value: "mode1", want: equipAppearanceRefreshMode1},
		{value: "userinfo", want: equipAppearanceRefreshMode1},
		{value: "mode_1", want: equipAppearanceRefreshMode1},
	} {
		if got := normalizeEquipAppearanceRefreshMode(test.value); got != test.want {
			t.Fatalf("normalizeEquipAppearanceRefreshMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

// TestEquipAppearanceSlotGateSwitchNormalization 钉住外观相关性闸门开关的 env 解析：
// 默认/空/未知 = on（修复档，只在真正改变 mode0 纸娃娃外观的穿脱上补外观刷新），
// 只有显式 off（及别名）才回退到"每次穿脱都无条件发"。options 零值必须保持修复档。
func TestEquipAppearanceSlotGateSwitchNormalization(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: equipAppearanceSlotGateOn},
		{value: "on", want: equipAppearanceSlotGateOn},
		{value: "1", want: equipAppearanceSlotGateOn},
		{value: "true", want: equipAppearanceSlotGateOn},
		{value: "garbage", want: equipAppearanceSlotGateOn},
		{value: "off", want: equipAppearanceSlotGateOff},
		{value: "OFF", want: equipAppearanceSlotGateOff},
		{value: "0", want: equipAppearanceSlotGateOff},
		{value: "false", want: equipAppearanceSlotGateOff},
		{value: "legacy", want: equipAppearanceSlotGateOff},
		{value: "disabled", want: equipAppearanceSlotGateOff},
	} {
		if got := normalizeEquipAppearanceSlotGateMode(test.value); got != test.want {
			t.Fatalf("normalizeEquipAppearanceSlotGateMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}

	zero := &Service{}
	if zero.equipAppearanceSlotGateOffRequested() {
		t.Fatal("zero-value Service must keep the appearance-slot gate on (fixed)")
	}
	if got := zero.equipAppearanceSlotGateMode(); got != equipAppearanceSlotGateOn {
		t.Fatalf("zero-value gate mode = %q, want %q", got, equipAppearanceSlotGateOn)
	}
	rolled := &Service{options: options{equipAppearanceSlotGateOff: true}}
	if !rolled.equipAppearanceSlotGateOffRequested() {
		t.Fatal("equipAppearanceSlotGateOff=true must report the gate as off")
	}
	if got := rolled.equipAppearanceSlotGateMode(); got != equipAppearanceSlotGateOff {
		t.Fatalf("rolled gate mode = %q, want %q", got, equipAppearanceSlotGateOff)
	}
}
