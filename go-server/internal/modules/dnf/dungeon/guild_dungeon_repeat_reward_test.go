package dungeon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func repeatRewardFixture(t *testing.T) (r.Group, SettlementCommand) {
	t.Helper()
	repos, cmd := guildDungeonSettlementFixture(t)
	catalog, err := guild.ParseDungeonCatalog(guildDungeonTestCatalog + "\n[repeat clear bonus reward]\n10156320 1\n")
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := guild.Load(context.Background(), repos.Settings)
	entries, err := guild.PrepareDungeonEntries(reg, catalog, 8500, []r.GuildOwner{cmd.GuildDungeon.Entry.Owner}, "repeat-0", cmd.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	cmd.GuildDungeon.Entry = entries[0]
	cmd.GuildDungeon.Catalog = catalog
	cmd.GuildDungeon.Items[10156320] = CardItemReward{Stack: r.ItemStack{ItemID: 10156320, Count: 1}, Stackable: true, SlotStart: 121, SlotEnd: 152, StackLimit: 10000}
	cmd.CompletionKey = entries[0].CompletionKey()
	return repos, cmd
}

func seedRepeatProgress(t *testing.T, repos r.Group, id string, progress int64) {
	t.Helper()
	c, _, err := repos.Character.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	c.Stats[guild.DungeonRepeatRewardProgressStat] = progress
	if err = repos.Character.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func killRewardCommand(cmd SettlementCommand, target string, total uint32) GuildDungeonKillCommand {
	return GuildDungeonKillCommand{Clear: cmd.GuildDungeon, Target: target, Total: total, UpdatedAt: cmd.UpdatedAt}
}
func repeatBoxCount(inv r.InventoryRecord) int64 {
	n := int64(0)
	for _, v := range inv.Slots {
		if v.ItemID == 10156320 {
			n += v.Count
		}
	}
	return n
}
func TestGuildDungeonRepeatRewardKillsAndPartialExit(t *testing.T) {
	repos, cmd := repeatRewardFixture(t)
	ctx := context.Background()
	owner, _ := NewOwner(repos)
	seedRepeatProgress(t, repos, cmd.CharacterID, 95)
	for n := 0; n < 3; n++ {
		if _, err := owner.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, fmt.Sprint(n), 7)); err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	if c.Stats[guild.DungeonRepeatRewardProgressStat] != 99 || c.Stats[guild.DungeonRepeatRewardFractionStat] != 28 {
		t.Fatal(c.Stats)
	}
	// Abandon/re-enter preserves partial progress, then a new run has a new budget.
	old := *cmd.GuildDungeon
	next := old
	next.Entry.RunID = "next"
	cmd.GuildDungeon = &next
	for n := 0; n < 7; n++ {
		if _, err := owner.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, fmt.Sprint(n), 7)); err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ = repos.Character.Load(ctx, cmd.CharacterID)
	inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	if c.Stats[guild.DungeonRepeatRewardProgressStat] != 9 || c.Stats[guild.DungeonRepeatRewardFractionStat] != 28 || repeatBoxCount(inv) != 1 {
		t.Fatal(c.Stats, inv)
	}
	// Old run and old target stay deduplicated after another run and a new owner.
	cmd.GuildDungeon = &old
	owner, _ = NewOwner(repos)
	if got, err := owner.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "0", 7)); err != nil || !got.Replayed {
		t.Fatal(got, err)
	}
	after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(c, after) {
		t.Fatal("old replay mutated progress")
	}
	// A clear still grants its original guild items but never invents more kills.
	if _, err := owner.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	after, _, _ = repos.Character.Load(ctx, cmd.CharacterID)
	if after.Stats[guild.DungeonRepeatRewardProgressStat] != 9 || after.Stats[guild.DungeonRepeatRewardFractionStat] != 28 {
		t.Fatal("clear added progress")
	}
}
func TestGuildDungeonRepeatRewardConcurrentFullBar(t *testing.T) {
	repos, cmd := repeatRewardFixture(t)
	ctx := context.Background()
	seedRepeatProgress(t, repos, cmd.CharacterID, 90)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _ := NewOwner(repos)
			_, err := o.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "boss", 1))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	c, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	if repeatBoxCount(inv) != 1 || c.Stats[guild.DungeonRepeatRewardProgressStat] != 0 {
		t.Fatal(inv, c)
	}
	o, _ := NewOwner(repos)
	if _, err := o.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "spawned-extra", 1)); err == nil {
		t.Fatal("exceeded run cap")
	}
}

type killRewardRollback struct {
	r.CharacterSettlementUnitOfWork
	kill r.GuildDungeonKillUnitOfWork
}

func (f killRewardRollback) WithinGuildDungeonKill(ctx context.Context, owner r.GuildOwner, key string, apply func(r.Group) error) error {
	return f.kill.WithinGuildDungeonKill(ctx, owner, key, func(tx r.Group) error {
		if err := apply(tx); err != nil {
			return err
		}
		return errors.New("late failure")
	})
}
func TestGuildDungeonRepeatRewardAtomicFailures(t *testing.T) {
	for _, kind := range []string{"late rollback", "missing box", "invalid balance", "wrong owner", "changed budget"} {
		t.Run(kind, func(t *testing.T) {
			repos, cmd := repeatRewardFixture(t)
			ctx := context.Background()
			seedRepeatProgress(t, repos, cmd.CharacterID, 90)
			original := repos.CharacterSettlement
			box := cmd.GuildDungeon.Items[10156320]
			entry := cmd.GuildDungeon.Entry
			switch kind {
			case "late rollback":
				repos.CharacterSettlement = killRewardRollback{original, original.(r.GuildDungeonKillUnitOfWork)}
			case "missing box":
				delete(cmd.GuildDungeon.Items, 10156320)
			case "invalid balance":
				seedRepeatProgress(t, repos, cmd.CharacterID, 100)
			case "wrong owner":
				cmd.GuildDungeon.Entry.Owner.AccountID = "foreign"
			case "changed budget":
				cmd.GuildDungeon.Entry.RepeatRewardPercent = 35
			}
			before, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
			invBefore, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			regBefore, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			o, _ := NewOwner(repos)
			if _, err := o.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "0", 1)); err == nil {
				t.Fatal("fault committed")
			}
			after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
			invAfter, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			regAfter, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(regBefore, regAfter) {
				t.Fatal("partial transaction")
			}
			_, found, _ := repos.Settings.Load(ctx, r.GuildDungeonKillReceiptScope(entry.Owner, entry.CompletionKey()))
			if found {
				t.Fatal("failed receipt persisted")
			}
			repos.CharacterSettlement = original
			cmd.GuildDungeon.Items[10156320] = box
			cmd.GuildDungeon.Entry = entry
			seedRepeatProgress(t, repos, cmd.CharacterID, 90)
			o, _ = NewOwner(repos)
			if got, err := o.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "0", 1)); err != nil || !got.Reward.Awarded {
				t.Fatal(got, err)
			}
		})
	}
}
func TestGuildDungeonRepeatRewardFullBagMail(t *testing.T) {
	repos, cmd := repeatRewardFixture(t)
	ctx := context.Background()
	seedRepeatProgress(t, repos, cmd.CharacterID, 90)
	inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	inv.Slots = map[string]r.ItemStack{}
	for i := 121; i <= 152; i++ {
		inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: 222, Count: 1}
	}
	if err := repos.Inventory.Save(ctx, inv); err != nil {
		t.Fatal(err)
	}
	o, _ := NewOwner(repos)
	got, err := o.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "0", 1))
	if err != nil || got.MailID == "" || !got.Reward.Awarded {
		t.Fatal(got, err)
	}
	mailbox, _, err := repos.Mailbox.Load(ctx, cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := o.CommitGuildDungeonKill(ctx, killRewardCommand(cmd, "0", 1))
	if err != nil || !again.Replayed || again.MailID != got.MailID {
		t.Fatal(again, err)
	}
	after, _, _ := repos.Mailbox.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(mailbox, after) {
		t.Fatal("duplicate mail")
	}
}
