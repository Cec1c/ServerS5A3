package dnfbridge

import (
	"context"
	"errors"
	"testing"

	"longheng.io/server/internal/modules/dnf/accountfriend"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func prepareUnchangedAccountFriendTest(t *testing.T) (*Service, *gameSession, *gameSession, *bufferConn) {
	t.Helper()
	s, a, b, ac, _ := prepareAccountFriendTest(t)
	callAccountFriend(t, s, a, 1436, "Beta", "request", 0)
	callAccountFriend(t, s, b, 1437, "Alpha", "", 0)
	ac.write.Reset()
	return s, a, b, ac
}

func requireAccountFriendPackets(t *testing.T, c *bufferConn, wantInfo, wantPending int) {
	t.Helper()
	counts := map[uint16]int{}
	for _, id := range accountFriendPacketIDs(t, c) {
		counts[id]++
	}
	if counts[1371] != wantInfo || counts[1372] != wantPending {
		t.Fatalf("friend packets=%v want info=%d pending=%d", counts, wantInfo, wantPending)
	}
}

func TestAccountFriendUnchangedPresenceAcrossFinishLoading(t *testing.T) {
	s, a, _, ac := prepareUnchangedAccountFriendTest(t)
	for room := 0; room < 3; room++ {
		// The per-room finish-loading gates are reopened on each transition.
		// Friend rows belong to the selected connection, not the room.
		a.currentFinishLoadingStateSent = true
		a.currentFinishLoadingCompletionSent = false
		a.postFinishLoadingPlayerStateSent = true
		if err := s.sendCurrentFinishLoadingCharacterState(a, "room_transition"); err != nil {
			t.Fatal(err)
		}
		requireAccountFriendPackets(t, ac, 0, 1)
		ac.write.Reset()
	}
}

func TestAccountFriendUnchangedPresenceStillUpdatesOnlineStateAndNotes(t *testing.T) {
	s, a, b, ac := prepareUnchangedAccountFriendTest(t)
	s.unbindGameSession(b)
	requireAccountFriendPackets(t, ac, 1, 1)
	ac.write.Reset()
	if err := s.sendCurrentAccountFriendLists(a, "unchanged_offline"); err != nil {
		t.Fatal(err)
	}
	requireAccountFriendPackets(t, ac, 0, 1)
	ac.write.Reset()
	s.bindGameSessionCharacter(b, 5)
	if err := s.sendCurrentAccountFriendLists(a, "online_again"); err != nil {
		t.Fatal(err)
	}
	requireAccountFriendPackets(t, ac, 1, 1)
	ac.write.Reset()
	g, _ := s.repositoryGroup()
	if err := accountfriend.NewOwner(g).UpdateNote(context.Background(), accountfriend.Command{
		AccountID: "dnf:1", CharacterID: "1", TargetAccountID: "dnf:2", TargetName: "Beta", Text: "new note",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.sendCurrentAccountFriendLists(a, "note_changed"); err != nil {
		t.Fatal(err)
	}
	requireAccountFriendPackets(t, ac, 1, 1)
}

func TestAccountFriendUnchangedPresenceResendsForSelectionAndReconnect(t *testing.T) {
	s, a, _, ac := prepareUnchangedAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	if err := g.Character.Save(context.Background(), dnfrepo.CharacterRecord{
		AccountID: "dnf:1", CharacterID: "12", Name: "Other", Level: 1, Job: "0",
	}); err != nil {
		t.Fatal(err)
	}
	s.bindGameSessionCharacter(a, 12)
	if err := s.sendCurrentAccountFriendLists(a, "selected_other"); err != nil {
		t.Fatal(err)
	}
	requireAccountFriendPackets(t, ac, 1, 1)
	ac.write.Reset()
	s.bindGameSessionCharacter(a, 1)
	if err := s.sendCurrentAccountFriendLists(a, "selected_original"); err != nil {
		t.Fatal(err)
	}
	requireAccountFriendPackets(t, ac, 1, 1)
	s.unbindGameSession(a)
	coldConn := &bufferConn{}
	cold := &gameSession{conn: coldConn, accountID: a.accountID, loginPacketSeen: true, accountFromToken: true, channel: a.channel}
	s.bindGameSessionCharacter(cold, 1)
	if err := s.sendCurrentAccountFriendLists(cold, "new_connection"); err != nil {
		t.Fatal(err)
	}
	requireAccountFriendPackets(t, coldConn, 1, 1)
}

func TestAccountFriendUnchangedPresenceCommitsOnlySuccessfulInfoWrites(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run([]string{"", "info", "pending"}[failAt], func(t *testing.T) {
			s, a, b, ac := prepareUnchangedAccountFriendTest(t)
			b.channel.ID++
			failure := &failNthDungeonWriteConn{failAt: failAt, err: errors.New("injected friend write failure")}
			a.conn = failure
			if err := s.sendCurrentAccountFriendLists(a, "changed_channel"); err == nil {
				t.Fatal("write failure hidden")
			}
			a.conn = ac
			if err := s.sendCurrentAccountFriendLists(a, "retry"); err != nil {
				t.Fatal(err)
			}
			wantInfo := 1
			if failAt == 2 {
				// The online row was already delivered. Retrying only the pending
				// list must not replay that row and its native online notification.
				wantInfo = 0
			}
			requireAccountFriendPackets(t, ac, wantInfo, 1)
		})
	}
}
