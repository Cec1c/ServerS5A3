package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func antonModeCatalogOracle(t *testing.T, previous []byte) []byte {
	t.Helper()
	shape := parseCurrentSpendTimeEventInfoBody(t, previous)
	activeOffset := 2
	for _, row := range shape.baseRows {
		activeOffset += len(row.raw)
	}
	// Native activity lookup uses decimal 2426 (little endian 7A 09).
	base := mustHexBytes(t, "7a0901000000000000000000000000")
	active := append([]byte{0x7a, 0x09}, make([]byte, 48)...)
	want := append([]byte(nil), previous[:activeOffset]...)
	binary.LittleEndian.PutUint16(want[:2], shape.baseCount+1)
	want = append(want, base...)
	want = append(want, shape.activeCount+1)
	want = append(want, previous[activeOffset+1:]...)
	return append(want, active...)
}

func TestAntonModeSelectionAddsNativeAvailabilityAndPreservesCatalog(t *testing.T) {
	for _, joust := range []bool{false, true} {
		for _, npc := range []bool{false, true} {
			old, err := buildCurrentSpendTimeEventInfoBodyWithAntonChannelEvent(currentSpendTimeJoustCatalogGoldenRewards, 10800, joust, npc, true)
			if err != nil {
				t.Fatal(err)
			}
			body, err := buildCurrentSpendTimeEventInfoBodyWithAntonModeSelection(currentSpendTimeJoustCatalogGoldenRewards, 10800, joust, npc, true, true)
			if err != nil || !bytes.Equal(body, antonModeCatalogOracle(t, old)) {
				t.Fatalf("mode catalog mismatch: %v", err)
			}
			shape := parseCurrentSpendTimeEventInfoBody(t, body)
			if !shape.hasEvent(181) || !shape.hasEvent(2426) || shape.hasEvent(282) || shape.hasEvent(2449) {
				t.Fatal("unrelated raid activity added")
			}
			disabled, err := buildCurrentSpendTimeEventInfoBodyWithAntonModeSelection(currentSpendTimeJoustCatalogGoldenRewards, 10800, joust, npc, true, false)
			if err != nil || !bytes.Equal(disabled, old) {
				t.Fatal("rollback changed previous catalog")
			}
			if dir := os.Getenv("DNF90_ANTON_MODE_FIXTURES"); dir != "" && !joust && !npc {
				offset := 2
				for _, row := range shape.baseRows {
					offset += len(row.raw)
				}
				oldOffset := 2
				for _, row := range parseCurrentSpendTimeEventInfoBody(t, old).baseRows {
					oldOffset += len(row.raw)
				}
				for name, data := range map[string][]byte{"catalog-full.bin": body, "catalog-base.bin": body[:offset], "catalog-base-previous.bin": old[:oldOffset]} {
					if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	if body, err := buildCurrentSpendTimeEventInfoBodyWithAntonModeSelection(currentSpendTimeJoustCatalogGoldenRewards, 10800, false, false, false, true); err == nil || body != nil {
		t.Fatal("chooser accepted without channel availability")
	}
}

func TestAntonModeSelectionFailsClosedForMissingPVFAndOtherProfile(t *testing.T) {
	var nilService *Service
	if nilService.currentAntonModeSelectionAvailable(nil) {
		t.Fatal("nil service enabled chooser")
	}
	for _, profile := range []string{"", "legacy", "official-24.9"} {
		s := &Service{options: options{currentClientVersion: profile}}
		if s.currentAntonModeSelectionAvailable(nil) {
			t.Fatalf("missing archive enabled profile %q", profile)
		}
	}
}

func TestRealPVFAntonModeSelectionFirstDescriptorAndRollback(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires published PVF")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(antonModeSelectionEventEnv, "")
	// Production's joust owner is disabled; this test uses the same setting
	// instead of instantiating unrelated betting rules from the raid PVF.
	s := &Service{options: options{currentClientVersion: "official-24.9", joustEventDisabled: true}, initialEquipmentArchive: archive}
	if !s.currentAntonModeSelectionAvailable(nil) {
		t.Fatal("actual Anton admission modes unavailable")
	}
	for _, value := range []string{"off", "0", "false", "no", "disabled", "legacy"} {
		t.Setenv(antonModeSelectionEventEnv, value)
		if s.currentAntonModeSelectionAvailable(nil) {
			t.Fatalf("rollback %q enabled chooser", value)
		}
	}
	t.Setenv(antonModeSelectionEventEnv, "")
	conn := &bufferConn{}
	session := &gameSession{conn: conn, connID: "anton-mode-first-descriptor"}
	catalog := &currentSpendTimeRuntimeCatalog{rewardItemIDs: currentSpendTimeJoustCatalogGoldenRewards, totalStageSeconds: 10800}
	if sent, err := s.sendCurrentSpendTimeProtocolState(session, catalog, 0, 0, true); err != nil || !sent {
		t.Fatalf("first descriptor: sent=%t err=%v", sent, err)
	}
	packet, rest := splitGameServerUpperPacketWithHeader(t, conn.write.Bytes(), 16)
	plain, err := zlibDecompress(packet.Body)
	if err != nil || packet.Header.MsgID != 108 || !parseCurrentSpendTimeEventInfoBody(t, plain).hasEvent(2426) {
		t.Fatalf("protected chooser descriptor missing: %v", err)
	}
	progress, _ := splitGameServerUpperPacket(t, rest)
	if progress.Header.MsgID != 1206 {
		t.Fatal("chooser broke adjacent progress ordering")
	}
	conn.write.Reset()
	if sent, err := s.sendCurrentSpendTimeProtocolState(session, catalog, 1, 0, false); err != nil || sent {
		t.Fatalf("repeat descriptor: sent=%t err=%v", sent, err)
	}
	for _, p := range splitAllGameServerUpperPackets(t, conn.write.Bytes()) {
		if p.Header.MsgID == 108 {
			t.Fatal("repeat sent surviving singleton's base catalog")
		}
	}
}
