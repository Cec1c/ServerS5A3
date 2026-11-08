package dnfbridge

import (
	"context"
	"encoding/binary"
	"testing"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func TestCurrentAccountAdventureExperiencePreservesRosterBaseline(t *testing.T) {
	source := adventureShopExpirationSource("")
	source[adventuregroup.CharacterManagementPath] = "[point bonus]\n1 90 10\n[/point bonus]\n[manage level point]\n10 40 100 200 500 1000\n[/manage level point]\n[manage level max]\n6\n[exp bonus]\n[/exp bonus]\n[gold bonus]\n[/gold bonus]\n[manage option]\n1 5 2 9 3 15 4 20\n[/manage option]\n"
	source[adventuregroup.AdventureSystem2018Path] += "[max adventure level]\n6\n[adventure exp rate]\n0.3\n[adventure exp table]\n1 `0` 2 `100` 3 `200` 4 `300` 5 `400` 6 `500`\n[/adventure exp table]\n"
	s, session, repos, _, _, _ := newAdventureShopExpirationFixture(t, source)
	ctx := context.Background()
	character, _, _ := repos.Character.Load(ctx, "31")
	character.Level = 10 // 100 roster points: the existing level-three baseline.
	if err := repos.Character.Save(ctx, character); err != nil {
		t.Fatal(err)
	}
	peer := r.CharacterRecord{AccountID: session.accountID, CharacterID: "32", Slot: 1, Level: 5}
	if err := repos.Character.Save(ctx, peer); err != nil {
		t.Fatal(err)
	}
	account, _, _ := repos.Account.Load(ctx, session.accountID)
	account.HonorExp = 337 // 101 additional modern EXP, not a replacement baseline.
	if err := repos.Account.Save(ctx, account); err != nil {
		t.Fatal(err)
	}
	before, _, _ := repos.Account.Load(ctx, session.accountID)
	for i := 0; i < 3; i++ {
		state, err := s.currentAccountAdventureGroupState(ctx, character, true, session)
		if err != nil {
			t.Fatal(err)
		}
		if state.Summary.ManageLevel != 3 || state.Summary.TotalPoint != 251 || state.Summary.ManageOption != 15 {
			t.Fatalf("roster level/progress was replaced by the overflow ledger: %+v", state.Summary)
		}
		full, err := s.currentAccountAdventureGroupInfoState(ctx, character, true, session)
		if err != nil || full.Summary != state.Summary {
			t.Fatalf("full=%+v summary=%+v err=%v", full.Summary, state.Summary, err)
		}
		body := buildCurrentAdventureActorRefreshBody(31, state.Summary)
		if binary.LittleEndian.Uint32(body[6:]) != 3 || binary.LittleEndian.Uint64(body[14:]) != 251 {
			t.Fatal("actor refresh lost combined progress")
		}
		later := r.CloneAccount(before)
		later.HonorExp = 340
		updated, err := s.projectCurrentAdventureAccountState(state, later)
		if err != nil || updated.Summary.ManageLevel != 3 || updated.Summary.TotalPoint != 252 {
			t.Fatal(updated.Summary, err)
		}
	}
	after, _, _ := repos.Account.Load(ctx, session.accountID)
	// Daily-login bookkeeping is allowed; EXP must never receive a baseline grant.
	if before.HonorExp != after.HonorExp {
		t.Fatal("projection wrote roster EXP into the durable overflow ledger")
	}
	peer.Stats = map[string]int64{"delete_flag": 1}
	if err := repos.Character.Save(ctx, peer); err != nil {
		t.Fatal(err)
	}
	state, err := s.currentAccountAdventureGroupState(ctx, character, true, session)
	if err != nil || state.Summary.ManageLevel != 3 || state.Summary.TotalPoint != 101 {
		t.Fatalf("deleted roster row contributed: %+v %v", state.Summary, err)
	}
}
