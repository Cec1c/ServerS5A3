package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// D33 P57 (new rule only): one friend adventure group is exactly ONE top-level
// row. This file encodes the target contract and nothing else, so running it
// against the unmodified production projection must FAIL (it used to emit one
// row per online character).
func TestAccountFriendGroupRowRuleSingleRowPerAccount(t *testing.T) {
	s, a, b, _, _ := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	callAccountFriend(t, s, a, 1436, "Beta", "申请", 0)
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)

	// Only 来来来 (id 5) of dnf:2 is online.
	rows, _, err := s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "来来来" {
		t.Fatalf("baseline single online character: rows=%+v err=%v", rows, err)
	}

	// 小号 (id 8) of the SAME friend account comes online: the group must stay one
	// row. The old projection emitted a second top-level row here.
	second := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", selectedCharacterID: 8, loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 7}}
	s.bindGameSessionCharacter(second, 8)
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil {
		t.Fatalf("two online characters: err=%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("one adventure group must be one top-level row: rows=%+v", rows)
	}
	if rows[0].AdventureName != "Beta" {
		t.Fatalf("row must carry the friend adventure name: %+v", rows[0])
	}

	// The durable representative is OFFLINE while another character of the same
	// account is online: the single row must be the online candidate, not the
	// offline durable one.
	setAccountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey, "8")
	s.unbindGameSession(second)
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "来来来" || rows[0].Channel != 42 {
		t.Fatalf("offline durable representative must fall back to the online candidate: rows=%+v err=%v", rows, err)
	}

	// All characters of the friend account are offline: still one row, from the
	// durable identity, at channel 0.
	s.unbindGameSession(b)
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "小号" || rows[0].Channel != 0 {
		t.Fatalf("all offline: rows=%+v err=%v", rows, err)
	}
}
