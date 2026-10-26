package dnfbridge

import (
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/raid"
)

func r195MapIdentity() currentRaidProjectionIdentity {
	return currentRaidProjectionIdentity{InstanceID: "battle", Phase: 0, CharID: 1047, Generation: 1}
}

func r195FullBody(t *testing.T, rows []raid.DungeonState, infection uint32) []byte {
	t.Helper()
	body, err := raid.BuildDungeonStateBody(rows, infection)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// The 585 body lists every dungeon row, but native v157 only reacts to the rows
// it receives: 01A65320 -> Luke vtbl+0x18 (01111F20) enqueues dstr30207
// whenever a NEW state is 3. A finished sanctum keeps state 3 for the phase, so
// re-sending the whole map because some other dungeon changed repeats the old
// clear. Only the changed row may be published.
func TestR195MapRowDeltaDoesNotRepeatClearedRow(t *testing.T) {
	previous := map[uint32]byte{5054: 3, 5055: 0, 5056: 2}
	rows := []raid.DungeonState{
		{ID: 5054, State: 3}, // already announced, must stay silent
		{ID: 5055, State: 1}, // other sanctum really changed
		{ID: 5056, State: 2},
	}
	delta, next := currentRaidMapRowDelta(previous, rows)
	if len(delta) != 1 || delta[0].ID != 5055 || delta[0].State != 1 {
		t.Fatalf("delta=%v want only 5055->1", delta)
	}
	if len(next) != 3 || next[5054] != 3 || next[5055] != 1 || next[5056] != 2 {
		t.Fatalf("cache=%v", next)
	}
}

func TestR195MapProjectionPacketsSendsFullOnFirstAndResubscribe(t *testing.T) {
	rows := []raid.DungeonState{{ID: 5054, State: 3}, {ID: 5055, State: 0}}
	full := r195FullBody(t, rows, 0)
	for _, tc := range []struct {
		name        string
		cache       currentRaidMapProjection
		resubscribe bool
	}{
		{"first", currentRaidMapProjection{}, false},
		{"resubscribe", currentRaidMapProjection{rows: map[uint32]byte{5054: 3, 5055: 0}, identity: r195MapIdentity(), valid: true}, true},
		{"new_identity", currentRaidMapProjection{rows: map[uint32]byte{5054: 3, 5055: 0}, identity: currentRaidProjectionIdentity{InstanceID: "old"}, valid: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			packets, _, err := tc.cache.packets(2, tc.resubscribe, r195MapIdentity(), rows, 0, full)
			if err != nil || len(packets) != 1 || packets[0].opcode != 585 || packets[0].body[1] != 2 {
				t.Fatalf("packets=%v err=%v want one full 585", packets, err)
			}
		})
	}
}

// The native state-3 prompt is proven on the Luke controller only. Other modes
// keep the historical full-map publication.
func TestR195MapProjectionPacketsStayFullOutsideLuke(t *testing.T) {
	rows := []raid.DungeonState{{ID: 210, State: 3}}
	cache := currentRaidMapProjection{rows: map[uint32]byte{210: 3}, identity: r195MapIdentity(), valid: true}
	packets, _, err := cache.packets(1, false, r195MapIdentity(), rows, 0, r195FullBody(t, rows, 0))
	if err != nil || len(packets) != 1 || packets[0].body[1] != 1 {
		t.Fatalf("mode1 packets=%v err=%v want full map", packets, err)
	}
}

// A change limited to the infection word must still reach the client as an
// empty row list carrying the new infection.
func TestR195MapProjectionPacketsPublishesInfectionOnlyChange(t *testing.T) {
	rows := []raid.DungeonState{{ID: 5054, State: 3}, {ID: 5055, State: 0}}
	cache := currentRaidMapProjection{rows: map[uint32]byte{5054: 3, 5055: 0}, infection: 0, identity: r195MapIdentity(), valid: true}
	packets, next, err := cache.packets(2, false, r195MapIdentity(), rows, 224, r195FullBody(t, rows, 224))
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 1 || packets[0].opcode != 585 {
		t.Fatalf("packets=%v want one 585", packets)
	}
	body := packets[0].body
	if body[0] != 0 || body[1] != 0 || binary.LittleEndian.Uint32(body[2:6]) != 224 {
		t.Fatalf("body=%x want count0 infection224", body)
	}
	if next.infection != 224 || !next.valid {
		t.Fatalf("next=%+v", next)
	}
}

func TestR195MapProjectionPacketsSilentWhenNothingChanged(t *testing.T) {
	rows := []raid.DungeonState{{ID: 5054, State: 3}, {ID: 5055, State: 0}}
	cache := currentRaidMapProjection{rows: map[uint32]byte{5054: 3, 5055: 0}, infection: 0, identity: r195MapIdentity(), valid: true}
	packets, _, err := cache.packets(2, false, r195MapIdentity(), rows, 0, r195FullBody(t, rows, 0))
	if err != nil || len(packets) != 0 {
		t.Fatalf("packets=%v err=%v want silence", packets, err)
	}
}
