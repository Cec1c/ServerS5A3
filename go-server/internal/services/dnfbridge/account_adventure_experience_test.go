package dnfbridge

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentAccountAdventureExperienceReadsDurableOwnerAndNativeRemainder(t *testing.T) {
	source := adventureShopExpirationSource("")
	source[adventuregroup.AdventureSystem2018Path] += "[max adventure level]\n3\n[adventure exp rate]\n0.3\n[adventure exp table]\n1 `0` 2 `100` 3 `200`\n[/adventure exp table]\n"
	s, session, repos, _, _, _ := newAdventureShopExpirationFixture(t, source)
	ctx := context.Background()
	account, _, _ := repos.Account.Load(ctx, session.accountID)
	account.HonorExp = 337
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := repos.Account.Save(ctx, r.AccountRecord{AccountID: "other", HonorExp: 999}); err != nil {
		t.Fatal(err)
	}
	s.options.accountID = "other"
	character, _, _ := repos.Character.Load(ctx, "31")
	for i := 0; i < 2; i++ {
		state, err := s.currentAccountAdventureGroupInfoState(ctx, character, true, session)
		if err != nil {
			t.Fatal(err)
		}
		if state.Summary.ManageLevel != 2 || state.Summary.TotalPoint != 1 {
			t.Fatal(state.Summary)
		}
		body := buildCurrentAdventureActorRefreshBody(31, state.Summary)
		if binary.LittleEndian.Uint32(body[6:]) != 2 || binary.LittleEndian.Uint64(body[14:]) != 1 {
			t.Fatal("actor refresh did not use the current-level remainder")
		}
	}
	before, _, _ := repos.Account.Load(ctx, session.accountID)
	state, err := s.currentAccountAdventureGroupState(ctx, character, true, session)
	if err != nil {
		t.Fatal(err)
	}
	// A later full-account read must refresh EXP as well as the shop/capsule.
	later := r.CloneAccount(before)
	later.HonorExp = 340
	state, err = s.projectCurrentAdventureAccountState(state, later)
	if err != nil || state.Summary.TotalPoint != 2 || state.Summary.ManageLevel != 2 {
		t.Fatal(state.Summary, err)
	}
	after, _, _ := repos.Account.Load(ctx, session.accountID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read-only experience projection wrote the account")
	}
	character.AccountID = "other"
	if _, err := s.currentAccountAdventureGroupState(ctx, character, true, session); err == nil {
		t.Fatal("foreign owner accepted")
	}
	repos.Account = nil
	s.repositoryProvider = func() (r.Group, bool) { return repos, true }
	if _, err := s.currentAccountAdventureGroupState(ctx, r.CharacterRecord{}, false, session); err == nil {
		t.Fatal("modern projection silently fell back without its account ledger")
	}
}
