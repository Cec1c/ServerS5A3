package dnfbridge

import (
	"context"
	"encoding/json"
	"testing"

	"longheng.io/server/internal/modules/dnf/accountfriend"
	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

// D33 feedback 10: the adventure-group friend list is a presence projection.
// It lists only the friend characters that are online; an offline friend falls
// back to the single character the accepted relation durably records. It must
// never enumerate the whole account roster and never pick an arbitrary
// "first" character.
func TestAccountFriendProjectionListsOnlyOnlineOrPersistedCharacter(t *testing.T) {
	s, a, b, _, _ := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	ctx := context.Background()
	callAccountFriend(t, s, a, 1436, "Beta", "申请", 0)
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)

	// dnf:2 has two characters: 来来来 (online, channel 42) and 小号 (offline).
	// Only the online one may appear; the offline roster must not be enumerated.
	rows, _, err := s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "来来来" || rows[0].Channel != 42 {
		t.Fatalf("one online character: rows=%+v err=%v", rows, err)
	}

	// A second online character of the same friend account must NOT become a
	// second top-level row: one friend adventure group is exactly one row. With
	// no durable last-selected character yet the deterministic rule picks the
	// lowest character id of the online set (来来来, id 5).
	second := &gameSession{conn: &bufferConn{}, accountID: "dnf:2", selectedCharacterID: 8, loginPacketSeen: true, accountFromToken: true, channel: channelcatalog.Channel{ID: 7}}
	s.bindGameSessionCharacter(second, 8)
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "来来来" || rows[0].Channel != 42 || rows[0].AdventureName != "Beta" {
		t.Fatalf("one row per adventure group, lowest-id representative: rows=%+v err=%v", rows, err)
	}

	// The durable last-selected character wins while it is online, so the single
	// row follows the friend's actual current character instead of adding a row.
	setAccountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey, "8")
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "小号" || rows[0].Channel != 7 {
		t.Fatalf("representative follows the last-selected online character: rows=%+v err=%v", rows, err)
	}
	setAccountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey, "")

	// Once every character is offline only the persisted accept-time character
	// remains, at channel 0.
	s.unbindGameSession(second)
	s.unbindGameSession(b)
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "来来来" || rows[0].Channel != 0 {
		t.Fatalf("offline fallback: rows=%+v err=%v", rows, err)
	}

	// The durable last-online record takes precedence over the accept-time
	// relation character: the friend most recently selected 小号, so the offline
	// row must become 小号 rather than 来来来.
	setAccountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey, "8")
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 1 || rows[0].CharacterName != "小号" || rows[0].Channel != 0 {
		t.Fatalf("last-online record precedence: rows=%+v err=%v", rows, err)
	}

	// Remove both durable identities. With no online character and no persisted
	// character there is no evidence for an offline row, so it must be omitted
	// rather than defaulting to a roster entry.
	setAccountMetadataValue(t, g, "dnf:2", lastOnlineCharacterMetadataKey, "")
	account, found, err := g.Account.Load(ctx, "dnf:1")
	if err != nil || !found {
		t.Fatalf("load account: found=%v err=%v", found, err)
	}
	const relationKey = "account_friend_v1:dnf:2"
	var relation accountfriend.Relation
	if err := json.Unmarshal([]byte(account.Metadata[relationKey]), &relation); err != nil {
		t.Fatalf("decode relation: %v", err)
	}
	if relation.PeerCharacterID == "" {
		t.Fatal("fixture must persist the peer character on accept")
	}
	relation.PeerCharacterID = ""
	encoded, err := json.Marshal(relation)
	if err != nil {
		t.Fatal(err)
	}
	account.Metadata[relationKey] = string(encoded)
	if err := g.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	rows, _, err = s.currentAccountFriendProjection(ctx, "dnf:1")
	if err != nil || len(rows) != 0 {
		t.Fatalf("offline friend without persisted character must not fabricate a row: rows=%+v err=%v", rows, err)
	}
}
