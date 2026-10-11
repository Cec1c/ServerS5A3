package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/party"
)

// 本文件是 `DNFBRIDGE_PARTY_PEER_ROW_VENDOR_FIELDS` / `DNFBRIDGE_PARTY_PEER_ROW_ACC`
// 的钉子，共五组：
//
//  1. `vendorPeerRowFields…CapturedBodies…`：厂商 op11 那两条 body 的**逐字节夹具**
//     （`:3337` / `:3340`，常量在 party_vendor_formation_alignment_test.go），
//     这次走的是**线上那条出口** `buildPartyPeerEndpointInfoBody`，而不是手工填好
//     四字段的行构造器 ⇒ 证明"厂商值确实进了线上组装路径"。
//  2. `…Legacy…`：`legacy` 档与默认档**只差那四个字段**，且 legacy 档就是改动前的
//     `acc=成员 char_id / nat=0 / mtu=1500 / char_attr=0`（逐字节回退的钉子）。
//  3. `…AccAuto…`：acc 规律的活路径钉子 —— CS op2 盖章、每会话稳定、同队伍两次
//     构建（formation / 名册后补发 / 进场 remotes）不换号。
//  4. `…Defaults…`：两档取值、非法值回落、零值 Service / nil Service 的默认档。
//  5. `…LiveFormation…`：真正发出去的 op11 帧上的四个字节 + 判读日志串。

// peerRowOffset 给出 body 里第 row 行的起始偏移（body = 1B 员数 + 22B/行）。
func peerRowOffset(t *testing.T, body []byte, row int) int {
	t.Helper()
	offset := 1 + row*currentPartyPeerEndpointSize
	if len(body) < offset+currentPartyPeerEndpointSize {
		t.Fatalf("body % X has no complete row %d (size %d)", body, row, len(body))
	}
	return offset
}

// peerRowAcc 读一行里的 acc（行内偏移 12，LE）。
func peerRowAcc(t *testing.T, body []byte, row int) uint32 {
	t.Helper()
	offset := peerRowOffset(t, body, row)
	return binary.LittleEndian.Uint32(body[offset+12 : offset+16])
}

// peerRowVendorFieldBytes 读一行最后 6 字节 = `u8 nat + u32 mtu(LE) + u8 char_attr`。
func peerRowVendorFieldBytes(t *testing.T, body []byte, row int) []byte {
	t.Helper()
	offset := peerRowOffset(t, body, row)
	return body[offset+16 : offset+22]
}

func vendorPeerRowFieldService(acc uint32) *Service {
	return &Service{options: options{
		partyPeerRowVendorFields: partyPeerRowRowFieldsVendor,
		partyPeerRowAcc:          partyPeerRowAccSetting{pinned: true, value: acc},
	}}
}

// 第 1 组：厂商两条 body **逐字节相等**（含 22B 行本身），走线上出口。
func TestVendorPeerRowFieldsReproduceCapturedBodiesThroughLiveBodyBuilder(t *testing.T) {
	cases := []struct {
		name     string
		hex      string
		uid      uint16
		port     uint16
		acc      uint32
		relayLeg string
	}{
		{
			name:     "conn59_to_char2",
			hex:      vendorOp11BodyToChar2,
			uid:      2,
			port:     30000,
			acc:      28,
			relayLeg: "recipient char_id=1 → peer char_id=2 @ relay port 30000",
		},
		{
			name:     "conn61_to_char1",
			hex:      vendorOp11BodyToChar1,
			uid:      1,
			port:     30001,
			acc:      27,
			relayLeg: "recipient char_id=2 → peer char_id=1 @ relay port 30001",
		},
	}
	for _, tc := range cases {
		service := vendorPeerRowFieldService(tc.acc)
		// 输入刻意用"旧值"（acc = char_id、四字段全零），证明出口把它们全部换掉。
		got := service.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{{
			UserID:    tc.uid,
			IPv4:      [4]byte{192, 168, 0, 101},
			Port:      tc.port,
			AccountID: uint32(tc.uid),
		}})
		want := mustDecodeVendorHex(t, tc.hex)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s (%s):\n got=% X\nwant=% X", tc.name, tc.relayLeg, got, want)
		}
		// 22B 行本身（用户要求的夹具粒度）也逐字节相等。
		if !bytes.Equal(got[1:], want[1:]) {
			t.Fatalf("%s: 22B row bytes = % X want % X", tc.name, got[1:], want[1:])
		}
		if len(got) != 1+currentPartyPeerEndpointSize || got[0] != 1 {
			t.Fatalf("%s: shape len=%d count=%d want len=%d count=1",
				tc.name, len(got), got[0], 1+currentPartyPeerEndpointSize)
		}
		if acc := peerRowAcc(t, got, 0); acc != tc.acc {
			t.Fatalf("%s: acc=%d want %d", tc.name, acc, tc.acc)
		}
		if fields := peerRowVendorFieldBytes(t, got, 0); !bytes.Equal(fields, []byte{0x01, 0x00, 0x02, 0x00, 0x00, 0x64}) {
			t.Fatalf("%s: nat/mtu/char_attr = % X want 01 00020000 64", tc.name, fields)
		}
	}
}

// 第 1 组补充：同样两条 body 用 **auto** 档（不钉死 acc）也能拿到 512/1/0x64，
// 且 acc 非零 —— 这是"默认档进线上就是厂商形状"的最小判据。
func TestVendorPeerRowFieldsDefaultModeCarriesVendorConstants(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	state := runtimePartyStateSnapshot(leader)
	// 零值 options 必须就是默认档（vendor + auto），这里刻意一个开关都不设。
	body := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(body) != 1+currentPartyPeerEndpointSize {
		t.Fatalf("body = % X, want one 22B row", body)
	}
	if fields := peerRowVendorFieldBytes(t, body, 0); !bytes.Equal(fields, []byte{0x01, 0x00, 0x02, 0x00, 0x00, 0x64}) {
		t.Fatalf("default mode nat/mtu/char_attr = % X want 01 00020000 64", fields)
	}
	memberAcc := currentPartyPeerEndpointSnapshot(member).PeerRowAcc
	if memberAcc == 0 {
		t.Fatal("default (auto) mode did not stamp the peer session")
	}
	if acc := peerRowAcc(t, body, 0); acc != memberAcc || acc == 1002 {
		t.Fatalf("default mode acc=%d want the peer session stamp %d (not the legacy char_id 1002)", acc, memberAcc)
	}
	// 退化的"会话已不在线"输入仍必须是确定性回落（不能每次构建都换号）。
	offline := &Service{}
	offlineBody := offline.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{{UserID: 4, Port: 31000, AccountID: 4}})
	if acc := peerRowAcc(t, offlineBody, 0); acc != 4 {
		t.Fatalf("offline member acc=%d want the deterministic legacy fallback 4", acc)
	}
}

// 第 2 组：legacy 档 = 改动前的 `acc=char_id / nat=0 / mtu=1500 / char_attr=0`，
// 且与默认档**只差这四个字段**（其余字节逐字节相同）。
func TestVendorPeerRowFieldsLegacyKeepsCapturedBytesByteForByte(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	state := runtimePartyStateSnapshot(leader)
	service.captureCurrentPartyPeerEndpointRegistration(leader, buildCapturedPeerEndpointRegistrationBody(t, 20612))
	service.captureCurrentPartyPeerEndpointRegistration(member, buildCapturedPeerEndpointRegistrationBody(t, 20613))

	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsVendor
	service.options.partyPeerRowAcc = partyPeerRowAccSetting{}
	vendorBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)

	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsLegacy
	legacyBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)

	if len(vendorBody) != len(legacyBody) {
		t.Fatalf("len vendor=%d legacy=%d", len(vendorBody), len(legacyBody))
	}
	// 四字段所在字节：行内 12..22（acc 4B + nat 1B + mtu 4B + char_attr 1B）→ body 13..23。
	if !bytes.Equal(vendorBody[:13], legacyBody[:13]) || !bytes.Equal(vendorBody[23:], legacyBody[23:]) {
		t.Fatalf("vendor/legacy differ outside the four vendor fields:\n vendor=% X\n legacy=% X", vendorBody, legacyBody)
	}
	if acc := peerRowAcc(t, legacyBody, 0); acc != 1002 {
		t.Fatalf("legacy acc=%d want 1002 (member char_id, 改动前行为)", acc)
	}
	if fields := peerRowVendorFieldBytes(t, legacyBody, 0); !bytes.Equal(fields, []byte{0x00, 0xdc, 0x05, 0x00, 0x00, 0x00}) {
		t.Fatalf("legacy nat/mtu/char_attr = % X want 00 1500(LE) 00", fields)
	}
	// legacy 档下"给行盖字段"这个动作必须是恒等映射（useRows 那条出口才可能逐字节不变）。
	rows := []currentPartyPeerEndpointRow{{userID: 7, accountID: 7}}
	if patched := service.applyPartyPeerRowVendorFields(rows); len(patched) != 1 ||
		patched[0].accountID != 7 || patched[0].nat != 0 || patched[0].mtu != 0 || patched[0].charAttr != 0 {
		t.Fatalf("legacy apply changed the rows: %+v", patched)
	}
	// legacy 档必须忽略 ROW_ACC 的钉死值（回退是无条件的）。
	service.options.partyPeerRowAcc = partyPeerRowAccSetting{pinned: true, value: 28}
	if again := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state); !bytes.Equal(again, legacyBody) {
		t.Fatalf("legacy mode honoured ROW_ACC:\n got=% X\nwant=% X", again, legacyBody)
	}
}

// 第 3 组：acc 规律的活路径钉子。
func TestVendorPeerRowFieldsAccAutoIsStampedPerSessionAndStable(t *testing.T) {
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	state := runtimePartyStateSnapshot(leader)
	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsVendor
	service.options.partyPeerRowAcc = partyPeerRowAccSetting{}

	// CS op2 是"会话开始"信号，也是厂商的 acc 取样时刻：先队长、后队员。
	service.captureCurrentPartyPeerEndpointRegistration(leader, buildCapturedPeerEndpointRegistrationBody(t, 20612))
	service.captureCurrentPartyPeerEndpointRegistration(member, buildCapturedPeerEndpointRegistrationBody(t, 20613))
	leaderAcc := currentPartyPeerEndpointSnapshot(leader).PeerRowAcc
	memberAcc := currentPartyPeerEndpointSnapshot(member).PeerRowAcc
	if leaderAcc == 0 || memberAcc == 0 || leaderAcc == memberAcc {
		t.Fatalf("stamps leader=%d member=%d, want two distinct non-zero ordinals", leaderAcc, memberAcc)
	}

	leaderBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if got := peerRowAcc(t, leaderBody, 0); got != memberAcc {
		t.Fatalf("leader op11 row acc=%d want the peer's stamp %d", got, memberAcc)
	}
	memberBody := service.buildRuntimePartyPeerEndpointInfoForReceiver(member, state)
	if got := peerRowAcc(t, memberBody, 0); got != leaderAcc {
		t.Fatalf("member op11 row acc=%d want the peer's stamp %d", got, leaderAcc)
	}

	// 同一会话重复构建（formation / 名册后补发 / 副本进场 remotes 三条出口）不换号。
	for attempt := 0; attempt < 3; attempt++ {
		if again := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state); !bytes.Equal(again, leaderBody) {
			t.Fatalf("attempt %d changed the leader op11 body:\n got=% X\nwant=% X", attempt, again, leaderBody)
		}
	}
	// 同一会话重复上报 CS op2（同端口 / 换端口）也不换号：盖章是一次性的。
	service.captureCurrentPartyPeerEndpointRegistration(leader, buildCapturedPeerEndpointRegistrationBody(t, 20612))
	service.captureCurrentPartyPeerEndpointRegistration(leader, buildCapturedPeerEndpointRegistrationBody(t, 52216))
	if again := currentPartyPeerEndpointSnapshot(leader).PeerRowAcc; again != leaderAcc {
		t.Fatalf("re-reporting CS op2 re-stamped the session: %d want %d", again, leaderAcc)
	}
	// 全零的 31B 重连探针既不改端口、也不盖章/换号。
	service.captureCurrentPartyPeerEndpointRegistration(&gameSession{connID: "probe", selectedCharacterID: 3}, make([]byte, currentPartyPeerRegistrationBodySize))
	// 会话已不在线（退役快照）时给出确定性回落，绝不每次换号。
	offline := &Service{}
	offlineBody := offline.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{{UserID: 4, Port: 31000, AccountID: 4}})
	if got := peerRowAcc(t, offlineBody, 0); got != 4 {
		t.Fatalf("offline member acc=%d want the deterministic legacy fallback 4", got)
	}
	if again := offline.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{{UserID: 4, Port: 31000, AccountID: 4}}); !bytes.Equal(again, offlineBody) {
		t.Fatalf("offline member acc drifted between builds:\n got=% X\nwant=% X", again, offlineBody)
	}
}

// 第 3 组补充：`ROW_ACC=<u32>` 钉死时**每一行**都是该值（夹具回归就是这么用的）。
func TestVendorPeerRowAccOverridePinsEveryRow(t *testing.T) {
	service := vendorPeerRowFieldService(0x1c)
	body := service.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{
		{UserID: 3, IPv4: [4]byte{38, 22, 90, 52}, Port: 31000, AccountID: 3},
		{UserID: 4, IPv4: [4]byte{38, 22, 90, 52}, Port: 31001, AccountID: 4},
	})
	if body[0] != 2 {
		t.Fatalf("row count = %d want 2", body[0])
	}
	for row := 0; row < 2; row++ {
		if acc := peerRowAcc(t, body, row); acc != 0x1c {
			t.Fatalf("row %d acc=%d want 0x1c", row, acc)
		}
	}
}

// 第 4 组：两档取值、非法值回落、零值 Service / nil Service。
func TestPartyPeerRowVendorFieldsDefaultsAndReadback(t *testing.T) {
	for _, value := range []string{"", " ", "vendor", " VENDOR ", "off", "1", "on"} {
		if got := readPartyPeerRowVendorFieldsMode(value); !got.vendor() || got.String() != "vendor" {
			t.Fatalf("value %q did not fall back to the vendor default: %v", value, got)
		}
	}
	if got := readPartyPeerRowVendorFieldsMode("legacy"); got.vendor() || got.String() != "legacy" {
		t.Fatalf("legacy value not honoured: %v", got)
	}

	accCases := []struct {
		value string
		auto  bool
		want  uint32
		bad   bool
	}{
		{"", true, 0, false},
		{" auto ", true, 0, false},
		{"AUTO", true, 0, false},
		{"28", false, 28, false},
		{" 0x1c ", false, 28, false},
		{"0X1B", false, 27, false},
		{"0", false, 0, false},
		{"abc", true, 0, true},
		{"-1", true, 0, true},
		{"4294967296", true, 0, true},
	}
	for _, tc := range accCases {
		got := readPartyPeerRowAccSetting(tc.value)
		if got.auto() != tc.auto || got.value != tc.want || got.bad != tc.bad {
			t.Fatalf("ROW_ACC %q → auto=%t value=%d bad=%t, want auto=%t value=%d bad=%t",
				tc.value, got.auto(), got.value, got.bad, tc.auto, tc.want, tc.bad)
		}
	}
	if got := readPartyPeerRowAccSetting("").String(); got != "auto" {
		t.Fatalf("auto String() = %q want auto", got)
	}
	if got := readPartyPeerRowAccSetting("28").String(); got != "28" {
		t.Fatalf("pinned String() = %q want 28", got)
	}
	// 零值结构体（= 没设 env 的 Service）必须是 auto，不能变成"钉死 0"。
	if zeroAcc := (partyPeerRowAccSetting{}); !zeroAcc.auto() || zeroAcc.String() != "auto" {
		t.Fatalf("zero-value ROW_ACC setting is not auto: %+v", zeroAcc)
	}

	// 零值 options（探针/测试路径）与 nil Service 都必须是厂商档 + auto。
	zero := &Service{}
	if !zero.partyPeerRowVendorFields().vendor() || !zero.partyPeerRowAcc().auto() {
		t.Fatalf("zero-value Service is not on the vendor/auto default")
	}
	var nilService *Service
	if !nilService.partyPeerRowVendorFields().vendor() || !nilService.partyPeerRowAcc().auto() {
		t.Fatalf("nil Service is not on the vendor/auto default")
	}
}

// 第 4 组补充：发号器本身 —— **1 基**、单调、溢出兜底不发 0。
func TestPartyPeerRowAccSequenceIsOneBasedAndMonotonic(t *testing.T) {
	resetPartyPeerRowAccSeqForTest(0)
	first := nextPartyPeerRowAcc()
	if first != 1 {
		t.Fatalf("first ordinal = %d want 1 (0 是「未盖章」的形状，绝不能发)", first)
	}
	second := nextPartyPeerRowAcc()
	if second != first+1 {
		t.Fatalf("second ordinal = %d want %d", second, first+1)
	}
	resetPartyPeerRowAccSeqForTest(^uint32(0))
	if got := nextPartyPeerRowAcc(); got != 1 {
		t.Fatalf("overflow fallback = %d want 1", got)
	}
	resetPartyPeerRowAccSeqForTest(second)
}

// 第 5 组：真正发出去的 op11 帧上的四个字段 + 判读日志串。
func TestVendorPeerRowFieldsOnLiveFormationFrame(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsVendor
	service.options.partyPeerRowAcc = partyPeerRowAccSetting{} // auto（= 线上默认）
	service.captureCurrentPartyPeerEndpointRegistration(leader, buildCapturedPeerEndpointRegistrationBody(t, 20612))
	service.captureCurrentPartyPeerEndpointRegistration(member, buildCapturedPeerEndpointRegistrationBody(t, 20613))
	memberAcc := currentPartyPeerEndpointSnapshot(member).PeerRowAcc

	state := runtimePartyStateSnapshot(leader)
	writeFrames(t, leader)
	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	bodies := peerEndpointFrameBodies(writeFrames(t, leader))
	if len(bodies) != 1 {
		t.Fatalf("op11 frames = %d want 1", len(bodies))
	}
	body := bodies[0]
	if len(body) != 1+currentPartyPeerEndpointSize || body[0] != 1 {
		t.Fatalf("op11 body = % X, want count=1 + one 22B peer row", body)
	}
	if uid := uint16(body[1]) | uint16(body[2])<<8; uid != 1002 {
		t.Fatalf("live op11 row uid=%d want the peer 1002", uid)
	}
	if fields := peerRowVendorFieldBytes(t, body, 0); !bytes.Equal(fields, []byte{0x01, 0x00, 0x02, 0x00, 0x00, 0x64}) {
		t.Fatalf("live op11 nat/mtu/char_attr = % X want 01 00020000 64", fields)
	}
	if acc := peerRowAcc(t, body, 0); acc != memberAcc {
		t.Fatalf("live op11 acc=%d want the peer session stamp %d", acc, memberAcc)
	}

	logged := readPeerEndpointTestLog(t, logger, logPath)
	for _, want := range []string{
		"game-party-peer-endpoint-registered",
		"peer_row_vendor_fields=vendor",
		"peer_row_acc_stamped=true",
		"game-party-peer-row-acc-stamped",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("packet log is missing %q:\n%s", want, logged)
		}
	}
}

// 第 5 组补充：**中继形态**（生产路径 `vendorRelayFixture` 的真中继房）下，厂商四字段必须
// 与中继端点共存：IP/端口仍是该腿的中继值（本轮一个字节没动），四字段是厂商值。
func TestVendorPeerRowFieldsOnRelayFormationShape(t *testing.T) {
	service, leader, member := vendorRelayFixture(t)
	state := runtimePartyStateSnapshot(leader)
	relay := service.currentPartyUDPRelay()
	address, port, ok := relay.Endpoint(uint16(state.PartyID), 1001, 1002)
	if !ok {
		t.Fatal("relay leg 1001->1002 missing")
	}
	service.captureCurrentPartyPeerEndpointRegistration(member, buildCapturedPeerEndpointRegistrationBody(t, 20613))
	memberAcc := currentPartyPeerEndpointSnapshot(member).PeerRowAcc
	if memberAcc == 0 {
		t.Fatal("CS op2 did not stamp the member session")
	}

	body := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if len(body) != 1+currentPartyPeerEndpointSize || body[0] != 1 {
		t.Fatalf("relay op11 body = % X, want count=1 (peer only)", body)
	}
	if uid := uint16(body[1]) | uint16(body[2])<<8; uid != 1002 {
		t.Fatalf("relay op11 row uid=%d want the peer 1002", uid)
	}
	if !bytes.Equal(body[3:7], address[:]) || !bytes.Equal(body[7:11], address[:]) {
		t.Fatalf("relay op11 inner/outer = % X/% X want relay ip % X", body[3:7], body[7:11], address)
	}
	if got := uint16(body[11])<<8 | uint16(body[12]); got != port {
		t.Fatalf("relay op11 port = %d want %d (network order)", got, port)
	}
	if fields := peerRowVendorFieldBytes(t, body, 0); !bytes.Equal(fields, []byte{0x01, 0x00, 0x02, 0x00, 0x00, 0x64}) {
		t.Fatalf("relay op11 nat/mtu/char_attr = % X want 01 00020000 64", fields)
	}
	if acc := peerRowAcc(t, body, 0); acc != memberAcc {
		t.Fatalf("relay op11 acc=%d want the peer session stamp %d", acc, memberAcc)
	}

	// legacy 档同一份输入：中继端点与其余字节不变，只有四字段回到 char_id/0/1500/0。
	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsLegacy
	legacy := service.buildRuntimePartyPeerEndpointInfoForReceiver(leader, state)
	if !bytes.Equal(legacy[:13], body[:13]) || !bytes.Equal(legacy[23:], body[23:]) {
		t.Fatalf("relay legacy body differs outside the four vendor fields:\n legacy=% X\nvendor=% X", legacy, body)
	}
	if acc := peerRowAcc(t, legacy, 0); acc != 1002 {
		t.Fatalf("relay legacy acc=%d want 1002 (member char_id)", acc)
	}
	if fields := peerRowVendorFieldBytes(t, legacy, 0); !bytes.Equal(fields, []byte{0x00, 0xdc, 0x05, 0x00, 0x00, 0x00}) {
		t.Fatalf("relay legacy nat/mtu/char_attr = % X want 00 1500(LE) 00", fields)
	}
}

// 第 5 组补充：legacy 档在**活路径**上必须回到改动前的字节（同一场 formation 的 op11
// 帧上 acc=对端 char_id、nat=0、mtu=1500、char_attr=0）。
func TestVendorPeerRowFieldsLegacyOnLiveFormationFrame(t *testing.T) {
	logger, logPath := newPeerEndpointTestLogger(t)
	service, leader, member, _, _ := changeHostFixture(t, 10, 10)
	service.packetLog = logger
	service.options.partyPeerRowVendorFields = partyPeerRowRowFieldsLegacy
	service.captureCurrentPartyPeerEndpointRegistration(leader, buildCapturedPeerEndpointRegistrationBody(t, 20612))
	service.captureCurrentPartyPeerEndpointRegistration(member, buildCapturedPeerEndpointRegistrationBody(t, 20613))

	state := runtimePartyStateSnapshot(leader)
	writeFrames(t, leader)
	if err := service.sendRuntimePartySnapshot(leader, state); err != nil {
		t.Fatal(err)
	}
	bodies := peerEndpointFrameBodies(writeFrames(t, leader))
	if len(bodies) != 1 {
		t.Fatalf("op11 frames = %d want 1", len(bodies))
	}
	body := bodies[0]
	if acc := peerRowAcc(t, body, 0); acc != 1002 {
		t.Fatalf("legacy live acc=%d want 1002 (member char_id)", acc)
	}
	if fields := peerRowVendorFieldBytes(t, body, 0); !bytes.Equal(fields, []byte{0x00, 0xdc, 0x05, 0x00, 0x00, 0x00}) {
		t.Fatalf("legacy live nat/mtu/char_attr = % X want 00 1500(LE) 00", fields)
	}
	// legacy 档不得盖章（连事件都不应该出现）。
	logged := readPeerEndpointTestLog(t, logger, logPath)
	if strings.Contains(logged, "game-party-peer-row-acc-stamped") {
		t.Fatalf("legacy mode stamped an acc:\n%s", logged)
	}
	if !strings.Contains(logged, "peer_row_vendor_fields=legacy") {
		t.Fatalf("legacy mode did not record its档位:\n%s", logged)
	}
}
