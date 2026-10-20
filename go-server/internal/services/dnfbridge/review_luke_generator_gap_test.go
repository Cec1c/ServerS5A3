package dnfbridge

import (
	"context"
	"encoding/binary"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
	"time"
)

func reviewActualLukeGeneratorFixture(t *testing.T, withPeer bool) (*raidStartGuardFixture, *gameSession, *runtimeDungeonState, *gameSession, time.Time) {
	t.Helper()
	f, s, _ := normalLukeFixture(t)
	a := f.service.initialEquipmentArchive
	table, e := worldmap.LoadSource(context.Background(), a, worldmap.Options{})
	if e != nil {
		t.Fatal(e)
	}
	resolver, e := worldmap.NewResolver(table)
	if e != nil {
		t.Fatal(e)
	}
	d, ok := table.FindDungeon(5054)
	if !ok {
		t.Fatal("actual birth dungeon missing")
	}
	var found bool
	for maze := range d.Mazes {
		top, e := worldmap.BuildDungeonLayout(resolver, 5054, maze, func(c worldmap.DungeonMapChoice) (int64, error) { return c.Candidates[0].ID, nil })
		if e != nil {
			t.Fatal(e)
		}
		for _, rm := range top.Rooms() {
			if rm.Map == nil {
				continue
			}
			budget := 0
			for _, o := range rm.Map.Map.PassiveObjects {
				if o.ObjectID == 64249 || o.ObjectID == 64250 {
					budget++
				}
			}
			if budget == 0 {
				continue
			}
			found = true
			coord := rm.Coordinate
			top.Start = &coord
			run, e := worldmap.NewDungeonRun(top)
			if e != nil {
				t.Fatal(e)
			}
			ds, e := worldmap.NewDungeonSession(run)
			if e != nil {
				t.Fatal(e)
			}
			scene, _ := ds.Scene()
			mt, e := newPVFDungeonMonsterCatalog(a)
			if e != nil {
				t.Fatal(e)
			}
			room, _, e := newRuntimeDungeonRoom(scene, mt, firstDungeonMonsterObjectKey)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = room.AnnounceAllActors(ds); e != nil {
				t.Fatal(e)
			}
			r := &runtimeDungeonState{Dungeon: d, MazeIndex: maze, Session: ds, Room: room, AccountID: s.accountID, Character: repo.CharacterRecord{CharacterID: "19", AccountID: s.accountID}}
			s.dungeon.runtime = r
			s.postStartMapPlayerStateSent = true
			var peer *gameSession
			var followers []*runtimePartyPreparedDungeonEntry
			if withPeer {
				peer = f.session()
				peer.selectedCharacterID = 20
				peer.accountID = "account-20"
				peer.channel = s.channel
				peer.loginPacketSeen = true
				peer.accountFromToken = true
				f.service.bindGameSessionCharacter(peer, 20)
				f.state.Members[1].GroupIndex = 1
				f.state.Battle.MemberGroups[20] = 1
				prun, err := worldmap.NewDungeonRun(top)
				if err != nil {
					t.Fatal(err)
				}
				pds, err := worldmap.NewDungeonSession(prun)
				if err != nil {
					t.Fatal(err)
				}
				psc, _ := pds.Scene()
				proom, _, err := newRuntimeDungeonRoom(psc, mt, firstDungeonMonsterObjectKey)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = proom.AnnounceAllActors(pds); err != nil {
					t.Fatal(err)
				}
				pr := &runtimeDungeonState{Dungeon: d, MazeIndex: maze, Session: pds, Room: proom, AccountID: peer.accountID, Character: repo.CharacterRecord{CharacterID: "20", AccountID: peer.accountID}}
				peer.dungeon.runtime = pr
				peer.postStartMapPlayerStateSent = true
				followers = []*runtimePartyPreparedDungeonEntry{{session: peer, runtime: pr}}
			}
			if e = f.service.freezeCurrentRaidDungeonGroupEntry(s, r, followers); e != nil {
				t.Fatal(e)
			}
			at := time.Now()
			if _, _, e = f.service.reduceCurrentLukeSceneLocked(s, r, "enter", at); e != nil {
				t.Fatal(e)
			}
			if peer != nil {
				if _, _, err := f.service.reduceCurrentLukeSceneLocked(peer, peer.dungeon.runtime, "enter", at); err != nil {
					t.Fatal(err)
				}
			}
			return f, s, r, peer, at
		}
	}
	if !found {
		t.Fatal("actual owned raid generator not present in chosen birth layouts")
	}

	return nil, nil, nil, nil, time.Time{}
}

func reviewLukeGeneratorBody(symbol, amount uint32, op byte) []byte {
	b := make([]byte, 9)
	binary.LittleEndian.PutUint32(b, symbol)
	binary.LittleEndian.PutUint32(b[4:], amount)
	b[8] = op
	return b
}
func TestReviewActualLukeGeneratorReportAdvancesOwnedCounter(t *testing.T) {
	f, s, r, _, _ := reviewActualLukeGeneratorFixture(t, false)
	before := f.state.Battle.Anton.NativeSymbols()[117]
	handled, e := f.service.handleOnlineRaidCommand(s, 662, reviewLukeGeneratorBody(117, 1, 1))
	if !handled || e != nil || f.state.Battle.Anton.NativeSymbols()[117] != before+1 {
		t.Fatal("legal actual-room public Luke generator dispatch must count once", handled, e)
	}
	scene, _ := r.Session.Scene()
	t.Logf("actual birth room=%s map=%d symbol117 %d→%d", scene.Coordinate.String(), scene.Map.Map.ID, before, f.state.Battle.Anton.NativeSymbols()[117])
}
func TestReviewActualLukeGeneratorSharedPeerAndDuplicateBudget(t *testing.T) {
	f, s, r, peer, at := reviewActualLukeGeneratorFixture(t, true)
	body := reviewLukeGeneratorBody(117, 1, 1)
	if r.raidEntry.RunID != peer.dungeon.runtime.raidEntry.RunID {
		t.Fatal("shared fixture lost run")
	}
	for i, owner := range []*gameSession{peer, s, peer, s} {
		_, changed, e := f.service.recordCurrentLukeGeneratorLocked(owner, body, at)
		if e != nil || changed != (i == 0) || f.state.Battle.Anton.NativeSymbols()[117] != 1 {
			t.Fatal(i, changed, e, f.state.Battle.Anton.NativeSymbols()[117])
		}
	}
}
func TestReviewActualLukeGeneratorRejectsWrongProducerAndOwner(t *testing.T) {
	f, s, r, _, at := reviewActualLukeGeneratorFixture(t, false)
	for _, body := range [][]byte{reviewLukeGeneratorBody(110, 1, 1), reviewLukeGeneratorBody(118, 1, 1), reviewLukeGeneratorBody(117, 2, 1), reviewLukeGeneratorBody(117, 1, 0), reviewLukeGeneratorBody(117, 1, 2), {117}, append(reviewLukeGeneratorBody(117, 1, 1), 0)} {
		if _, changed, e := f.service.recordCurrentLukeGeneratorLocked(s, body, at); e == nil || changed || f.state.Battle.Anton.NativeSymbols()[117] != 0 {
			t.Fatal("foreign producer accepted", body, e)
		}
	}
	body := reviewLukeGeneratorBody(117, 1, 1)
	cases := []struct {
		name  string
		alter func() func()
	}{
		{"instance", func() func() {
			old := r.raidEntry.InstanceID
			r.raidEntry.InstanceID = "foreign"
			return func() { r.raidEntry.InstanceID = old }
		}},
		{"epoch", func() func() { r.raidEntry.Attempt++; return func() { r.raidEntry.Attempt-- } }},
		{"phase", func() func() { r.raidEntry.Phase = 1; return func() { r.raidEntry.Phase = 0 } }},
		{"participant", func() func() {
			old := r.raidEntry.Participants
			r.raidEntry.Participants = []uint16{20}
			return func() { r.raidEntry.Participants = old }
		}},
		{"channel", func() func() { s.channel.ID++; return func() { s.channel.ID-- } }},
		{"account", func() func() { r.AccountID = "other"; return func() { r.AccountID = s.accountID } }},
		{"placement", func() func() {
			s.postStartMapPlayerStateSent = false
			return func() { s.postStartMapPlayerStateSent = true }
		}},
		{"expired", func() func() {
			old := f.state.Battle.Deadline
			f.state.Battle.Deadline = at
			return func() { f.state.Battle.Deadline = old }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restore := c.alter()
			defer restore()
			if _, changed, e := f.service.recordCurrentLukeGeneratorLocked(s, body, at); e == nil || changed || f.state.Battle.Anton.NativeSymbols()[117] != 0 {
				t.Fatal("foreign owner accepted", e)
			}
		})
	}
	replacement := f.session()
	replacement.channel = s.channel
	replacement.loginPacketSeen = true
	replacement.accountFromToken = true
	f.service.bindGameSessionCharacter(replacement, 19)
	if _, changed, e := f.service.recordCurrentLukeGeneratorLocked(s, body, at); e == nil || changed || f.state.Battle.Anton.NativeSymbols()[117] != 0 {
		t.Fatal("old socket after actual replacement accepted", e)
	}
}
