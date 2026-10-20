package dnfbridge

import (
	"encoding/hex"
	"testing"
)

// vendorRaidDailyAvailabilityHex 是厂商 `game-000059` 会话里 op1303 的完整包
// （len=31：16 字节包头 + 15 字节包体），黄金基准。
const vendorRaidDailyAvailabilityHex = "0017051f000000e585faf7360000000001000a000002000a000003000a0000"

// TestRaidDailyAvailabilityBodyMatchesVendorPacket 钉住 op1303 包体与厂商字节
// 完全一致（三条 5 字节行：mode/used/limit/field4/field5）。
func TestRaidDailyAvailabilityBodyMatchesVendorPacket(t *testing.T) {
	raw, err := hex.DecodeString(vendorRaidDailyAvailabilityHex)
	if err != nil {
		t.Fatalf("vendor hex decode: %v", err)
	}
	if len(raw) != 31 {
		t.Fatalf("vendor packet len=%d, expected 31", len(raw))
	}
	vendorBody := raw[16:]
	body := buildCurrentRaidDailyAvailabilityBody(
		[]int{currentRaidDailyAntonMode, currentRaidDailyLukeMode, currentRaidDailyFiendwarMode},
		currentRaidDailyDefaultUsed,
		currentRaidDailyDefaultLimit,
	)
	if len(body) != 15 {
		t.Fatalf("body len=%d, expected 15", len(body))
	}
	if hex.EncodeToString(body) != hex.EncodeToString(vendorBody) {
		t.Fatalf("body mismatch:\n ours=%s\nvendor=%s", hex.EncodeToString(body), hex.EncodeToString(vendorBody))
	}
	if currentRaidDailyAvailabilityN != 3 {
		t.Fatalf("row count=%d, expected 3", currentRaidDailyAvailabilityN)
	}
}

// The real official-24.9 reader consumes 3*4 bytes. A switch must never
// enable the incompatible 3*5 vendor fixture for that profile.
func TestOfficialRaidDailyAvailabilityDoesNotSendLegacyRows(t *testing.T) {
	for _, setting := range []string{"", "on", "off"} {
		t.Run("switch="+setting, func(t *testing.T) {
			t.Setenv(currentRaidDailyAvailabilityEnv, setting)
			conn := &bufferConn{}
			s := &Service{options: options{currentClientVersion: "official-24.9"}}
			if err := s.sendCurrentRaidDailyAvailability(&gameSession{conn: conn, selectedCharacterID: 19}, "test"); err != nil {
				t.Fatal(err)
			}
			if conn.write.Len() != 0 {
				t.Fatalf("incompatible 1303 was emitted: %x", conn.write.Bytes())
			}
		})
	}
}

func TestLegacyRaidDailyAvailabilityPreservesVendorBody(t *testing.T) {
	t.Setenv(currentRaidDailyAvailabilityEnv, "on")
	conn := &bufferConn{}
	s := &Service{options: options{currentClientVersion: "90cn", gameUpperHeader: gameUpperHeaderChannel13, gameUpperBodyCodec: gameUpperBodyCodecPlain}}
	if err := s.sendCurrentRaidDailyAvailability(&gameSession{conn: conn, selectedCharacterID: 19}, "test"); err != nil {
		t.Fatal(err)
	}
	packets := splitAllGameServerUpperPackets(t, conn.write.Bytes())
	if len(packets) != 1 || packets[0].Header.MsgID != currentRaidDailyAvailabilityMsgID || packets[0].Header.Classification != 0 || hex.EncodeToString(packets[0].Body) != vendorRaidDailyAvailabilityHex[32:] {
		t.Fatalf("legacy packet changed: %+v", packets)
	}
}
