package dnfbridge

import (
	"context"
	r "longheng.io/server/internal/modules/dnf/repository"
	"testing"
)

type friendReadCounter struct {
	r.AccountRepository
	loads int
}

func (c *friendReadCounter) Load(ctx context.Context, id string) (r.AccountRecord, bool, error) {
	c.loads++
	return c.AccountRepository.Load(ctx, id)
}

func TestUnchangedRoomFriendPresenceSkipsRepositoryReads(t *testing.T) {
	s, a, _, _, _ := prepareAccountFriendTest(t)
	g, _ := s.repositoryGroup()
	accounts := &friendReadCounter{AccountRepository: g.Account}
	g.Account = accounts
	s.repositoryProvider = func() (r.Group, bool) { return g, true }
	s.refreshCurrentAccountFriendPresence(a, "initial")
	before := accounts.loads
	if before == 0 {
		t.Fatal("fixture did not read account")
	}
	for range 100 {
		s.refreshCurrentAccountFriendPresence(a, "finish_loading_after_player_state")
	}
	if accounts.loads != before {
		t.Fatal("room traversal rebuilt presence", accounts.loads, before)
	}
	a.channel.ID++
	s.refreshCurrentAccountFriendPresence(a, "channel_changed")
	if accounts.loads == before {
		t.Fatal("channel change was hidden")
	}
	before = accounts.loads
	if err := s.sendCurrentAccountFriendLists(a, "initial"); err != nil {
		t.Fatal(err)
	}
	before = accounts.loads
	for range 20 {
		if err := s.sendCurrentAccountFriendLists(a, "finish_loading_after_player_state"); err != nil {
			t.Fatal(err)
		}
	}
	if accounts.loads != before {
		t.Fatal("unchanged self list reread")
	}
	if err := s.sendCurrentAccountFriendLists(a, "mutation"); err != nil {
		t.Fatal(err)
	}
	if accounts.loads == before {
		t.Fatal("mutation did not refresh")
	}
}
