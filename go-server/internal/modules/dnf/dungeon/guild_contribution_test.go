package dungeon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func contributionSettlementContract(t *testing.T, repos r.Group) *guild.ContributionContract {
	t.Helper()
	c, err := guild.ParseContributionCatalog("[hongbao point reward]\n`guild dungeon clear` 5 10 `guild chat` 1 2 `guild attendance` 1 3 `party dungeon clear` 2 1\n[/hongbao point reward]")
	if err != nil {
		t.Fatal(err)
	}
	definition := CardItemReward{Stack: r.ItemStack{ItemID: guild.ContributionCoinID, Count: 1, Bind: true, Extra: map[string]string{"item_kind": "stackable", "stackable_type": "[quest]"}}, Stackable: true, SlotStart: 177, SlotEnd: 232, StackLimit: 10000}
	return &guild.ContributionContract{Catalog: c, Policy: guild.ContributionPolicy{GuildClearDailyAwards: 1, SkipAttendanceJoinDay: true}, Deliver: GuildContributionDelivery(repos.Settings, definition, nil)}
}

func contributionMatureMembership(t *testing.T, repos r.Group, now time.Time) {
	t.Helper()
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range reg.Guilds {
		for id, m := range g.Members {
			m.JoinedAt = now.Add(-48 * time.Hour)
			g.Members[id] = m
		}
	}
	if err := guild.Save(context.Background(), repos.Settings, reg, now); err != nil {
		t.Fatal(err)
	}
}

func TestContributionCoinsInventoryMailConcurrentAndReplay(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			repos, cmd := guildSettlementFixture(t)
			contributionMatureMembership(t, repos, cmd.UpdatedAt)
			ctx := context.Background()
			if full {
				inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
				if inv.Slots == nil {
					inv.Slots = map[string]r.ItemStack{}
				}
				for i := 177; i <= 232; i++ {
					inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: 1000 + int64(i), Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			}
			contract := contributionSettlementContract(t, repos)
			owner := r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}
			u := repos.CharacterSettlement.(r.GuildSettlementUnitOfWork)
			var wg sync.WaitGroup
			errs := make(chan error, 12)
			for range 12 {
				wg.Go(func() {
					_, err := guild.GrantDailyContribution(ctx, u, owner, contract, guild.ContributionAttendance, cmd.UpdatedAt, func() bool { return true })
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			box, _, _ := repos.Mailbox.Load(ctx, cmd.CharacterID)
			reg, _ := guild.Load(ctx, repos.Settings)
			if reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 3 {
				t.Fatal(reg)
			}
			if full {
				if len(box.Mails) != 1 {
					t.Fatal(box)
				}
				for _, m := range box.Mails {
					if len(m.Attachments) != 1 || m.Attachments[0].ItemID != guild.ContributionCoinID || m.Attachments[0].Count != 3 || !m.ExpireAt.Equal(cmd.UpdatedAt.Add(15*24*time.Hour)) || m.Metadata["unlimited"] != "" {
						t.Fatal(m)
					}
				}
			} else if len(box.Mails) != 0 || inv.Slots["0:177"].Count != 3 {
				t.Fatal(inv, box)
			}
			again, err := guild.GrantDailyContribution(ctx, u, owner, contract, guild.ContributionAttendance, cmd.UpdatedAt, func() bool { return true })
			if err != nil || !again.Replayed {
				t.Fatal(again, err)
			}
			// The distinct chat event merges into the same task stack (or creates its
			// own 15-day overflow mail); it never replaces an existing attachment.
			if _, err := guild.GrantDailyContribution(ctx, u, owner, contract, guild.ContributionChat, cmd.UpdatedAt, func() bool { return true }); err != nil {
				t.Fatal(err)
			}
			inv, _, _ = repos.Inventory.Load(ctx, cmd.CharacterID)
			box, _, _ = repos.Mailbox.Load(ctx, cmd.CharacterID)
			if full && len(box.Mails) != 2 || !full && inv.Slots["0:177"].Count != 5 {
				t.Fatal(inv, box)
			}
		})
	}
}

func TestContributionGuildDungeonFiveClearsAndOldReceipt(t *testing.T) {
	repos, cmd := guildDungeonSettlementFixture(t)
	ctx := context.Background()
	contributionMatureMembership(t, repos, cmd.UpdatedAt)
	cmd.GuildDungeon.Entry.JoinedAt = cmd.UpdatedAt.Add(-48 * time.Hour)
	contract := contributionSettlementContract(t, repos)
	cmd.GuildContribution = &GuildContributionClear{Contract: contract, Activity: guild.ContributionActivity{Event: guild.ContributionGuildClear, CompletionKey: cmd.CompletionKey}, Revalidate: func() bool { return true }}
	original := cmd
	o, _ := NewOwner(repos)
	for i := 1; i <= 10; i++ {
		clear := *cmd.GuildDungeon
		clear.Entry.RunID = fmt.Sprint("coin-run-", i)
		cmd.GuildDungeon = &clear
		cmd.CompletionKey = clear.Entry.CompletionKey()
		cmd.GuildContribution.Activity.CompletionKey = cmd.CompletionKey
		if i == 1 {
			original = cmd
			copyContract := *cmd.GuildContribution
			original.GuildContribution = &copyContract
		}
		res, err := o.CommitSettlement(ctx, cmd)
		want := uint32(0)
		if i == 5 {
			want = 10
		}
		if err != nil || res.GuildContribution.Points != want {
			t.Fatal(i, res.GuildContribution, err)
		}
	}
	before, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	replay, err := o.CommitSettlement(ctx, original)
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
	after, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	reg, _ := guild.Load(ctx, repos.Settings)
	if !reflect.DeepEqual(before, after) || after.Slots["0:177"].Count != 10 || reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 210 {
		t.Fatal(after, reg.Guilds[1])
	}
}

func TestContributionSettlementLateFailureAndUnknownCommit(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			repos, cmd := guildSettlementFixture(t)
			ctx := context.Background()
			contributionMatureMembership(t, repos, cmd.UpdatedAt)
			owner := r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}
			reg, _ := guild.Load(ctx, repos.Settings)
			peer := r.CharacterRecord{AccountID: "peer", CharacterID: "other", Name: "Peer"}
			if _, err := reg.JoinDirect(1, peer, cmd.UpdatedAt.Add(-48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			if inv.Slots == nil {
				inv.Slots = map[string]r.ItemStack{}
			}
			for i := 177; i <= 232; i++ {
				inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: 1000 + int64(i), Count: 1}
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			cmd.GuildContribution = &GuildContributionClear{Contract: contributionSettlementContract(t, repos), Activity: guild.ContributionActivity{Event: guild.ContributionPartyClear, CompletionKey: cmd.CompletionKey, PartyMembers: []r.GuildOwner{owner, {AccountID: peer.AccountID, CharacterID: peer.CharacterID}}, FrozenGuilds: map[string]guild.ContributionMembership{cmd.CharacterID: {GuildID: 1, JoinedAt: cmd.UpdatedAt.Add(-48 * time.Hour)}, peer.CharacterID: {GuildID: 1, JoinedAt: cmd.UpdatedAt.Add(-48 * time.Hour)}}}, Revalidate: func() bool { return true }}
			original := repos.CharacterSettlement
			repos.CharacterSettlement = guildSettlementFault{original, original.(r.GuildSettlementUnitOfWork), unknown}
			o, _ := NewOwner(repos)
			_, err := o.CommitSettlement(ctx, cmd)
			want := errGuildSettlementLate
			if unknown {
				want = r.ErrGuildCommitUnknown
			}
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			reg, _ = guild.Load(ctx, repos.Settings)
			box, _, _ := repos.Mailbox.Load(ctx, cmd.CharacterID)
			if !unknown && (reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 0 || len(box.Mails) != 0) {
				t.Fatal("partial contribution/mail")
			}
			repos.CharacterSettlement = original
			o, _ = NewOwner(repos)
			res, err := o.CommitSettlement(ctx, cmd)
			if err != nil || res.Replayed != unknown {
				t.Fatal(res, err)
			}
			reg, _ = guild.Load(ctx, repos.Settings)
			box, _, _ = repos.Mailbox.Load(ctx, cmd.CharacterID)
			if reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 1 || len(box.Mails) != 1 {
				t.Fatal("recovery duplicated coins")
			}
		})
	}
}
