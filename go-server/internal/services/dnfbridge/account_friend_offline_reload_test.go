package dnfbridge

import (
	"bytes"
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/accountfriend"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// Cold services and sessions intentionally share only the durable repository.
// A peer going offline must not make an incoming request or accepted roster
// depend on the old online-session directory or presentation cache.
func TestAccountFriendOfflineRequesterColdReloadAndAccept(t *testing.T) {
	s, a, b, _, bc := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	callAccountFriend(t, s, a, 1436, "Beta", "offline reload", 0)
	_, initial, err := s.currentAccountFriendProjection(context.Background(), "dnf:2")
	if err != nil || len(initial) != 1 || initial[0].Kind != 0 {
		t.Fatal("incoming request missing", initial, err)
	}
	s.unbindGameSession(a)
	cold := &Service{options: s.options, repositoryProvider: s.repositoryProvider}
	conn := &bufferConn{}
	reloaded := &gameSession{conn: conn, accountID: "dnf:2", loginPacketSeen: true, accountFromToken: true, selectedCharacterID: b.selectedCharacterID, channel: channelcatalog.Channel{ID: 42}}
	cold.bindGameSessionCharacter(reloaded, b.selectedCharacterID)
	if err := cold.sendCurrentAccountFriendLists(reloaded, "cold_reload"); err != nil {
		t.Fatal(err)
	}
	_, rest := splitGameServerUpperPacket(t, conn.write.Bytes())
	packet, rest := splitGameServerUpperPacket(t, rest)
	want, err := buildCurrentAccountFriendPendingBody(initial)
	if err != nil || packet.Header.MsgID != 1372 || !bytes.Equal(packet.Body, want) || len(rest) != 0 {
		t.Fatalf("offline incoming changed: %x want %x err %v", packet.Body, want, err)
	}
	conn.write.Reset()
	callAccountFriend(t, cold, reloaded, 1437, "Alpha", "", 0)
	ack, _ := splitGameServerUpperPacket(t, conn.write.Bytes())
	if ack.Header.MsgID != 1437 || ack.Body[0] != 1 {
		t.Fatal("offline requester acceptance failed", ack)
	}
	entries, err := accountfriend.NewOwner(g).List(nil, "dnf:1")
	if err != nil || len(entries) != 1 || entries[0].Relation.State != accountfriend.Friend {
		t.Fatal("accept not reciprocal/durable", entries, err)
	}
	cold.unbindGameSession(reloaded)
	second := &Service{options: s.options, repositoryProvider: s.repositoryProvider}
	rows, pending, err := second.currentAccountFriendProjection(context.Background(), "dnf:1")
	if err != nil || len(rows) != 1 || len(pending) != 0 {
		t.Fatal("accepted account roster lost after both peers offline", rows, pending, err)
	}
	if rows[0].CharacterName != "来来来" || rows[0].Channel != 0 {
		t.Fatalf("cold offline row must fall back to the persisted friend character: %+v", rows)
	}
	if bc.write.Len() == 0 {
		t.Fatal("original request did not deliver recipient projection")
	}
}
