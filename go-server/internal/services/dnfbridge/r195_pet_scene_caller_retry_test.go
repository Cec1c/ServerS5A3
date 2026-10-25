package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
)

func TestR195PetSceneCallerWriteFailureRetries(t *testing.T) {
	for _, caller := range []struct {
		name            string
		call            func(*Service, *gameSession, string) error
		allowRunePrefix bool
	}{
		{"initial_mode1", (*Service).sendSelectedCreatureInitialStateAfterMode1, true},
		{"scene_ready", (*Service).sendSelectedCreatureSceneReadyProjection, false},
	} {
		t.Run(caller.name, func(t *testing.T) {
			for _, failure := range []struct {
				name   string
				msgID  uint16
				prefix []string
			}{
				{name: "metadata", msgID: currentCreatureStateTableMsgID},
				{name: "slot26", msgID: uint16(dnfenum.CmdPacketWalkoutPartyMember), prefix: []string{"op105"}},
				{name: "growth", msgID: currentCreatureGrowthMsgID, prefix: []string{"op105", "slot26"}},
			} {
				t.Run(failure.name, func(t *testing.T) {
					service, session, _ := r195NewPetSceneMetadataFixture(t)
					wantErr := errors.New("injected pet caller projection write failure")
					conn := &failNthDungeonWriteConn{err: wantErr}
					conn.failWhen = func(data []byte) bool {
						if failure.msgID == uint16(dnfenum.CmdPacketWalkoutPartyMember) {
							return r195TownPetSlot26Write(data)
						}
						packet, rest := splitLongHengGameServerUpperPacket(t, data)
						return packet.Header.Classification == 0 && packet.Header.MsgID == failure.msgID && len(rest) == 0
					}
					session.conn = conn
					if err := caller.call(service, session, "r195_pet_caller_failure"); !errors.Is(err, wantErr) {
						t.Fatalf("caller error=%v want injected error=%v", err, wantErr)
					}
					if len(conn.failed) == 0 {
						t.Fatal("caller never attempted the selected pet write")
					}
					if session.selectedCreatureStateTableSent {
						t.Fatal("failed caller left metadata flag true, suppressing scene-ready recovery")
					}
					if got := r195PetCallerPacketSignatures(t, conn.write.Bytes(), caller.allowRunePrefix); !r195SameStrings(got, failure.prefix) {
						t.Fatalf("successful pet prefix=%v want=%v", got, failure.prefix)
					}

					conn.failWhen = nil
					conn.write.Reset()
					// Recover through the real scene-ready caller, including when
					// the original failure came from the initial-mode1 caller.
					if err := service.sendSelectedCreatureSceneReadyProjection(session, "r195_pet_caller_recovery"); err != nil {
						t.Fatal(err)
					}
					if got := r195PetCallerPacketSignatures(t, conn.write.Bytes(), false); !r195SameStrings(got, []string{"op105", "slot26", "op102"}) {
						t.Fatalf("scene-ready recovery order=%v want=[op105 slot26 op102]", got)
					}
					if !session.selectedCreatureStateTableSent {
						t.Fatal("successful scene-ready recovery did not commit metadata flag")
					}
					before := append([]byte(nil), conn.write.Bytes()...)
					writes := conn.writes
					if err := service.sendSelectedCreatureSceneReadyProjection(session, "r195_pet_caller_repeated_ready"); err != nil {
						t.Fatal(err)
					}
					if conn.writes != writes || !bytes.Equal(conn.write.Bytes(), before) {
						t.Fatal("second scene-ready attempted another write after successful recovery")
					}
					if !session.selectedCreatureStateTableSent {
						t.Fatal("second scene-ready cleared the committed metadata flag")
					}
				})
			}
		})
	}
}

// Both callers use server16 even when the sequence at header+11 is nonzero.
// The initial caller may prepend one equipment-rune op14; it must not count
// as the pet slot26 update or conceal a new actor/mode1 rebuild.
func r195PetCallerPacketSignatures(t *testing.T, data []byte, allowRunePrefix bool) []string {
	t.Helper()
	var signatures []string
	for len(data) != 0 {
		packet, rest := splitLongHengGameServerUpperPacket(t, data)
		frame := data[:len(data)-len(rest)]
		data = rest
		if packet.Header.Classification != 0 {
			t.Fatalf("unexpected caller packet classification=%d msg=%d", packet.Header.Classification, packet.Header.MsgID)
		}
		switch packet.Header.MsgID {
		case uint16(dnfenum.CmdPacketSetUDPIPPort), 1371:
			t.Fatalf("pet caller emitted actor/mode1 rebuild msg=%d body=%x", packet.Header.MsgID, packet.Body)
		case currentCreatureStateTableMsgID:
			signatures = append(signatures, "op105")
		case uint16(dnfenum.CmdPacketWalkoutPartyMember):
			if r195TownPetSlot26Write(frame) {
				signatures = append(signatures, "slot26")
				continue
			}
			if !allowRunePrefix || len(signatures) != 0 || !r195PetCallerRunePrefixBody(packet.Body) {
				t.Fatalf("unexpected non-pet caller op14 body=%x", packet.Body)
			}
			allowRunePrefix = false
		case currentCreatureGrowthMsgID:
			signatures = append(signatures, "op102")
		default:
			t.Fatalf("unexpected caller packet msg=%d body=%x", packet.Header.MsgID, packet.Body)
		}
	}
	return signatures
}

func r195PetCallerRunePrefixBody(body []byte) bool {
	if len(body) < 3 || body[0] != currentSocketListEquipment {
		return false
	}
	count := int(binary.LittleEndian.Uint16(body[1:3]))
	if count == 0 || len(body) != 3+count*currentItemListEntryWireSize {
		return false
	}
	for offset := 3; offset < len(body); offset += currentItemListEntryWireSize {
		entry := body[offset : offset+currentItemListEntryWireSize]
		slot := binary.LittleEndian.Uint16(entry[:2])
		if slot > 32 || slot == r195TownPetRefreshSlot ||
			binary.LittleEndian.Uint16(entry[currentEquipmentEffectRuneWireOffset:currentEquipmentEffectRuneWireOffset+2]) == 0 {
			return false
		}
	}
	return true
}
