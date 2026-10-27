package dnfbridge

import (
	"context"
	"testing"
)

// Reuse the real dungeon164 selection, but discard the historical fixture's
// channel binding and commit through the production owner before upper316.
// Town origin is an explicit local fixture; no player or PVF data is changed.
func TestR159RidableNormalCommitOwnsChannelRealPVF(t *testing.T) {
	for _, mode := range []string{"valid", "changed_channel", "stale_run"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrdinaryRidableFormalFixture(t)
			_, _, origin := newBackToVillageRuntime(t)
			for _, session := range []*gameSession{f.peer.source, f.peer.target} {
				r := session.dungeon.runtime
				f.peer.service.releasePartyDungeonInstance(r, session.selectedCharacterID, "r159_fixture_reprepare")
				session.dungeon.runtime = nil
				r.lifecycleToken = 0
				r.ridableReportChannelID, r.ridableReportChannelType = 0, 0
				r.Character.Stats = make(map[string]int64)
				for key, value := range origin.Character.Stats {
					r.Character.Stats[key] = value
				}
				if err := f.peer.repositories.Character.Save(context.Background(), r.Character); err != nil {
					t.Fatal(err)
				}
				if err := freezeCurrentDungeonTownReturnOrigin(session, r); err != nil {
					t.Fatal("freeze fixture origin", err)
				}
				if err := f.peer.service.commitDungeonRuntime(session, r); err != nil {
					t.Fatal("normal commit", err)
				}
				session.conn.(*bufferConn).write.Reset()
			}
			if f.peer.source.dungeon.runtime.partyInstance != f.peer.target.dungeon.runtime.partyInstance {
				t.Fatal("normal commits did not join one shared instance")
			}
			var definition uint32
			for _, entry := range f.peer.target.dungeon.runtime.randomizedObjects.Entries {
				if entry.Source.Map == f.scene.Coordinate && currentDungeonRidableTerminalEligible(entry) {
					definition = uint32(entry.Source.ObjectID)
					break
				}
			}
			if definition == 0 {
				t.Fatalf("actual frozen room has no eligible terminal source: %+v", f.peer.target.dungeon.runtime.randomizedObjects.Entries)
			}
			switch mode {
			case "changed_channel":
				f.peer.target.channel.ID++
			case "stale_run":
				f.peer.target.dungeon.runToken++
			}
			sendOrdinaryRidableFormal(t, f, f.peer.target, 20, 0, ordinaryRidableReportBody(3, definition, byte(f.scene.Coordinate.X), byte(f.scene.Coordinate.Y)))
			want := 4
			if mode == "valid" {
				want = 3
			}
			for _, session := range []*gameSession{f.peer.source, f.peer.target} {
				rows := ordinaryRidableFullProjection(t, session, f.scene, currentDungeonStartMapPayloadRefresh)
				if len(rows) != want {
					t.Fatalf("normal commit -> upper316 -> shared refresh rows=%d want=%d channelBinding=%d/%d actualChannel=%d/%d", len(rows), want, session.dungeon.runtime.ridableReportChannelID, session.dungeon.runtime.ridableReportChannelType, session.channel.ID, session.channel.Type)
				}
			}
		})
	}
}
