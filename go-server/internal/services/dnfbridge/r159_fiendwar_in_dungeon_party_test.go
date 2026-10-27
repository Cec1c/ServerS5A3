package dnfbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"reflect"
	"sort"
	"testing"
)

// Real resource entry creates the encounter party; no town invitation is sent.
func r159ActiveEncounter(t *testing.T) (*raidStartGuardFixture, []*gameSession) {
	return r159ActiveEncounterAt(t, 0)
}

func r159ActiveEncounterAt(t *testing.T, selectedArea int) (*raidStartGuardFixture, []*gameSession) {
	t.Helper()
	f, p := r134N1Fixture(t)
	s, b := f.service, f.state.Battle
	if selectedArea == 30 || selectedArea == 31 {
		// Controlled already-unlocked raid phase, as in the N2 entry fixture.
		// Entry, shared membership and scene loading still use live handlers.
		b.FiendWar.Symbols[b.FiendWar.Config.Symbols["top boss state"]] = 1
		b.FiendWar.Symbols[b.FiendWar.Config.Symbols["bot boss state"]] = 1
		syncCurrentFiendWarDungeons(b)
	}
	area := 0
	for id, a := range b.FiendWar.Config.Areas {
		if selectedArea != 0 && selectedArea != id {
			continue
		}
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
	t.Cleanup(func() { partyDungeonLoadingBarrierDispatch = restoreDispatch })
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
	t.Logf("ready real encounter area=%d dungeon=%d key=%+v run=%s participants=%v", area, p[0].dungeon.runtime.Dungeon.ID, p[0].dungeonPartyRun, p[0].dungeon.runtime.raidEntry.RunID, p[0].dungeon.runtime.raidEntry.Participants)

	return f, p
}

// This is a separate scene/run admission boundary, not a claim that rejecting
// every in-dungeon request meets D03. It records the missing transfer contract.
func TestR159FiendWarSeparateActiveRuns(t *testing.T) {
	f, p := r134N1Fixture(t)
	s, b := f.service, f.state.Battle
	areas := []int{}
	for id, a := range b.FiendWar.Config.Areas {
		if a.Dungeon > 0 && b.FiendWar.AreaOpen(id) {
			areas = append(areas, id)
		}
	}
	sort.Ints(areas)
	if len(areas) < 2 {
		t.Fatal("real config lacks two available independent encounters")
	}
	for i, peer := range p {
		r134N1Presence(t, f, peer, areas[i])
	}
	for _, peer := range p {
		if err := s.tryCurrentFiendWarEncounter(peer); err != nil {
			t.Fatal(err)
		}
		if peer.dungeon.runtime == nil || peer.dungeon.runtime.raidEntry == nil {
			t.Fatal("real single entry failed", f.logText(t))
		}
		if err := s.handleLegacyGamePacket(peer, buildCurrentDungeonClientUpperForPhaseTest(t, uint16(dnfenum.CmdPacketFinishLoading), make([]byte, currentFinishLoadingLegacyRequestBodySize))); err != nil {
			t.Fatal(err)
		}
	}
	r0, r1 := p[0].dungeon.runtime, p[1].dungeon.runtime
	if r0.raidEntry.RunID == r1.raidEntry.RunID || r0.Dungeon.ID == r1.Dungeon.ID {
		t.Fatal("fixture is not two independent runs")
	}
	t.Logf("real independently entered: area=%d dungeon=%d run=%s participants=%v / area=%d dungeon=%d run=%s participants=%v", areas[0], r0.Dungeon.ID, r0.raidEntry.RunID, r0.raidEntry.Participants, areas[1], r1.Dungeon.ID, r1.raidEntry.RunID, r1.raidEntry.Participants)
	before := cloneRuntimeRaidBattle(b)
	for _, peer := range p {
		r134N1Reset(peer)
	}
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, p[1].selectedCharacterID)
	r134N1Send(t, s, p[0], dnfenum.CmdPacketRequestPeer, body)
	packets := splitAllGameServerUpperPackets(t, p[0].conn.(*partyDungeonSyncConn).frames())
	found := false
	for _, packet := range packets {
		if packet.Header.Classification == 1 && packet.Header.MsgID == 10 {
			found = bytes.Equal(packet.Body, []byte{0, 3, 0})
		}
	}
	if !found || !reflect.DeepEqual(before, cloneRuntimeRaidBattle(b)) || p[0].dungeon.runtime != r0 || p[1].dungeon.runtime != r1 {
		t.Fatal("independent encounter changed without admission/scene transfer")
	}
}

func TestR159FiendWarRestoreWriteFailure(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	s := f.service
	before := cloneRuntimeRaidBattle(f.state.Battle)
	beforeParty := s.runtimePartyStateForSession(p[0])
	for _, at := range []int{1, 2} {
		t.Run(fmt.Sprintf("write_%d", at), func(t *testing.T) {
			original := p[0].conn
			defer func() { p[0].conn = original }()
			fault := errors.New("injected request/roster failure")
			broken := &failNthDungeonWriteConn{failAt: at, err: fault}
			p[0].conn = broken
			body := make([]byte, 13)
			binary.LittleEndian.PutUint16(body, p[1].selectedCharacterID)
			err := s.handleGameUpper(p[0], buildCurrentDungeonClientUpperForPhaseTest(t, 10, body))
			if broken.writes < at {
				t.Fatalf("fault not exercised: writes=%d target=%d", broken.writes, at)
			}
			if at == 1 && !errors.Is(err, fault) {
				t.Fatalf("ACK failure=%v", err)
			}
			if !reflect.DeepEqual(before, cloneRuntimeRaidBattle(f.state.Battle)) || !reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) {
				t.Fatal("write failure changed membership/rewards")
			}
			p[0].conn = original
			for _, peer := range p {
				r134N1Reset(peer)
			}
			r134N1Send(t, s, p[0], dnfenum.CmdPacketRequestPeer, body)
			for _, peer := range p {
				roster := 0
				ack := 0
				for _, packet := range splitAllGameServerUpperPackets(t, peer.conn.(*partyDungeonSyncConn).frames()) {
					if packet.Header.Classification == 1 && packet.Header.MsgID == 10 && bytes.Equal(packet.Body, []byte{1}) {
						ack++
					}
					if packet.Header.Classification == 0 && packet.Header.MsgID == 153 {
						roster++
					}
					if packet.Header.Classification == 0 && (packet.Header.MsgID == 7 || packet.Header.MsgID == 8 || packet.Header.MsgID == 11) {
						t.Fatal("retry restarted handshake")
					}
				}
				if roster != 1 {
					t.Fatalf("retry roster=%d", roster)
				}
				if peer == p[0] && ack != 1 {
					t.Fatalf("retry success ACK=%d", ack)
				}
			}
		})
	}
}
func TestR159FiendWarActiveInvite(t *testing.T) {
	for _, area := range []int{0, 30, 31} {
		t.Run(fmt.Sprintf("area_%d", area), func(t *testing.T) {
			for _, mode := range []byte{0, 12} {
				t.Run(fmt.Sprintf("existing_encounter_mode_%d", mode), func(t *testing.T) {
					f, p := r159ActiveEncounterAt(t, area)
					s := f.service
					beforeBattle := cloneRuntimeRaidBattle(f.state.Battle)
					beforeAssets := r159Assets(t, f, p)
					beforeParty := s.runtimePartyStateForSession(p[0])
					runs := []*runtimeDungeonState{p[0].dungeon.runtime, p[1].dungeon.runtime}
					keys := []dungeonPartyRunKey{p[0].dungeonPartyRun, p[1].dungeonPartyRun}
					for attempt := 0; attempt < 2; attempt++ {
						for _, peer := range p {
							r134N1Reset(peer)
						}
						body := make([]byte, 13)
						binary.LittleEndian.PutUint16(body, p[1].selectedCharacterID)
						body[2] = mode
						r134N1Send(t, s, p[0], dnfenum.CmdPacketRequestPeer, body)
						ack := 0
						for i, peer := range p {
							roster, rows := 0, 0
							for _, packet := range splitAllGameServerUpperPackets(t, peer.conn.(*partyDungeonSyncConn).frames()) {
								if packet.Header.Classification == 1 && packet.Header.MsgID == 10 {
									if !bytes.Equal(packet.Body, []byte{1}) {
										t.Fatalf("same committed encounter rejected: %x", packet.Body)
									}
									ack++
								}
								if packet.Header.Classification == 0 {
									switch packet.Header.MsgID {
									case 7, 8, 11:
										t.Fatalf("existing link restarted by op%d", packet.Header.MsgID)
									case 153:
										roster++
									case 9:
										rows++
									}
								}
							}
							if roster != 1 || rows != 2 {
								t.Fatalf("peer%d roster=%d rows=%d", i, roster, rows)
							}
							if peer.dungeon.runtime != runs[i] || peer.dungeonPartyRun != keys[i] {
								t.Fatal("run/scene changed")
							}
						}
						if ack != 1 {
							t.Fatalf("ACK=%d", ack)
						}
						if !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(f.state.Battle)) || !reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) {
							t.Fatal("membership/reward owner changed")
						}
					}
					if !bytes.Equal(beforeAssets, r159Assets(t, f, p)) {
						t.Fatal("restoration changed persisted character/inventory")
					}
					// A following mode0/12 response remains an acknowledgement, not a fresh join.
					r134N1Send(t, s, p[1], dnfenum.CmdPacketResponsePeer, buildResponsePeerBody(p[0].selectedCharacterID, mode, 0))
					if !reflect.DeepEqual(beforeBattle, cloneRuntimeRaidBattle(f.state.Battle)) {
						t.Fatal("response changed battle")
					}
				})
			}
		})
	}
}

func r159Assets(t *testing.T, f *raidStartGuardFixture, p []*gameSession) []byte {
	t.Helper()
	rows := []any{}
	for _, peer := range p {
		id := fmt.Sprint(peer.selectedCharacterID)
		c, found, err := f.repos.Character.Load(context.Background(), id)
		if err != nil || !found {
			t.Fatal("character snapshot", err)
		}
		i, _, err := f.repos.Inventory.Load(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, c, i)
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestR159FiendWarActiveInviteGuards(t *testing.T) {
	f, p := r159ActiveEncounter(t)
	s, b := f.service, f.state.Battle
	for _, name := range []string{"different_run", "different_scene", "not_ready", "departed", "reward_frozen", "wrong_account", "different_party_instance"} {
		t.Run(name, func(t *testing.T) {
			r := p[1].dungeon.runtime
			entry := *r.raidEntry
			oldKey, oldValid := p[1].dungeonPartyRun, p[1].dungeonPartyRunValid
			oldAccount, oldInstance := r.AccountID, r.partyInstance
			oldComplete, oldDepartures := b.PhaseCompleted, b.RunDepartures
			defer func() {
				*r.raidEntry = entry
				p[1].dungeonPartyRun = oldKey
				p[1].dungeonPartyRunValid = oldValid
				r.AccountID = oldAccount
				r.partyInstance = oldInstance
				b.PhaseCompleted = oldComplete
				b.RunDepartures = oldDepartures
			}()
			switch name {
			case "different_run":
				r.raidEntry.RunID += "-other"
			case "different_scene":
				p[1].dungeonPartyRun.MapID++
			case "not_ready":
				p[1].dungeonPartyRunValid = false
			case "departed":
				b.RunDepartures = map[string]map[uint16]bool{entry.RunID: {p[1].selectedCharacterID: true}}
			case "reward_frozen":
				b.PhaseCompleted = true
			case "wrong_account":
				r.AccountID = "different"
			case "different_party_instance":
				r.partyInstance = &dungeonPartyInstance{}
			}
			before := cloneRuntimeRaidBattle(b)
			beforeParty := s.runtimePartyStateForSession(p[0])
			for _, peer := range p {
				r134N1Reset(peer)
			}
			body := make([]byte, 13)
			binary.LittleEndian.PutUint16(body, p[1].selectedCharacterID)
			r134N1Send(t, s, p[0], dnfenum.CmdPacketRequestPeer, body)
			found := false
			for _, packet := range splitAllGameServerUpperPackets(t, p[0].conn.(*partyDungeonSyncConn).frames()) {
				if packet.Header.MsgID == 10 && packet.Header.Classification == 1 {
					found = bytes.Equal(packet.Body, []byte{0, 3, 0})
				}
			}
			if !found {
				t.Fatal("invalid owner was acknowledged")
			}
			if !reflect.DeepEqual(before, cloneRuntimeRaidBattle(b)) || !reflect.DeepEqual(beforeParty, s.runtimePartyStateForSession(p[0])) {
				t.Fatal("rejection mutated owner")
			}
		})
	}
}
