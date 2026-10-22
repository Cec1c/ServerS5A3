package dnfbridge

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"strings"
	"testing"
)

// Packet logging is disabled in the reported first-party/rejoin incident.
// Exercise the real sender to keep its bounded application diagnostic useful
// without changing any of the actor frames delivered to the client.
func TestR195PeerWireDiagnosticMatchesSentFramesWithoutPacketLog(t *testing.T) {
	s, leader, follower, _ := partyDungeonOwnerSwitchFixture(t, partyDungeonOwnerSceneContext, false)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	wire := captureDungeonPeerActorWire(t, s, leader, follower, "r195-diagnostic")
	event := r195DiagnosticEvent(t, &logs, "game-dungeon-party-peer-actor-send")
	for name, body := range map[string][]byte{"mode0": wire.mode0, "mode1": wire.mode1} {
		if event[name+"_digest"] != currentProjectionBodyDigest(body) || event[name+"_body_len"] != float64(len(body)) || event[name+"_owner"] != float64(body[4]) {
			t.Fatalf("%s diagnostic does not describe sent frame: %v", name, event)
		}
	}
	if event["mode0_object_key"] != float64(binary.LittleEndian.Uint16(wire.mode0[5+0x47:])) || event["mode1_object_key"] != float64(binary.LittleEndian.Uint16(wire.mode1[21:])) || event["actor_char_id"] != float64(follower.selectedCharacterID) || event["receiver_char_id"] != float64(leader.selectedCharacterID) {
		t.Fatalf("diagnostic lost directional actor identity: %v", event)
	}
	if event["appearance_rows"] == nil || event["mode1_create_rows"] == nil {
		t.Fatalf("diagnostic lost decoded appearance composition: %v", event)
	}
	if s.packetLog != nil {
		t.Fatal("test unexpectedly enabled packet logging")
	}
	s.logRaidPeerDiagnostic("game-dungeon-party-peer-actor-send", "body", "PRIVATE_PACKET", "account_id", "PRIVATE_ACCOUNT", "error", "PRIVATE_ERROR")
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("diagnostic forwarded unbounded/private fields")
	}
}
