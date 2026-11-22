package dnfbridge

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestLearnedPartyMemberStallRecoveryIsBoundedAndTargeted(t *testing.T) {
	for _, members := range []int{2, 3} {
		for _, mode := range []string{"recover", "resumed", "scene_changed", "rejoined", "shutdown", "few_packets", "burst_then_quiet", "short_pause"} {
			t.Run(fmt.Sprintf("%d/%s", members, mode), func(t *testing.T) {
				r, clients, bindings := partyUDPRelayFullMeshFixture(t, members)
				r.stallTiming = partyUDPRelayStallTiming{idle: 5 * time.Second, confirm: 400 * time.Millisecond, poll: 5 * time.Millisecond}
				var requests atomic.Int32
				var stable atomic.Bool
				stable.Store(true)
				prepared := make(chan struct{}, 1)
				r.prepareStalledPeer = func(partyID uint16, binding currentPartyUDPRelayBinding) *partyUDPRelayRecoveryPlan {
					if partyID != partyUDPRelayFullMeshPartyID || binding != bindings[0] {
						t.Errorf("healthy member selected for recovery: %d/%+v", partyID, binding)
					}
					select {
					case prepared <- struct{}{}:
					default:
					}
					return &partyUDPRelayRecoveryPlan{current: stable.Load, deliver: func(current func() bool) bool {
						if !current() {
							return false
						}
						requests.Add(1)
						return true
					}}
				}
				if !r.Sync(partyUDPRelayFullMeshPartyID, bindings) {
					t.Fatal("sync failed")
				}
				endpoints := partyUDPRelayFullMeshLearnAll(t, r, clients)
				r.mu.Lock()
				for key, leg := range r.legs {
					if key.ownerID == bindings[0].characterID && mode != "short_pause" {
						leg.mu.Lock()
						leg.lastReceivedAt = time.Now().Add(-6 * time.Second)
						leg.mu.Unlock()
					}
				}
				r.mu.Unlock()
				started, changed := false, false
				iterations := 0
				until := time.Now().Add(850 * time.Millisecond)
				for time.Now().Before(until) {
					iterations++
					// Only member 1 stops. All other directed paths keep moving,
					// including the healthy 2<->3 pair in the three-member case.
					for _, sender := range clients[1:] {
						if (mode == "few_packets" && iterations > 8) || (mode == "burst_then_quiet" && iterations > 80) {
							continue
						}
						for _, receiver := range clients {
							if sender.characterID == receiver.characterID {
								continue
							}
							_, err := sender.conn.WriteToUDP([]byte("ongoing-peer-payload"), endpoints[partyUDPRelayFullMeshDirection{sender.characterID, receiver.characterID}])
							if err != nil {
								t.Fatal(err)
							}
						}
					}
					select {
					case <-prepared:
						started = true
					default:
					}
					if started && !changed {
						changed = true
						switch mode {
						case "resumed":
							_, err := clients[0].conn.WriteToUDP([]byte("member-resumed"), endpoints[partyUDPRelayFullMeshDirection{clients[0].characterID, clients[1].characterID}])
							if err != nil {
								t.Fatal(err)
							}
						case "scene_changed":
							stable.Store(false)
						case "shutdown":
							if err := r.Close(); err != nil {
								t.Fatal(err)
							}
						case "rejoined":
							r.CloseParty(partyUDPRelayFullMeshPartyID)
							if !r.Sync(partyUDPRelayFullMeshPartyID, bindings) {
								t.Fatal("resync failed")
							}
						}
					}
					time.Sleep(2 * time.Millisecond)
				}
				if !started && mode != "short_pause" {
					t.Fatal("established but stalled member was never observed")
				}
				if started && mode == "short_pause" {
					t.Fatal("brief pause started recovery")
				}
				want := int32(0)
				if mode == "recover" {
					want = 1
				}
				if got := requests.Load(); got != want {
					t.Fatalf("recovery requests=%d want %d", got, want)
				}
				if mode == "recover" && members == 3 {
					r.mu.Lock()
					for key, leg := range r.legs {
						if key.ownerID != clients[0].characterID && key.peerID != clients[0].characterID {
							leg.mu.Lock()
							forwarded, used := leg.forwarded, leg.stallRecoveryUsed
							leg.mu.Unlock()
							if forwarded < 32 || used {
								t.Errorf("healthy pair disturbed: forwarded=%d recovery=%v", forwarded, used)
							}
						}
					}
					r.mu.Unlock()
				}
			})
		}
	}
}

func newStalledRecoveryPlanFixture(t *testing.T) (completionReturnFixture, currentPartyUDPRelayBinding, uint16) {
	t.Helper()
	f := newCompletionReturnPartyFixture(t)
	f.service.options.currentClientVersion = "official-24.9"
	f.service.options.partyUDPRelaySilentPeerKick = true
	for _, p := range []*gameSession{f.leader, f.member} {
		p.setFinishLoadingGates(finishLoadingGates{State: true, Completion: true, PostFinish: true})
		scene, ok := p.dungeon.runtime.Session.Scene()
		if !ok {
			t.Fatal("no scene")
		}
		f.service.recordDungeonPartyRunKey(p, p.dungeon.runtime, scene)
	}
	identity, _ := f.service.boundGameSessionCharacterSnapshot(f.leader)
	state, _ := f.service.runtimePartyManagerForService().SnapshotByUser(identity.character, identity.generation)
	return f, currentPartyUDPRelayBinding{characterID: identity.character, generation: identity.generation}, state.ID
}

func TestStalledRecoverySendsOnlyAffectedMembersEndpointTable(t *testing.T) {
	f, binding, partyID := newStalledRecoveryPlanFixture(t)
	plan := f.service.prepareStalledPartyMemberRecovery(partyID, binding)
	if plan == nil || !plan.current() {
		t.Fatal("active same-room party not eligible")
	}
	f.leaderConn.write.Reset()
	f.memberConn.write.Reset()
	if !plan.deliver(func() bool { return true }) {
		t.Fatal("endpoint request not sent")
	}
	frames := splitUpperFrames(t, f.leaderConn.write.Bytes())
	if len(frames) != 1 || frames[0].msgID != 11 {
		t.Fatalf("recovery changed more than endpoint table: %+v", frames)
	}
	if f.memberConn.write.Len() != 0 || f.leader.dungeon.runtime != f.leaderRuntime || f.member.dungeon.runtime != f.memberRuntime {
		t.Fatal("recovery disturbed another member or scene")
	}
}

func TestStalledRecoveryRejectsInvalidAndChangedScenes(t *testing.T) {
	for _, mode := range []string{"loading", "same_room_reload", "completed", "returning", "generation", "traffic_resumed"} {
		t.Run(mode, func(t *testing.T) {
			f, binding, partyID := newStalledRecoveryPlanFixture(t)
			plan := f.service.prepareStalledPartyMemberRecovery(partyID, binding)
			if plan == nil {
				t.Fatal("missing initial recovery plan")
			}
			still := func() bool { return true }
			switch mode {
			case "loading":
				f.member.setFinishLoadingGatePostFinish(false)
			case "same_room_reload":
				f.member.setFinishLoadingGates(finishLoadingGates{})
				f.member.setFinishLoadingGates(finishLoadingGates{true, true, true})
			case "completed":
				if err := f.leaderRuntime.Session.CompleteCurrentRoom(); err != nil {
					t.Fatal(err)
				}
			case "returning":
				f.leaderRuntime.townReturnPending = true
			case "generation":
				f.service.mu.Lock()
				f.leader.characterGeneration++
				f.service.mu.Unlock()
			case "traffic_resumed":
				still = func() bool { return false }
			}
			f.leaderConn.write.Reset()
			f.memberConn.write.Reset()
			if plan.deliver(still) || f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
				t.Fatal("obsolete recovery wrote packets")
			}
		})
	}
}

func TestStalledRecoveryRechecksSceneAtWireBoundary(t *testing.T) {
	f, binding, partyID := newStalledRecoveryPlanFixture(t)
	plan := f.service.prepareStalledPartyMemberRecovery(partyID, binding)
	if plan == nil {
		t.Fatal("missing initial plan")
	}
	checked, resume, done := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	var first atomic.Bool
	go func() {
		done <- plan.deliver(func() bool {
			if first.CompareAndSwap(false, true) {
				close(checked)
				<-resume
			}
			return true
		})
	}()
	select {
	case <-checked:
	case <-time.After(time.Second):
		close(resume)
		t.Fatal("preflight not reached")
	}
	f.member.setFinishLoadingGatePostFinish(false)
	f.member.setFinishLoadingGatePostFinish(true)
	close(resume)
	select {
	case sent := <-done:
		if sent || f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
			t.Fatal("changed loading lifetime escaped final guard")
		}
	case <-time.After(time.Second):
		t.Fatal("recovery delivery did not finish")
	}
}

func TestStalledRecoveryRespectsScopeSwitchAndPVP(t *testing.T) {
	f, binding, partyID := newStalledRecoveryPlanFixture(t)
	f.service.options.partyUDPRelaySilentPeerKick = false
	if f.service.prepareStalledPartyMemberRecovery(partyID, binding) != nil {
		t.Fatal("disabled recovery prepared")
	}
	f.service.options.partyUDPRelaySilentPeerKick = true
	f.member.setFinishLoadingGateState(false)
	if f.service.prepareStalledPartyMemberRecovery(partyID, binding) != nil {
		t.Fatal("loading peer prepared")
	}
	r := &currentPartyUDPRelay{prepareStalledPeer: func(uint16, currentPartyUDPRelayBinding) *partyUDPRelayRecoveryPlan {
		t.Error("PVP recovery callback")
		return nil
	}}
	r.watchStalledPartyMember(&currentPartyUDPRelayLeg{key: currentPartyUDPRelayLegKey{pvp: true}}, &currentPartyUDPRelayLeg{key: currentPartyUDPRelayLegKey{pvp: true}})
}
