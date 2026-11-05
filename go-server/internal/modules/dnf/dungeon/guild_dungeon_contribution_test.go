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

func enableDungeonContribution(t *testing.T, repos r.Group, cmd *SettlementCommand) {
	t.Helper()
	cmd.GuildContribution = &GuildContributionClear{
		Contract:   contributionSettlementContract(t, repos),
		Activity:   guild.ContributionActivity{Event: guild.ContributionGuildClear, CompletionKey: cmd.CompletionKey},
		Revalidate: func() bool { return true },
	}
}

func TestGuildDungeonContributionMailRollbackAndUnknownCommit(t *testing.T) {
	for _, mode := range []string{"concurrent", "rollback", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			repos, cmd := guildDungeonSettlementFixture(t)
			ctx := context.Background()
			enableDungeonContribution(t, repos, &cmd)
			inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			if inv.Slots == nil {
				inv.Slots = map[string]r.ItemStack{}
			}
			for i := 177; i <= 232; i++ {
				inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: int64(1000 + i), Count: 1}
			}
			if err := repos.Inventory.Save(ctx, inv); err != nil {
				t.Fatal(err)
			}
			beforeChar, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
			beforeGuild, _ := guild.Load(ctx, repos.Settings)
			original := repos.CharacterSettlement
			if mode != "concurrent" {
				repos.CharacterSettlement = guildSettlementFault{original, original.(r.GuildSettlementUnitOfWork), mode == "unknown"}
				o, _ := NewOwner(repos)
				_, err := o.CommitSettlement(ctx, cmd)
				want := errGuildSettlementLate
				if mode == "unknown" {
					want = r.ErrGuildCommitUnknown
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
				if mode == "rollback" {
					afterChar, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
					afterInv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
					afterGuild, _ := guild.Load(ctx, repos.Settings)
					box, _, _ := repos.Mailbox.Load(ctx, cmd.CharacterID)
					_, receipt, _ := repos.Settings.Load(ctx, r.GuildSettlementReceiptScope(r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}, cmd.CompletionKey))
					if !reflect.DeepEqual(beforeChar, afterChar) || !reflect.DeepEqual(inv, afterInv) || !reflect.DeepEqual(beforeGuild, afterGuild) || len(box.Mails) != 0 || receipt {
						t.Fatal("failed settlement retained a partial reward")
					}
				}
				repos.CharacterSettlement = original
			}
			o, _ := NewOwner(repos)
			var wg sync.WaitGroup
			errs := make(chan error, 8)
			for range 8 {
				wg.Go(func() { _, err := o.CommitSettlement(ctx, cmd); errs <- err })
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			ch, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
			reg, _ := guild.Load(ctx, repos.Settings)
			box, _, _ := repos.Mailbox.Load(ctx, cmd.CharacterID)
			if ch.Stats[fmt.Sprintf("battlepass2:%s:9:-1", r.ActivityDay(cmd.UpdatedAt))] != 20 || reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 20 || len(box.Mails) != 1 {
				t.Fatal("missing or duplicate reward", ch.Stats, reg.Guilds[1], box)
			}
			for _, mail := range box.Mails {
				if len(mail.Attachments) != 1 || mail.Attachments[0].ItemID != guild.ContributionCoinID || mail.Attachments[0].Count != 20 || !mail.ExpireAt.Equal(cmd.UpdatedAt.Add(15*24*time.Hour)) {
					t.Fatal(mail)
				}
			}
		})
	}
}

func TestGuildDungeonContributionLegacyReceiptAndProjection(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			repos, cmd := guildDungeonSettlementFixture(t)
			ctx := context.Background()
			if legacy {
				reg, _ := guild.Load(ctx, repos.Settings)
				m := reg.Guilds[1].Members[cmd.CharacterID]
				m.DungeonContributionPoints = nil
				reg.Guilds[1].Members[cmd.CharacterID] = m
				if err := guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt); err != nil {
					t.Fatal(err)
				}
			}
			o, _ := NewOwner(repos)
			if _, err := o.CommitSettlement(ctx, cmd); err != nil {
				t.Fatal(err)
			}
			enableDungeonContribution(t, repos, &cmd)
			replay, err := o.CommitSettlement(ctx, cmd)
			if err != nil || !replay.Replayed || replay.GuildContribution.Points != 0 {
				t.Fatal(replay, err)
			}
			cmd.GuildDungeon.Entry.RunID = "new-paid-clear"
			cmd.CompletionKey = cmd.GuildDungeon.Entry.CompletionKey()
			cmd.GuildContribution.Activity.CompletionKey = cmd.CompletionKey
			res, err := o.CommitSettlement(ctx, cmd)
			if err != nil || res.GuildContribution.Points != 20 {
				t.Fatal(res, err)
			}
			reg, _ := guild.Load(ctx, repos.Settings)
			members, err := guild.HongbaoContributionMembers(ctx, repos.Settings, reg.Guilds[1])
			if err != nil || len(members) != 1 || members[0].ContributionPoints != 20 || reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 40 || res.Inventory.Slots["0:177"].Count != 20 {
				t.Fatal("legacy score must stay excluded; newly paid score must remain", members, reg.Guilds[1], err)
			}
		})
	}
}
