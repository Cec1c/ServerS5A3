package guild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const contributionFixture = "[hongbao point reward]\n`guild dungeon clear` 5 10 `guild chat` 1 2 `guild attendance` 1 3 `party dungeon clear` 2 1\n[/hongbao point reward]"

func contributionContract(t *testing.T) *ContributionContract {
	t.Helper()
	c, err := ParseContributionCatalog(contributionFixture)
	if err != nil {
		t.Fatal(err)
	}
	return &ContributionContract{Catalog: c, Policy: ContributionPolicy{GuildClearDailyAwards: 1, SkipAttendanceJoinDay: true}, Deliver: func(_ context.Context, _ r.Group, _ *r.CharacterRecord, _ *r.InventoryRecord, _ uint32, _ string, _ time.Time) (ContributionDelivery, error) {
		return ContributionDelivery{Slot: 153}, nil
	}}
}

func TestContributionCatalogMatchedAndMalformed(t *testing.T) {
	want := contributionContract(t).Catalog
	for _, s := range []string{"", strings.Replace(contributionFixture, "5 10", "0 10", 1), strings.Replace(contributionFixture, "1 3", "1 -3", 1), strings.Replace(contributionFixture, "2 1", "1 1", 1), strings.Replace(contributionFixture, "`party dungeon clear`", "`guild chat`", 1), contributionFixture + "\n" + contributionFixture} {
		if _, err := ParseContributionCatalog(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	if path := os.Getenv("GUILD_CONTRIBUTION_EXTRACTED_ETC"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseContributionCatalog(string(b))
		if err != nil || !reflect.DeepEqual(want, got) {
			t.Fatal(got, err)
		}
	}
}

func TestContributionDailyConcurrentReplayJoinDayAndBoundary(t *testing.T) {
	repos, owner, now := essenceTestGroup(t)
	ctx := context.Background()
	contract := contributionContract(t)
	u := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork)
	var wg sync.WaitGroup
	results := make(chan ContributionIncome, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			res, err := GrantDailyContribution(ctx, u, owner, contract, ContributionAttendance, now, func() bool { return true })
			results <- res
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	fresh := 0
	for res := range results {
		if !res.Replayed {
			fresh++
			if res.Points != 3 {
				t.Fatal(res)
			}
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fresh != 1 {
		t.Fatal(fresh)
	}
	chat, err := GrantDailyContribution(ctx, u, owner, contract, ContributionChat, now, func() bool { return true })
	if err != nil || chat.Points != 2 {
		t.Fatal(chat, err)
	}
	reg, _ := Load(ctx, repos.Settings)
	if reg.Guilds[1].Members[owner.CharacterID].ContributionPoints != 5 {
		t.Fatal(reg)
	}
	// Membership changes do not erase the per-character daily ledger/receipt.
	member := reg.Guilds[1].Members[owner.CharacterID]
	member.JoinedAt = now
	reg.Guilds[1].Members[owner.CharacterID] = member
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	retry, err := GrantDailyContribution(ctx, u, owner, contract, ContributionChat, now, func() bool { return true })
	if err != nil || !retry.Replayed {
		t.Fatal(retry, err)
	}
	// now is 05:59:59 UTC+8; the next second is a new service day.
	next, err := GrantDailyContribution(ctx, u, owner, contract, ContributionAttendance, now.Add(time.Second), func() bool { return true })
	if err != nil || next.Points != 3 || next.Replayed {
		t.Fatal(next, err)
	}
	// Joining today excludes attendance, while chat remains eligible.
	reg, _ = Load(ctx, repos.Settings)
	member = reg.Guilds[1].Members["2"]
	member.JoinedAt = now.Add(time.Second)
	reg.Guilds[1].Members["2"] = member
	if err := Save(ctx, repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
	joined, err := GrantDailyContribution(ctx, u, r.GuildOwner{AccountID: "a2", CharacterID: "2"}, contract, ContributionAttendance, now.Add(time.Second), func() bool { return true })
	if err != nil || joined.Points != 0 {
		t.Fatal(joined, err)
	}
	firstChat, err := GrantDailyContribution(ctx, u, r.GuildOwner{AccountID: "a2", CharacterID: "2"}, contract, ContributionChat, now.Add(time.Second), func() bool { return true })
	if err != nil || firstChat.Points != 2 || firstChat.Replayed {
		t.Fatal(firstChat, err)
	}
}

func TestContributionGuildClearThresholdAndPolicy(t *testing.T) {
	for _, limit := range []uint32{0, 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			repos, owner, now := essenceTestGroup(t)
			ctx := context.Background()
			contract := contributionContract(t)
			contract.Policy.GuildClearDailyAwards = limit
			for i := 1; i <= 10; i++ {
				err := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork).WithinGuildSettlement(ctx, owner, fmt.Sprint(i), func(tx r.Group) error {
					ch, _, e := tx.Character.Load(ctx, owner.CharacterID)
					if e != nil {
						return e
					}
					inv := r.InventoryRecord{CharacterID: owner.CharacterID}
					res, e := ApplyContribution(ctx, tx, &ch, &inv, contract, ContributionActivity{Event: ContributionGuildClear, CompletionKey: fmt.Sprint(i)}, now)
					if e != nil {
						return e
					}
					want := uint32(0)
					if i == 5 || (limit == 0 && i == 10) {
						want = 10
					}
					if res.Points != want {
						t.Fatalf("clear %d: %+v", i, res)
					}
					return r.SaveCharacterFields(ctx, tx.Character, ch, r.CharacterFieldStats)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			reg, _ := Load(ctx, repos.Settings)
			want := uint32(10)
			if limit == 0 {
				want = 20
			}
			if reg.Guilds[1].Members[owner.CharacterID].ContributionPoints != want {
				t.Fatal(reg)
			}
		})
	}
}

func TestContributionPartyMembershipAndFailedDelivery(t *testing.T) {
	repos, owner, now := essenceTestGroup(t)
	ctx := context.Background()
	contract := contributionContract(t)
	for _, tc := range []struct {
		name    string
		members []r.GuildOwner
		points  uint32
		fail    bool
	}{
		{"same", []r.GuildOwner{owner, {AccountID: "a2", CharacterID: "2"}}, 1, false},
		{"other", []r.GuildOwner{owner, {AccountID: "a3", CharacterID: "3"}}, 0, false},
		{"wrong-owner", []r.GuildOwner{owner, {AccountID: "wrong", CharacterID: "2"}}, 0, false},
		{"duplicate", []r.GuildOwner{owner, owner}, 0, true},
		{"no-self", []r.GuildOwner{{AccountID: "a2", CharacterID: "2"}, {AccountID: "a3", CharacterID: "3"}}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork).WithinGuildSettlement(ctx, owner, tc.name, func(tx r.Group) error {
				ch, _, _ := tx.Character.Load(ctx, owner.CharacterID)
				inv := r.InventoryRecord{CharacterID: owner.CharacterID}
				frozen := map[string]ContributionMembership{}
				registry, _ := Load(ctx, tx.Settings)
				for _, m := range tc.members {
					frozen[m.CharacterID] = ContributionMembership{}
					if g, member, ok := registry.GuildForOwner(m); ok {
						frozen[m.CharacterID] = ContributionMembership{GuildID: g.ID, JoinedAt: member.JoinedAt}
					}
				}
				res, e := ApplyContribution(ctx, tx, &ch, &inv, contract, ContributionActivity{Event: ContributionPartyClear, PartyMembers: tc.members, FrozenGuilds: frozen, CompletionKey: tc.name}, now)
				if (e != nil) != tc.fail || res.Points != tc.points {
					t.Fatal(res, e)
				}
				return e
			})
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
		})
	}
	before, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	regBefore, _ := Load(ctx, repos.Settings)
	contract.Deliver = func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, uint32, string, time.Time) (ContributionDelivery, error) {
		return ContributionDelivery{}, errors.New("delivery unavailable")
	}
	if _, err := GrantDailyContribution(ctx, repos.CharacterSettlement.(r.GuildSettlementUnitOfWork), owner, contract, ContributionChat, now, func() bool { return true }); err == nil {
		t.Fatal("delivery failure accepted")
	}
	after, _, _ := repos.Character.Load(ctx, owner.CharacterID)
	regAfter, _ := Load(ctx, repos.Settings)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(regBefore, regAfter) {
		t.Fatal("failed delivery changed contribution")
	}
}

func TestContributionPartyFrozenMembershipRejectsRejoin(t *testing.T) {
	for _, changed := range []string{"1", "2"} {
		t.Run(changed, func(t *testing.T) {
			repos, owner, now := essenceTestGroup(t)
			ctx := context.Background()
			reg, _ := Load(ctx, repos.Settings)
			members := []r.GuildOwner{owner, {AccountID: "a2", CharacterID: "2"}}
			frozen := map[string]ContributionMembership{}
			for _, peer := range members {
				g, m, _ := reg.GuildForOwner(peer)
				frozen[peer.CharacterID] = ContributionMembership{GuildID: g.ID, JoinedAt: m.JoinedAt}
			}
			member := reg.Guilds[1].Members[changed]
			member.JoinedAt = now
			reg.Guilds[1].Members[changed] = member
			if err := Save(ctx, repos.Settings, reg, now); err != nil {
				t.Fatal(err)
			}
			err := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork).WithinGuildSettlement(ctx, owner, "rejoin", func(tx r.Group) error {
				ch, _, _ := tx.Character.Load(ctx, owner.CharacterID)
				inv := r.InventoryRecord{CharacterID: owner.CharacterID}
				result, err := ApplyContribution(ctx, tx, &ch, &inv, contributionContract(t), ContributionActivity{Event: ContributionPartyClear, PartyMembers: members, FrozenGuilds: frozen, CompletionKey: "rejoin"}, now)
				if err != nil || result.Points != 0 {
					t.Fatalf("rejoined membership earned old clear: %+v %v", result, err)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
