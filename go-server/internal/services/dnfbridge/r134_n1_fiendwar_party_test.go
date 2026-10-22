package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/town"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// The archive, battlefield, dungeon and map are real. Authentication, characters,
// raid start and town presence are fixtures; client requests use the upper handler.
func r134N1Fixture(t *testing.T) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	f, peers := actualCurrentFiendWar(t, 2)
	s, b := f.service, f.state.Battle
	s.options.pvfPath = os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	s.onlinePlayers = newOnlinePlayerManager()
	s.dungeonChoice = func(int) (int, error) { return 0, nil }
	s.dungeonSeed = func() (uint32, error) { return 1, nil }
	var err error
	s.townCatalog, err = town.Load(context.Background(), s.initialEquipmentArchive, town.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapTable, err = worldmap.LoadSource(context.Background(), s.initialEquipmentArchive, worldmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.worldMapResolver, err = worldmap.NewResolver(s.worldMapTable)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonMonsterTable, err = newPVFDungeonMonsterCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	s.dungeonAICharacterTable, err = newPVFDungeonAICharacterCatalog(s.initialEquipmentArchive)
	if err != nil {
		t.Fatal(err)
	}
	delete(s.raids, f.state.RaidKey)
	f.state.RaidKey = uint32(s.channelAdvertiseID())<<24 | 244<<16 | 19
	s.raids[f.state.RaidKey] = f.state
	f.state.Started = true
	b.ChannelID = 244
	b.EnterAfter, b.Deadline = time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	for _, p := range peers {
		p.channel.ID, p.channel.Group = 244, "fiendwar"
		p.residentChannel = p.channel
		p.connID = fmt.Sprintf("r134-n1-%d", p.selectedCharacterID)
		p.conn = &partyDungeonSyncConn{charID: p.selectedCharacterID}
		id := fmt.Sprint(p.selectedCharacterID)
		c, found, err := f.repos.Character.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		c.Level = 90
		c.Stats = map[string]int64{"fatigue": 156, "gold": 100000, "town_id": int64(b.FiendWar.Config.WaitingTown), "area_id": int64(b.FiendWar.Config.WaitingArea), "pos_x": 300, "pos_y": 200, "direction": 5}
		if err := f.repos.Character.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		r134N1Presence(t, f, p, b.FiendWar.Config.WaitingArea)
	}
	return f, peers
}

func r134N1Presence(t *testing.T, f *raidStartGuardFixture, p *gameSession, area int) {
	t.Helper()
	b := f.state.Battle
	b.FiendWar.MemberAreas[p.selectedCharacterID] = area
	setCurrentFiendWarTestOrigin(p, area)
	p.townPositionSnapshot = p.townSelectorOriginSnapshot
	c, found, err := f.repos.Character.Load(context.Background(), fmt.Sprint(p.selectedCharacterID))
	if err != nil || !found {
		t.Fatal(found, err)
	}
	c.Stats["town_id"], c.Stats["area_id"] = int64(b.FiendWar.Config.WaitingTown), int64(area)
	if err := f.repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	p.townSceneReadyCharacterID = p.selectedCharacterID
	f.service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: p.selectedCharacterID, AccountID: p.accountID, Session: p, TownID: byte(b.FiendWar.Config.WaitingTown), AreaID: byte(area), ChannelID: p.channel.ID, Instance: f.state.RaidKey, RaidBattleInstance: b.InstanceID})
}

func r134N1Send(t *testing.T, s *Service, p *gameSession, op dnfenum.CmdPacket, body []byte) {
	t.Helper()
	if err := s.handleGameUpper(p, buildCurrentDungeonClientUpperForPhaseTest(t, uint16(op), body)); err != nil {
		t.Fatal(err)
	}
}

func r134N1Invite(t *testing.T, f *raidStartGuardFixture, peers []*gameSession) {
	t.Helper()
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, peers[1].selectedCharacterID)
	r134N1Send(t, f.service, peers[0], dnfenum.CmdPacketRequestPeer, body)
	if countUpperFramesByMsgID(peers[1].conn.(*partyDungeonSyncConn).frames(), 7) != 0 {
		t.Fatal("FiendWar manual invite forwarded", f.logText(t))
	}
	packets := splitAllGameServerUpperPackets(t, peers[0].conn.(*partyDungeonSyncConn).frames())
	found := false
	for _, packet := range packets {
		if packet.Header.MsgID == 10 && packet.Header.Classification == 1 && bytes.Equal(packet.Body, []byte{0, 3, 0}) {
			found = true
		}
	}
	if !found {
		t.Fatal("manual invitation lacks mode-bearing failure")
	}
}

func TestR134N1RealPVFWaitingPartyHandler(t *testing.T) {
	f, p := r134N1Fixture(t)
	r134N1Invite(t, f, p)
	r134N1Send(t, f.service, p[1], dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(p[0].selectedCharacterID, 0, 0))
	for i, peer := range p {
		state := f.service.runtimePartyStateForSession(peer)
		if state.PartyID != 0 {
			t.Fatalf("manual request created party: %+v", state)
		}
		if f.state.Battle.MemberGroups[peer.selectedCharacterID] != byte(i+1) {
			t.Fatal("frozen attack group changed")
		}
	}
	t.Logf("channel=244 type=42 waiting=%d/%d: manual formation rejected; personal raid seats retained", f.state.Battle.FiendWar.Config.WaitingTown, f.state.Battle.FiendWar.Config.WaitingArea)
}

func TestR134N1RealPVFLateAcceptAfterEncounter(t *testing.T) {
	f, p := r134N1Fixture(t)
	s, b := f.service, f.state.Battle
	// Model a lobby left by an older server version. New manual invites fail,
	// and encounter admission replaces this obsolete projection automatically.
	old, ok := s.replaceManagedRuntimeParty(runtimeRaidGroupPartyState(*f.state, f.state.Members[:1]))
	if !ok {
		t.Fatal("initial lobby")
	}
	r134N1Invite(t, f, p)
	area := 0
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon > 0 && b.FiendWar.AreaOpen(id) && a.MemberMax >= 2 && (area == 0 || id < area) {
			area = id
		}
	}
	if area == 0 {
		t.Fatal("no real open encounter")
	}
	for _, peer := range p {
		r134N1Presence(t, f, peer, area)
	}
	if err := s.tryCurrentFiendWarEncounter(p[0]); err != nil {
		t.Fatal(err)
	}
	for _, peer := range p {
		if peer.dungeon.runtime == nil || peer.dungeon.runtime.raidEntry == nil {
			request := dungeoncmd.SelectDungeonRequest{DungeonID: uint32(b.FiendWar.Config.Areas[area].Dungeon)}
			r, scene, err := s.prepareDungeonRuntime(context.Background(), p[0], request)
			if err == nil {
				_, err = s.prepareRuntimePartyDungeonEntry(context.Background(), p[0], s.runtimePartyStateForSession(p[0]), request, r, scene)
			}
			t.Logf("fixture entry preflight diagnostic: %v", err)
			t.Fatal("real encounter failed", f.logText(t))
		}
	}
	// Release the real loading barrier via the live legacy op37 dispatcher before
	// checking the late invite and subsequent dungeon-local roster refresh.
	restoreDispatch := partyDungeonLoadingBarrierDispatch
	partyDungeonLoadingBarrierDispatch = func(run func()) { run() }
	defer func() { partyDungeonLoadingBarrierDispatch = restoreDispatch }()
	for _, peer := range p {
		if err := s.handleLegacyGamePacket(peer, buildCurrentDungeonClientUpperForPhaseTest(t, uint16(dnfenum.CmdPacketFinishLoading), make([]byte, currentFinishLoadingLegacyRequestBodySize))); err != nil {
			t.Fatal(err)
		}
	}
	for _, peer := range p {
		if _, ready := s.currentDungeonPartyRunKey(peer); !ready || !peer.postStartMapPlayerStateSent {
			t.Fatal("real encounter loading barrier did not finish", f.logText(t))
		}
	}
	current := s.runtimePartyStateForSession(p[0])
	if current.PartyID == old.PartyID || len(runtimePartyMembers(current)) != 2 {
		t.Fatalf("encounter did not replace lobby: %+v", current)
	}
	for _, peer := range p {
		r134N1Reset(peer)
	}
	r134N1Send(t, s, p[1], dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(p[0].selectedCharacterID, 0, 0))
	ack := false
	for _, packet := range splitAllGameServerUpperPackets(t, p[1].conn.(*partyDungeonSyncConn).frames()) {
		if packet.Header.MsgID == uint16(dnfenum.CmdPacketResponsePeer) && packet.Header.Classification == 1 {
			ack = true
		}
	}
	if !ack {
		t.Fatal("late accepted invite has no terminal ACK after server already placed both actors in the same real encounter", f.logText(t))
	}
	beforeParty := s.runtimePartyStateForSession(p[0])
	beforeBattle := cloneRuntimeRaidBattle(b)
	beforeRuns := []*runtimeDungeonState{p[0].dungeon.runtime, p[1].dungeon.runtime}
	beforeAssets := make([]any, 0, 2)
	for _, peer := range p {
		inv, _, err := f.repos.Inventory.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if err != nil {
			t.Fatal(err)
		}
		beforeAssets = append(beforeAssets, inv)
	}
	for _, mode := range []byte{0, 12} {
		t.Run(fmt.Sprintf("idempotent_mode_%d", mode), func(t *testing.T) {
			for _, peer := range p {
				r134N1Reset(peer)
			}
			r134N1Send(t, s, p[1], dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(p[0].selectedCharacterID, mode, 0))
			want := []byte{1, byte(p[0].selectedCharacterID), byte(p[0].selectedCharacterID >> 8), mode}
			matches := 0
			for _, peer := range p {
				roster, rows := 0, 0
				for _, packet := range splitAllGameServerUpperPackets(t, peer.conn.(*partyDungeonSyncConn).frames()) {
					if packet.Header.Classification == 0 && packet.Header.MsgID == 153 {
						roster++
					}
					if packet.Header.Classification == 0 && packet.Header.MsgID == 9 {
						rows++
					}
					if packet.Header.Classification == 0 && (packet.Header.MsgID == 11 || packet.Header.MsgID == 8) {
						t.Fatal("existing peer handshake/accept notice replayed")
					}
					if peer == p[1] && packet.Header.Classification == 1 && packet.Header.MsgID == 11 && bytes.Equal(packet.Body, want) {
						matches++
					}
				}
				if roster != 1 || rows != 2 {
					t.Fatalf("char %d roster=%d rows=%d; require exactly one complete same-run refresh", peer.selectedCharacterID, roster, rows)
				}
			}
			if matches != 1 {
				t.Fatalf("exact ACK count %d", matches)
			}
		})
	}
	for _, name := range []string{"different_run", "different_raid", "different_instance", "different_party_owner", "departed", "return_pending", "completed_battle", "timed_out", "wrong_account", "stale_generation", "invalid_mode", "nonzero_value"} {
		t.Run(name, func(t *testing.T) {
			r := p[1].dungeon.runtime
			saved := *r.raidEntry
			oldInstance, oldAccount, oldGeneration := r.partyInstance, r.AccountID, p[1].characterGeneration
			oldReturn, oldComplete, oldTimeout := r.townReturnPending, b.PhaseCompleted, b.TimedOut
			oldDepartures := b.RunDepartures
			mode, value := byte(0), uint32(0)
			switch name {
			case "different_run":
				r.raidEntry.RunID += "-different"
			case "different_raid":
				r.raidEntry.RaidKey++
			case "different_instance":
				r.raidEntry.InstanceID += "-different"
			case "different_party_owner":
				r.partyInstance = &dungeonPartyInstance{}
			case "departed":
				b.RunDepartures = map[string]map[uint16]bool{r.raidEntry.RunID: {p[1].selectedCharacterID: true}}
			case "return_pending":
				r.townReturnPending = true
			case "completed_battle":
				b.PhaseCompleted = true
			case "timed_out":
				b.TimedOut = true
			case "wrong_account":
				r.AccountID = "wrong-account"
			case "stale_generation":
				p[1].characterGeneration++
			case "invalid_mode":
				mode = 1
			case "nonzero_value":
				value = 1
			}
			defer func() {
				*r.raidEntry = saved
				r.partyInstance = oldInstance
				r.AccountID = oldAccount
				p[1].characterGeneration = oldGeneration
				r.townReturnPending = oldReturn
				b.PhaseCompleted = oldComplete
				b.TimedOut = oldTimeout
				b.RunDepartures = oldDepartures
			}()
			for _, peer := range p {
				r134N1Reset(peer)
			}
			r134N1Send(t, s, p[1], dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(p[0].selectedCharacterID, mode, value))
			for _, peer := range p {
				for _, packet := range splitAllGameServerUpperPackets(t, peer.conn.(*partyDungeonSyncConn).frames()) {
					if packet.Header.MsgID == 11 && packet.Header.Classification == 1 && bytes.Equal(packet.Body, []byte{0, partyCommandFailureGeneric}) {
						continue
					}
					if packet.Header.MsgID == 11 || packet.Header.MsgID == 8 || packet.Header.MsgID == 9 || packet.Header.MsgID == 153 {
						t.Fatalf("invalid owner produced party packet %d/%d", packet.Header.Classification, packet.Header.MsgID)
					}
				}
			}
		})
	}
	if !reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) || !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(b)) {
		t.Fatal("party/battle ownership mutated")
	}
	for i, peer := range p {
		inv, _, err := f.repos.Inventory.Load(context.Background(), fmt.Sprint(peer.selectedCharacterID))
		if err != nil || !reflect.DeepEqual(inv, beforeAssets[i]) || peer.dungeon.runtime != beforeRuns[i] {
			t.Fatal("runtime/assets changed", err)
		}
	}
	t.Run("active_existing_encounter_request_has_success_feedback", func(t *testing.T) {
		for _, peer := range p {
			r134N1Reset(peer)
		}
		body := make([]byte, 13)
		binary.LittleEndian.PutUint16(body, p[0].selectedCharacterID)
		r134N1Send(t, s, p[1], dnfenum.CmdPacketRequestPeer, body)
		replied := false
		for _, packet := range splitAllGameServerUpperPackets(t, p[1].conn.(*partyDungeonSyncConn).frames()) {
			if packet.Header.MsgID == 10 && packet.Header.Classification == 1 && bytes.Equal(packet.Body, []byte{1}) {
				replied = true
			}
		}
		if !replied {
			t.Fatal("already committed ready encounter request must restore its existing party, no new join")
		}
		if !reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) || !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(b)) {
			t.Fatal("active request altered party/battle")
		}
	})
	t.Logf("real area=%d dungeon=%d old_party=%d new_party=%d run=%s", area, p[0].dungeon.runtime.Dungeon.ID, old.PartyID, current.PartyID, p[0].dungeon.runtime.raidEntry.RunID)
}

func r134N1Reset(p *gameSession) {
	c := p.conn.(*partyDungeonSyncConn)
	c.mu.Lock()
	c.raw.Reset()
	c.mu.Unlock()
}
