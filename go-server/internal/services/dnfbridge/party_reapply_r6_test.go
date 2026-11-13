package dnfbridge

import (
	"encoding/binary"
	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/party"
	"testing"
)

func TestPartyReapplyRoutesClickedMemberToCurrentLeader(t *testing.T) {
	s, applicant, leader, applicantConn, leadConn := newCurrentSocialPeerTest(t)
	s.options.currentClientVersion = "official-24.9"
	memberConn := &bufferConn{}
	member := &gameSession{conn: memberConn, channel: leader.channel, accountID: "dnf:3", selectedCharacterID: 6}
	s.bindGameSessionCharacter(member, 6)
	s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 6, TownID: 1, AreaID: 1, Session: member})
	m := s.runtimePartyManagerForService()
	created := m.Create(party.RuntimePartyMember{UserID: 5, SessionGeneration: leader.characterGeneration, State: alignedcmd.PartyMemberState{UserID: 5}}, alignedcmd.PartyState{MaxMembers: 4})
	if !created.OK {
		t.Fatal(created.Reason)
	}
	if r := m.Join(created.Party.ID, party.RuntimePartyMember{UserID: 6, SessionGeneration: member.characterGeneration, State: alignedcmd.PartyMemberState{UserID: 6}}); !r.OK {
		t.Fatal(r.Reason)
	}
	// Native ordinary-party request is 13 bytes (trade/PVP use 11).
	body := make([]byte, 13)
	binary.LittleEndian.PutUint16(body, 6)
	for i := 0; i < 2; i++ {
		leadConn.write.Reset()
		if e := s.handleOnlineRequestPeer(applicant, 10, body); e != nil {
			t.Fatal(e)
		}
		if leadConn.write.Len() == 0 {
			source, sourceOK := s.boundGameSessionCharacterSnapshot(applicant)
			target, targetOK := s.onlineGameSessionCharacterSnapshot(6)
			lp, lpOK := m.SnapshotByUser(5, leader.characterGeneration)
			t.Fatalf("iteration=%d source=%+v/%v target=%+v/%v party=%+v/%v sameArea=%v/%v applicantReply=%x", i, source, sourceOK, target, targetOK, lp, lpOK, s.onlinePlayers.PeerInSameArea(1, 6), s.onlinePlayers.PeerInSameArea(1, 5), applicantConn.write.Bytes())
		}
		notice, rest := splitGameServerUpperPacket(t, leadConn.write.Bytes())
		if notice.Header.MsgID != 7 || notice.Header.Classification != 0 || len(rest) != 0 || memberConn.write.Len() != 0 {
			t.Fatal("request did not reach leader only")
		}
		r, ok := m.ConsumePeerRequest(5, leader.characterGeneration, 1, applicant.characterGeneration, 0)
		if !ok || r.AnchorID != 5 || r.JoinerID != 1 || r.PartyID != created.Party.ID {
			t.Fatalf("bad consent direction: %+v %v", r, ok)
		}
	}
}

func TestPartyReapplyPreservesDirectedPortsAcrossMemberTimeout(t *testing.T) {
	relay, e := newCurrentPartyUDPRelay(true, "127.0.0.1", testCurrentPartyUDPRelayPortStart(t), 12, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer relay.Close()
	b := []currentPartyUDPRelayBinding{{characterID: 2, generation: 1, address: [4]byte{127, 0, 0, 1}}, {characterID: 3, generation: 1, address: [4]byte{127, 0, 0, 1}}, {characterID: 4, generation: 1, address: [4]byte{127, 0, 0, 1}}}
	// Live ordering: 3+2 first, then 4; 2 times out, then reapplies.
	if !relay.Sync(2, b[:2]) || !relay.Sync(2, b) {
		t.Fatal("initial formation")
	}
	before := map[[2]uint16]uint16{}
	for _, a := range b {
		for _, z := range b {
			if a.characterID != z.characterID {
				_, port, ok := relay.Endpoint(2, a.characterID, z.characterID)
				if !ok {
					t.Fatal("missing port")
				}
				before[[2]uint16{a.characterID, z.characterID}] = port
			}
		}
	}
	for round := 0; round < 3; round++ {
		if !relay.Sync(2, b[1:]) {
			t.Fatal("timeout leave")
		}
		if _, _, ok := relay.Endpoint(2, 2, 3); ok {
			t.Fatal("departed member still routable")
		}
		if !relay.Sync(2, b) {
			t.Fatal("rejoin")
		}
		for pair, old := range before {
			_, got, ok := relay.Endpoint(2, pair[0], pair[1])
			if !ok || got != old {
				t.Fatalf("round%d pair%v port changed %d -> %d", round, pair, old, got)
			}
		}
	}
	if len(relay.portOwners) > relay.portCount {
		t.Fatal("unbounded port history")
	}
	relay.CloseParty(2)
	if len(relay.portOwners) != 0 {
		t.Fatal("retired party retained history")
	}
}
