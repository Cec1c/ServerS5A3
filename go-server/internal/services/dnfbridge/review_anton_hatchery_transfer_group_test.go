package dnfbridge

import (
	"context"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"testing"
)

func actualAntonHatcheryTransferParty(t *testing.T) (*Service, *gameSession, *gameSession, *runtimeDungeonState, *runtimeDungeonState, uint32) {
	t.Helper()
	s, leader, old, target := actualAntonHatchWarpOwner(t)
	_, peer, peerOld := newBackToVillageRuntime(t)
	peer.accountID = "account-1"
	peer.loginPacketSeen = true
	peer.accountFromToken = true
	peer.channel = leader.channel
	peer.residentChannel = leader.residentChannel
	repos, _ := s.repositoryGroup()
	character := old.Character
	character.CharacterID = "100"
	character.Level = 90
	if err := repos.Character.Save(context.Background(), character); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(peer, 100)
	old.raidEntry.Participants = []uint16{99, 100}
	copy := *old.raidEntry
	copy.Participants = append([]uint16(nil), old.raidEntry.Participants...)
	peerOld.Dungeon = old.Dungeon
	peerOld.Request = old.Request
	peerOld.Character = character
	peerOld.AccountID = old.AccountID
	peerOld.Session = old.Session
	peerOld.Room = old.Room
	peerOld.raidEntry = &copy
	peerOld.lifecycleToken = 1
	peer.dungeon.runToken = 1
	peerOld.townReturnOrigin.CharacterID = 100
	peer.dungeon.runtime = peerOld
	peer.postStartMapPlayerStateSent = true
	state := s.raids[old.raidEntry.RaidKey]
	state.Members = append(state.Members, runtimeRaidMemberState{CharID: 100, GroupIndex: 1})
	state.Battle.MemberAccounts[100] = "account-1"
	party := alignedcmd.PartyState{PartyID: 12, UserID: 99, Members: []alignedcmd.PartyMemberState{{UserID: 99}, {UserID: 100}}}
	storeRuntimePartyState(leader, party)
	storeRuntimePartyState(peer, party)
	return s, leader, peer, old, peerOld, target
}

func TestReviewActualAntonHatcheryTransferGroupCommitIsAtomic(t *testing.T) {
	for _, failPeer := range []bool{false, true} {
		name := "success"
		if failPeer {
			name = "late-peer-change"
		}
		t.Run(name, func(t *testing.T) {
			s, leader, peer, old, peerOld, target := actualAntonHatcheryTransferParty(t)
			req := dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2}
			r, scene, err := s.prepareDungeonRuntime(context.Background(), leader, req)
			if err != nil {
				t.Fatal(err)
			}
			followers, err := s.prepareRuntimePartyDungeonEntry(context.Background(), leader, s.runtimePartyStateForSession(leader), req, r, scene)
			if err != nil {
				t.Fatal(err)
			}
			if len(followers) != 1 {
				t.Fatal("frozen peer not prepared")
			}
			if err = s.freezeCurrentRaidDungeonGroupEntry(leader, r, followers); err != nil {
				t.Fatal(err)
			}
			if err = freezeCurrentDungeonTownReturnOrigin(leader, r); err != nil {
				t.Fatal(err)
			}
			beforeLeader, beforePeer := leader.dungeon.runToken, peer.dungeon.runToken
			if failPeer {
				peer.characterGeneration++
			}
			err = s.commitCurrentAntonHatcheryTransferGroup(leader, r, followers)
			if failPeer {
				if err == nil || leader.dungeon.runtime != old || peer.dungeon.runtime != peerOld || leader.dungeon.runToken != beforeLeader || peer.dungeon.runToken != beforePeer {
					t.Fatal("one peer failure partially retired energy group")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if leader.dungeon.runtime != r || peer.dungeon.runtime != followers[0].runtime || !followers[0].committed || r.raidEntry.RunID != followers[0].runtime.raidEntry.RunID || r.Seed != followers[0].runtime.Seed {
					t.Fatal("group did not share committed target")
				}
				if old.Session.Snapshot().Run.Status != worldmap.DungeonRunActive || peerOld.Session.Snapshot().Run.Status != worldmap.DungeonRunActive {
					t.Fatal("transfer invented clear or giveup")
				}
			}
		})
	}
}

func TestReviewActualAntonHatcheryTransferPublicPartyKeepsSharedOwner(t *testing.T) {
	s, leader, peer, old, peerOld, target := actualAntonHatcheryTransferParty(t)
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{DungeonID: target, Difficulty: 2})
	if err := s.handleDungeonSelectUpper(leader, body); err != nil {
		t.Fatal(err)
	}
	l, p := leader.dungeon.runtime, peer.dungeon.runtime
	if l == nil || p == nil || l == old || p == peerOld || l.Dungeon.ID != int64(target) || p.Dungeon.ID != int64(target) || l.raidEntry.RunID != p.raidEntry.RunID || l.Seed != p.Seed {
		t.Fatal("public transfer did not install entire frozen group")
	}
	if l.partyInstance == nil || l.partyInstance != p.partyInstance {
		t.Fatal("public transfer split shared target scene")
	}
	for _, session := range []*gameSession{leader, peer} {
		data := append([]byte(nil), session.conn.(*bufferConn).write.Bytes()...)
		count := 0
		for len(data) > 0 {
			packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
			data = rest
			if packet.Header.MsgID == currentDungeonStartNotification {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("committed char %d received %d start-map packets, wanted one", session.selectedCharacterID, count)
		}
	}
}
