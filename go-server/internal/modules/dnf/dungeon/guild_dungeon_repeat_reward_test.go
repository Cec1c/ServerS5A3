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

func TestGuildDungeonRepeatRewardAtomicFailures(t *testing.T) {
	for _, kind := range []string{"full bag", "late rollback", "missing box definition", "invalid balance", "changed policy"} {
		t.Run(kind, func(t *testing.T) {
			repos, cmd := repeatRewardFixture(t)
			ctx := context.Background()
			seedRepeatProgress(t, repos, cmd.CharacterID, 90)
			original := repos.CharacterSettlement
			validBox := cmd.GuildDungeon.Items[10156320]
			switch kind {
			case "full bag":
				inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
				inv.Slots = map[string]r.ItemStack{"0:121": {ItemID: 10154439, Count: 3}}
				for i := 122; i <= 152; i++ {
					inv.Slots[fmt.Sprintf("0:%d", i)] = r.ItemStack{ItemID: 222, Count: 1}
				}
				if err := repos.Inventory.Save(ctx, inv); err != nil {
					t.Fatal(err)
				}
			case "late rollback":
				repos.CharacterSettlement = guildSettlementFault{original, original.(r.GuildSettlementUnitOfWork), false}
			case "missing box definition":
				delete(cmd.GuildDungeon.Items, 10156320)
			case "invalid balance":
				seedRepeatProgress(t, repos, cmd.CharacterID, 100)
			case "changed policy":
				reg, _ := guild.Load(ctx, repos.Settings)
				reg.DungeonPolicy = &guild.DungeonPolicy{ResetDay: 1, ResetHour: 6, UTCOffsetMinutes: 480, ClearUnitsPerMember: 1, RepeatRewardPercent: 20}
				if err := guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt); err != nil {
					t.Fatal(err)
				}
			}
			before, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
			invBefore, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			regBefore, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			skillBefore, _, _ := repos.Skill.Load(ctx, cmd.CharacterID)
			o, _ := NewOwner(repos)
			if _, err := o.CommitSettlement(ctx, cmd); err == nil {
				t.Fatal("invalid settlement committed")
			}
			after, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
			invAfter, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
			regAfter, _, _ := repos.Settings.Load(ctx, guild.RegistryScope)
			skillAfter, _, _ := repos.Skill.Load(ctx, cmd.CharacterID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invBefore, invAfter) || !reflect.DeepEqual(regBefore, regAfter) || !reflect.DeepEqual(skillBefore, skillAfter) {
				t.Fatal("partial transaction survived")
			}
			_, found, err := repos.Settings.Load(ctx, r.GuildSettlementReceiptScope(cmd.GuildDungeon.Entry.Owner, cmd.CompletionKey))
			if err != nil || found {
				t.Fatal("failed clear wrote receipt", err)
			}
			// Repair only the injected fault, then retry the same completion key.
			repos.CharacterSettlement = original
			cmd.GuildDungeon.Items[10156320] = validBox
			if kind == "full bag" {
				delete(invAfter.Slots, "0:152")
				if err := repos.Inventory.Save(ctx, invAfter); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "invalid balance" {
				seedRepeatProgress(t, repos, cmd.CharacterID, 90)
			}
			if kind == "changed policy" {
				reg, _ := guild.Load(ctx, repos.Settings)
				reg.DungeonPolicy = nil
				if err := guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt); err != nil {
					t.Fatal(err)
				}
			}
			o, _ = NewOwner(repos)
			got, err := o.CommitSettlement(ctx, cmd)
			if err != nil || !got.GuildDungeon.RepeatReward.Awarded || got.Character.Stats[guild.DungeonRepeatRewardProgressStat] != 0 {
				t.Fatal("repaired retry", got.GuildDungeon, err)
			}
		})
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
		go func() { defer wg.Done(); o, _ := NewOwner(repos); _, err := o.CommitSettlement(ctx, cmd); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	inv, _, _ := repos.Inventory.Load(ctx, cmd.CharacterID)
	boxes := int64(0)
	for _, item := range inv.Slots {
		if item.ItemID == 10156320 {
			boxes += item.Count
		}
	}
	c, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	reg, _ := guild.Load(ctx, repos.Settings)
	if boxes != 1 || c.Stats[guild.DungeonRepeatRewardProgressStat] != 0 || reg.Guilds[1].Dungeon.Counts[8500] != 1 {
		t.Fatal(boxes, c.Stats, reg.Guilds[1].Dungeon)
	}
}

func TestGuildDungeonRepeatRewardCharacterIsolationAndNewMonth(t *testing.T) {
	repos, cmd := repeatRewardFixture(t)
	ctx := context.Background()
	seedRepeatProgress(t, repos, cmd.CharacterID, 80)
	reg, _ := guild.Load(ctx, repos.Settings)
	peer, _, _ := repos.Character.Load(ctx, cmd.CharacterID)
	peer.CharacterID = "peer"
	peer.Name = "Peer"
	peer.Stats[guild.DungeonRepeatRewardProgressStat] = 0
	if err := repos.Character.Save(ctx, peer); err != nil {
		t.Fatal(err)
	}
	skill, _, _ := repos.Skill.Load(ctx, cmd.CharacterID)
	skill.CharacterID = peer.CharacterID
	if err := repos.Skill.Save(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.Save(ctx, r.InventoryRecord{CharacterID: peer.CharacterID}); err != nil {
		t.Fatal(err)
	}
	m := reg.Guilds[1].Members[cmd.CharacterID]
	m.CharacterID = peer.CharacterID
	m.Name = peer.Name
	m.Grade = 4
	reg.Guilds[1].Members[peer.CharacterID] = m
	if err := guild.Save(ctx, repos.Settings, reg, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	owners := []r.GuildOwner{cmd.GuildDungeon.Entry.Owner, {AccountID: peer.AccountID, CharacterID: peer.CharacterID}}
	entries, err := guild.PrepareDungeonEntries(reg, cmd.GuildDungeon.Catalog, 8500, owners, "shared-party", cmd.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		pc := cmd
		clear := *cmd.GuildDungeon
		clear.Entry = e
		clear.PartySize = 2
		pc.GuildDungeon = &clear
		pc.CharacterID = e.Owner.CharacterID
		pc.CompletionKey = e.CompletionKey()
		o, _ := NewOwner(repos)
		res, err := o.CommitSettlement(ctx, pc)
		want := int64(90)
		if i == 1 {
			want = 10
		}
		if err != nil || res.Character.Stats[guild.DungeonRepeatRewardProgressStat] != want {
			t.Fatal(i, res.Character.Stats, err)
		}
	}
	// Reset only the guild's monthly count. Personal 90 percent survives.
	cmd.UpdatedAt = time.Unix(int64(entries[0].Cycle.EndUnix), 0)
	reg, _ = guild.Load(ctx, repos.Settings)
	fresh, err := guild.PrepareDungeonEntries(reg, cmd.GuildDungeon.Catalog, 8500, owners[:1], "next-month", cmd.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	cmd.GuildDungeon.Entry = fresh[0]
	cmd.CompletionKey = fresh[0].CompletionKey()
	o, _ := NewOwner(repos)
	res, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !res.GuildDungeon.RepeatReward.Awarded || res.GuildDungeon.Count != 1 {
		t.Fatal(res.GuildDungeon, err)
	}
	peer, _, _ = repos.Character.Load(ctx, peer.CharacterID)
	if peer.Stats[guild.DungeonRepeatRewardProgressStat] != 10 {
		t.Fatal("peer progress changed")
	}
}

func TestGuildDungeonRepeatRewardPersistsAndGrantsOnce(t *testing.T) {
	repos, cmd := repeatRewardFixture(t)
	ctx := context.Background()
	owner, _ := NewOwner(repos)
	for n := 1; n <= 20; n++ {
		cmd.GuildDungeon.Entry.RunID = fmt.Sprintf("repeat-%d", n)
		cmd.CompletionKey = cmd.GuildDungeon.Entry.CompletionKey()
		result, err := owner.CommitSettlement(ctx, cmd)
		if err != nil {
			t.Fatal(n, err)
		}
		if got := result.Character.Stats["guild_dungeon_repeat_reward_progress"]; got != int64(n%10*10) {
			t.Fatalf("clear %d progress=%d", n, got)
		}
		boxes := int64(0)
		for _, item := range result.Inventory.Slots {
			if item.ItemID == 10156320 {
				boxes += item.Count
			}
		}
		if boxes != int64(n/10) {
			t.Fatalf("clear %d boxes=%d", n, boxes)
		}
		replay, err := owner.CommitSettlement(ctx, cmd)
		if err != nil || !replay.Replayed || replay.Character.Stats["guild_dungeon_repeat_reward_progress"] != result.Character.Stats["guild_dungeon_repeat_reward_progress"] {
			t.Fatal("replay", n, err)
		}
	}
}
