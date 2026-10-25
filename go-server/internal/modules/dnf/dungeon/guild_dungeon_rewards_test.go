package dungeon

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const guildDungeonTestCatalog = "[dungeon]\n[dungeon index]\n8500\n[gauge limit]\n10000\n[gauge on clear]\n150\n[conquest gauge ratio]\n50\n[reward]\n[gauge ratio]\n100\n[item]\n10154439 3\n[contribute]\n20\n[/gauge ratio]\n[/reward]\n[/dungeon]"

func guildDungeonSettlementFixture(t *testing.T) (r.Group, SettlementCommand) {
	t.Helper()
	repos, cmd := guildSettlementFixture(t)
	cmd.GuildOrdinaryClear = false
	cmd.LuckyStar = nil
	c, err := guild.ParseDungeonCatalog(guildDungeonTestCatalog)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := guild.PrepareDungeonEntries(reg, c, 8500, []r.GuildOwner{{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}}, "run-first", cmd.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	cmd.GuildDungeon = &GuildDungeonClear{Entry: entries[0], Catalog: c, Items: map[uint32]CardItemReward{10154439: {Stack: r.ItemStack{ItemID: 10154439, Count: 1}, Stackable: true, SlotStart: 121, SlotEnd: 152, StackLimit: 10000}}}
	cmd.CompletionKey = entries[0].CompletionKey()
	return repos, cmd
}

func TestGuildDungeonSettlementPersistedReplayAndNextCycle(t *testing.T) {
	repos, cmd := guildDungeonSettlementFixture(t)
	ctx := context.Background()
	o, _ := NewOwner(repos)
	first, err := o.CommitSettlement(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.GuildDungeon.Count != 1 || first.GuildDungeon.Contribution != 20 || first.Inventory.Slots["0:121"].Count != 3 || first.GuildMileage.GuildID != 0 {
		t.Fatalf("result=%+v", first)
	}
	second := cmd
	clear := *cmd.GuildDungeon
	clear.Entry.RunID = "second"
	second.GuildDungeon = &clear
	second.CompletionKey = clear.Entry.CompletionKey()
	if _, err = o.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	// New owner, older durable receipt, after a subsequent completion.
	o, _ = NewOwner(repos)
	replay, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.GuildDungeonItems, first.GuildDungeonItems) {
		t.Fatal(replay, err)
	}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil || reg.Guilds[1].Dungeon.Counts[8500] != 2 || reg.Guilds[1].Members[cmd.CharacterID].ContributionPoints != 40 || replay.Inventory.Slots["0:121"].Count != 6 {
		t.Fatal("replay changed clear", err)
	}
	stale := second
	newClear := clear
	newClear.Entry.RunID = "uncommitted-old"
	stale.GuildDungeon = &newClear
	stale.CompletionKey = newClear.Entry.CompletionKey()
	stale.UpdatedAt = time.Unix(int64(clear.Entry.Cycle.EndUnix), 0)
	if _, err = o.CommitSettlement(ctx, stale); err == nil {
		t.Fatal("old cycle entry accepted")
	}
	// The committed receipt may be replayed across reset without a new award.
	cmd.UpdatedAt = stale.UpdatedAt
	if r, err := o.CommitSettlement(ctx, cmd); err != nil || !r.Replayed {
		t.Fatal(r, err)
	}
}

func TestGuildDungeonSettlementAtomicFailures(t *testing.T) {
	for _, kind := range []string{"late rollback", "full bag", "wrong guild", "rejoined", "policy changed", "wrong owner", "wrong completion", "missing item definition", "contribution overflow"} {
		t.Run(kind, func(t *testing.T) {
			repos, cmd := guildDungeonSettlementFixture(t)
			ctx := context.Background()
			switch kind {
			case "late rollback":
				original := repos.CharacterSettlement
				repos.CharacterSettlement = guildSettlementFault{original, original.(r.GuildSettlementUnitOfWork), false}
			case "full bag":
				inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
				inv.Slots = map[string]r.ItemStack{}
				for i := 121; i <= 152; i++ {
					inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: 222, Count: 1}
				}
				repos.Inventory.Save(ctx, inv)
			case "wrong guild":
				cmd.GuildDungeon.Entry.GuildID = 99
			case "rejoined":
				cmd.GuildDungeon.Entry.JoinedAt = cmd.UpdatedAt.Add(-time.Hour)
			case "policy changed":
				reg, _ := guild.Load(ctx, repos.Settings)
				reg.DungeonPolicy = &guild.DungeonPolicy{ResetDay: 1, ClearUnitsPerMember: 2}
				guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt)
			case "wrong owner":
				cmd.AccountID = "other"
			case "wrong completion":
				cmd.CompletionKey = "another"
			case "missing item definition":
				cmd.GuildDungeon.Items = nil
			case "contribution overflow":
				reg, _ := guild.Load(ctx, repos.Settings)
				m := reg.Guilds[1].Members[cmd.CharacterID]
				m.ContributionPoints = ^uint32(0)
				reg.Guilds[1].Members[cmd.CharacterID] = m
				guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt)
			}
			before, _, _ := repos.Character.Load(ctx, "c")
			invBefore, _, _ := repos.Inventory.Load(ctx, "c")
			regBefore, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			skillBefore, _, _ := repos.Skill.Load(ctx, "c")
			o, _ := NewOwner(repos)
			if _, err := o.CommitSettlement(ctx, cmd); err == nil {
				t.Fatal("invalid clear committed")
			}
			after, _, _ := repos.Character.Load(ctx, "c")
			invAfter, _, _ := repos.Inventory.Load(ctx, "c")
			regAfter, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			skillAfter, _, _ := repos.Skill.Load(ctx, "c")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(regBefore, regAfter) || !reflect.DeepEqual(skillBefore, skillAfter) {
				t.Fatal("failed clear partially committed")
			}
		})
	}
}

func TestGuildDungeonSettlementConcurrentDuplicate(t *testing.T) {
	repos, cmd := guildDungeonSettlementFixture(t)
	o, _ := NewOwner(repos)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := o.CommitSettlement(context.Background(), cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	reg, err := guild.Load(context.Background(), repos.Settings)
	if err != nil || reg.Guilds[1].Dungeon.Counts[8500] != 1 {
		t.Fatal("duplicate count", err)
	}
	inv, _, _ := repos.Inventory.Load(context.Background(), cmd.CharacterID)
	if inv.Slots["0:121"].Count != 3 {
		t.Fatal("duplicate items")
	}
}
