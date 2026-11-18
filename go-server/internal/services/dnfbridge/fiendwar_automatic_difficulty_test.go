package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

func TestFiendWarAutomaticDifficultyRequiresDeclaredMetadata(t *testing.T) {
	for _, value := range []int64{0, 1, 2, 3, 4} {
		d := worldmap.Dungeon{ID: 4965}
		d.Metadata.Difficulty = worldmap.OptionalInt{Set: true, Value: value}
		body, err := currentFiendWarDeclaredSelectBody(d)
		if err != nil {
			t.Fatal(err)
		}
		r, err := dungeoncmd.DecodeSelectDungeonRequest(body)
		if err != nil || r.DungeonID != 4965 || r.Difficulty != byte(value) {
			t.Fatalf("declared value%d lost: request=%+v err=%v", value, r, err)
		}
	}
	for _, d := range []worldmap.Dungeon{
		{ID: 4965},
		{ID: 0},
		{ID: -1},
		{ID: int64(^uint32(0)) + 1},
	} {
		if body, err := currentFiendWarDeclaredSelectBody(d); err == nil || body != nil {
			t.Fatalf("invalid declaration accepted: %+v", d)
		}
	}
	for _, value := range []int64{-1, 5, 256} {
		d := worldmap.Dungeon{ID: 4965}
		d.Metadata.Difficulty = worldmap.OptionalInt{Set: true, Value: value}
		if body, err := currentFiendWarDeclaredSelectBody(d); err == nil || body != nil {
			t.Fatalf("invalid difficulty%d accepted", value)
		}
	}
}

// Run the existing automatic admission owner, with four actual-PVF fixture
// characters. The wire check catches a defaulted request surviving into op28.
func TestRealPVFFiendWarAutomaticDifficultyFourMembers(t *testing.T) {
	f, peers := r134N1FixtureMembers(t, 4)
	s, b := f.service, f.state.Battle
	seen := map[int]bool{}
	area := 0
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon <= 0 {
			continue
		}
		d, ok := s.worldMapTable.FindDungeon(int64(a.Dungeon))
		if !ok || !d.Metadata.Difficulty.Set || d.Metadata.Difficulty.Value != 2 {
			t.Fatalf("real FiendWar DGN=%d lacks declared difficulty2", a.Dungeon)
		}
		seen[a.Dungeon] = true
		body, err := s.currentFiendWarEncounterSelectBody(uint32(a.Dungeon))
		if err != nil || len(body) < 5 || body[4] != 2 {
			t.Fatalf("real dungeon%d automatic body lost declared difficulty: %x %v", a.Dungeon, body, err)
		}
		if b.FiendWar.AreaOpen(id) && a.MemberMax >= len(peers) && (area == 0 || id < area) {
			area = id
		}
	}
	if area == 0 || len(seen) < 20 {
		t.Fatalf("insufficient real encounter catalog: area=%d dungeons=%d", area, len(seen))
	}
	for _, p := range peers {
		r134N1Presence(t, f, p, area)
	}
	if err := s.tryCurrentFiendWarEncounter(peers[0]); err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		r := p.dungeon.runtime
		if r == nil || r.Request.Difficulty != 2 || r.raidEntry == nil || len(r.raidEntry.Participants) != 4 {
			if r == nil {
				t.Fatalf("character%d automatic runtime absent: %s", p.selectedCharacterID, f.logText(t))
			}
			t.Fatalf("character%d automatic difficulty=%d; want PVF2, participants=%+v", p.selectedCharacterID, r.Request.Difficulty, r.raidEntry)
		}
		found := false
		for _, packet := range splitAllGameServerUpperPackets(t, p.conn.(*partyDungeonSyncConn).frames()) {
			if packet.Header.Classification == 0 && packet.Header.MsgID == 28 {
				found = true
				if len(packet.Body) < 5 || packet.Body[4] != 2 {
					t.Fatalf("character%d op28 difficulty wrong: %x", p.selectedCharacterID, packet.Body)
				}
			}
		}
		if !found {
			t.Fatalf("character%d op28 absent", p.selectedCharacterID)
		}
	}
	t.Logf("four members retain difficulty2 through automatic admission and op28; checked%d PVF dungeons", len(seen))
}
