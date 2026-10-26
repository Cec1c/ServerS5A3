package dnfbridge

import (
	"testing"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/dungeoncmd"
)

func TestR173StaleRoutedHatcherySelectDoesNotAckLeader(t *testing.T) {
	s, leader, peer, old, peerOld, target := actualAntonHatcheryTransferParty(t)
	body := encodeCurrentDungeonSelectRequest(dungeoncmd.SelectDungeonRequest{
		DungeonID:  target,
		Difficulty: 2,
	})

	// The follower's op16 was queued while both sessions still owned 218.
	token := s.bindPartyDungeonEntryRoutedPending(peer)
	if token == nil {
		t.Fatal("follower routed token was not bound")
	}

	// Another request commits the same 218 -> hatchery transition first.
	if err := s.handleDungeonSelectUpper(leader, body); err != nil {
		t.Fatal(err)
	}
	if leader.dungeon.runtime == old || peer.dungeon.runtime == peerOld {
		t.Fatal("leader request did not commit the whole group")
	}
	if leader.dungeon.runtime.Dungeon.ID != int64(target) || peer.dungeon.runtime.Dungeon.ID != int64(target) {
		t.Fatalf("committed ids leader=%d peer=%d want=%d",
			leader.dungeon.runtime.Dungeon.ID, peer.dungeon.runtime.Dungeon.ID, target)
	}

	leaderConn := leader.conn.(*bufferConn)
	peerConn := peer.conn.(*bufferConn)
	leaderConn.write.Reset()
	peerConn.write.Reset()

	// This is the already-queued follower request reaching the leader loop
	// after the group has moved to 221. It must not act on the new runtime.
	if err := s.handleDungeonSelectUpperPlanned(leader, body, nil, false, token); err != nil {
		t.Fatal(err)
	}
	leaderAcks := 0
	data := append([]byte(nil), leaderConn.write.Bytes()...)
	for len(data) > 0 {
		packet, rest := splitCurrentGameServerUpperPacketAuto(t, data)
		data = rest
		if packet.Header.Classification == 1 && packet.Header.MsgID == uint16(dnfenum.CmdPacketSelectDungeon) {
			leaderAcks++
		}
	}
	if leaderAcks != 0 {
		t.Fatalf("stale follower request replayed %d op16 ACKs to the leader", leaderAcks)
	}
	if peerConn.write.Len() != 0 {
		t.Fatalf("stale follower request wrote to the already-transitioned peer: %x", peerConn.write.Bytes())
	}
}

func TestR173RoutedSourceRetainsFreshRequests(t *testing.T) {
	f := newDoc104PartyFixture(t, 9, true)
	s, peer := f.service, f.follower
	old := s.bindPartyDungeonEntryRoutedPending(peer)
	if !s.routedPartyDungeonEntrySourceCurrent(old) {
		t.Fatal("fresh source request rejected")
	}
	fresh := s.bindPartyDungeonEntryRoutedPending(peer)
	if s.routedPartyDungeonEntrySourceCurrent(old) || !s.routedPartyDungeonEntrySourceCurrent(fresh) {
		t.Fatal("source check lost request sequence ownership")
	}
	if err := s.handleDungeonSelectUpperPlanned(f.leader, doc104SelectBody(), nil, true, old); err != nil {
		t.Fatal(err)
	}
	if !s.routedPartyDungeonEntrySourceCurrent(fresh) || len(doc104StepsFor(t, f, 100)) != 0 || len(doc104StepsFor(t, f, 99)) != 0 {
		t.Fatal("stale request wrote packets or consumed newer request")
	}
	peer.dungeon.mu.Lock()
	peer.dungeon.runToken++
	peer.dungeon.mu.Unlock()
	if s.routedPartyDungeonEntrySourceCurrent(fresh) {
		t.Fatal("source runtime change accepted")
	}
	// Successful commits still retire their pending token after runToken
	// advances; freshness at entry must not change the completion contract.
	if !s.discardPartyDungeonEntryRoutedRequest(fresh) {
		t.Fatal("completed request could not retire its token")
	}
	changed := s.bindPartyDungeonEntryRoutedPending(peer)
	s.bindGameSessionCharacter(peer, 101)
	if s.routedPartyDungeonEntrySourceCurrent(changed) {
		t.Fatal("old identity accepted after character switch")
	}
}
