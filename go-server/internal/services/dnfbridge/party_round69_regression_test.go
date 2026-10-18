package dnfbridge

import (
	"bytes"
	"net"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
)

func TestRound69ChangeHostKeepsLearnedRelayEndpoints(t *testing.T) {
	s, leader, member, _, _ := changeHostFixture(t, 10, 10)
	leader.conn = &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12001}}
	member.conn = &tcpAddrBufferConn{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12002}}
	relay, err := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	s.partyUDPRelay = relay
	state := runtimePartyStateSnapshot(leader)
	if !s.syncRuntimePartyUDPRelay(state) {
		t.Fatal("initial relay sync failed")
	}
	pid := uint16(state.PartyID)
	_, ab, ok := relay.Endpoint(pid, 1001, 1002)
	if !ok {
		t.Fatal("missing AB endpoint")
	}
	_, ba, ok := relay.Endpoint(pid, 1002, 1001)
	if !ok {
		t.Fatal("missing BA endpoint")
	}
	a, b := testCurrentPartyUDPClient(t, "127.0.0.1"), testCurrentPartyUDPClient(t, "127.0.0.1")
	defer a.Close()
	defer b.Close()
	abAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(ab)}
	baAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(ba)}
	buffer := make([]byte, 128)
	// Learn B before transfer; subsequent delivery must use that same learned leg.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, _ = b.WriteToUDP([]byte("ready"), baAddr)
		_, _ = a.WriteToUDP([]byte("before"), abAddr)
		_ = b.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := b.ReadFromUDP(buffer)
		if err == nil && bytes.Equal(buffer[:n], []byte("before")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial relay delivery failed")
		}
	}
	for _, step := range []struct {
		sender *gameSession
		slot   byte
	}{{leader, 1}, {member, 0}, {leader, 1}} {
		if err := s.handleOnlineChangePartyHost(step.sender, []byte{step.slot}); err != nil {
			t.Fatal(err)
		}
		if got := runtimePartyStateSnapshot(step.sender).PartyID; got != int(pid) {
			t.Fatalf("party ID changed to %d", got)
		}
		if _, port, ok := relay.Endpoint(pid, 1001, 1002); !ok || port != ab {
			t.Fatal("AB endpoint changed")
		}
		if _, port, ok := relay.Endpoint(pid, 1002, 1001); !ok || port != ba {
			t.Fatal("BA endpoint changed")
		}
		_, _ = a.WriteToUDP([]byte("after"), abAddr)
		_ = b.SetReadDeadline(time.Now().Add(time.Second))
		for {
			n, _, err := b.ReadFromUDP(buffer)
			if err != nil {
				t.Fatalf("learned endpoint lost after host transfer: %v", err)
			}
			if bytes.Equal(buffer[:n], []byte("after")) {
				break
			}
		}
	}
}

func TestRound69DelegatedMemberGiveupLeavesPartyAfterTownCommit(t *testing.T) {
	for _, drifted := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching_generation", true: "generation_drift"}[drifted], func(t *testing.T) {
			f := newLeaderExitFixture(t)
			manager := f.service.runtimePartyManagerForService()
			before, _ := manager.SnapshotByUser(99, 0)
			round69AlignInstancePartyID(t, f, before.ID)
			var ga, gb uint64
			for _, m := range before.Members {
				if m.UserID == 99 {
					ga = m.SessionGeneration
				}
				if m.UserID == 100 {
					gb = m.SessionGeneration
				}
			}
			result := manager.TransferLeader(99, ga, 100, gb)
			if !result.OK {
				t.Fatal(result.Reason)
			}
			f.service.publishRuntimePartySnapshot(result.Party)
			if drifted {
				f.service.mu.Lock()
				f.leader.characterGeneration += 100
				f.service.mu.Unlock()
				if _, found := manager.SnapshotByUser(99, f.leader.characterGeneration); found {
					t.Fatal("fixture did not create recorded generation drift")
				}
			}
			f.leaderRuntime.townReturnPending = true
			f.leaderRuntime.townReturnOp24Sent = true
			f.leaderRuntime.townReturnRequestMsgID = uint16(dnfenum.CmdPacketGiveupGame)
			_ = f.service.commitPendingDungeonReturnForSceneRequest(f.leader, "round69_member_giveup")
			if _, found := manager.SnapshotByUser(99, 0); found {
				t.Fatal("former leader retained party membership after giveup")
			}
			if left, found := manager.SnapshotByUser(100, 0); !found || left.Leader != 100 || len(left.Members) != 1 {
				t.Fatalf("new leader party changed: %+v", left)
			}
			if f.member.dungeon.runtime != f.memberRuntime || f.memberRuntime.partyInstance != f.instance {
				t.Fatal("surviving leader was pulled out of the dungeon")
			}
			if !assertOp9Kind3RemovalFor(splitUpperFrames(t, f.memberConn.write.Bytes()), 99) {
				t.Fatal("remaining dungeon peer did not receive actor removal")
			}
			if f.leader.dungeon.runtime != nil || runtimePartyStateSnapshot(f.leader).PartyID != 0 {
				t.Fatal("leaving member retained dungeon runtime or party projection")
			}
		})
	}
}

// The legacy fixture enters with cached party 12 before bootstrapping the
// manager. Align its registry key with the actual managed party for this guard.
func round69AlignInstancePartyID(t *testing.T, f leaderExitFixtureState, partyID uint16) {
	t.Helper()
	registry := f.service.partyDungeonInstanceRegistryForService()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	f.instance.mu.Lock()
	defer f.instance.mu.Unlock()
	if registry.byKey[f.instance.key] != f.instance {
		t.Fatal("fixture instance is not registered")
	}
	delete(registry.byKey, f.instance.key)
	f.instance.key.PartyID = partyID
	registry.byKey[f.instance.key] = f.instance
}

func TestRound69MemberGiveupRejectsStaleSocketAndDifferentParty(t *testing.T) {
	for _, scenario := range []string{"stale_socket", "different_party", "different_party_with_drift"} {
		t.Run(scenario, func(t *testing.T) {
			f := newLeaderExitFixture(t)
			manager := f.service.runtimePartyManagerForService()
			before, _ := manager.SnapshotByUser(100, 0)
			round69AlignInstancePartyID(t, f, before.ID)
			requester := f.member
			if scenario == "stale_socket" {
				// Retain the old socket's recorded generation in the manager,
				// but make a replacement socket the authoritative character owner.
				replacement := &gameSession{conn: &bufferConn{}, selectedCharacterID: 100,
					characterGeneration: f.member.characterGeneration + 100}
				f.service.mu.Lock()
				f.service.gameSessions[100] = replacement
				f.service.mu.Unlock()
			} else {
				var leader, member party.RuntimePartyMember
				for _, m := range before.Members {
					if m.UserID == 99 {
						leader = m
					} else if m.UserID == 100 {
						member = m
					}
				}
				created := manager.Create(leader, before.Settings)
				if !created.OK || created.Party.ID == before.ID {
					t.Fatal("new party not created")
				}
				joined := manager.Join(created.Party.ID, member)
				if !joined.OK {
					t.Fatal(joined.Reason)
				}
				f.service.publishRuntimePartySnapshot(joined.Party)
				if scenario == "different_party_with_drift" {
					f.service.mu.Lock()
					f.member.characterGeneration += 100
					f.service.mu.Unlock()
				}
			}
			want := manager.Snapshots()
			projection := runtimePartyStateSnapshot(requester)
			if err := f.service.leaveRuntimePartyOnMemberGiveup(requester, f.instance); err != nil {
				t.Fatal(err)
			}
			if got := manager.Snapshots(); !reflect.DeepEqual(got, want) {
				t.Fatalf("rejected exit changed memberships: got %+v want %+v", got, want)
			}
			if !reflect.DeepEqual(runtimePartyStateSnapshot(requester), projection) {
				t.Fatal("rejected exit changed party projection")
			}
			if f.leaderConn.write.Len() != 0 || f.memberConn.write.Len() != 0 {
				t.Fatal("rejected exit emitted removal frames")
			}
		})
	}
}
