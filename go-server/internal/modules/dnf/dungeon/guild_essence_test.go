package dungeon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildEssenceSettlementDurableReplayAndRollback(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	c, err := guild.ParseEssenceCatalog("[daily max ap]\n5000\n[ap reward]\n10 15 15 30 0\n[/ap reward]")
	if err != nil {
		t.Fatal(err)
	}
	cmd.GuildEssence = c
	cmd.GuildEssenceRevalidate = func() bool { return true }
	o, _ := NewOwner(g)
	first, err := o.CommitSettlement(ctx, cmd)
	if err != nil || first.GuildEssence.AP != 15 {
		t.Fatal(first.GuildEssence, err)
	}
	second := cmd
	second.CompletionKey = "essence-second"
	if _, err = o.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	o, _ = NewOwner(g)
	replay, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || replay.GuildEssence != first.GuildEssence {
		t.Fatal(replay.GuildEssence, err)
	}
	reg, _ := guild.Load(ctx, g.Settings)
	if reg.Guilds[1].Essence.AP != 30 || reg.Guilds[1].Essence.GP != 0 {
		t.Fatal(reg.Guilds[1].Essence)
	}
	before, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
	beforeChar, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	third := cmd
	third.CompletionKey = "essence-rollback"
	calls := 0
	third.GuildEssenceRevalidate = func() bool { calls++; return calls < 2 }
	if _, err = o.CommitSettlement(ctx, third); !errors.Is(err, ErrSettlementStateInvalid) {
		t.Fatal(err)
	}
	after, _, _ := g.Settings.Load(ctx, guild.RegistryScope)
	afterChar, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeChar, afterChar) {
		t.Fatal("late failure committed")
	}
	third.GuildEssenceRevalidate = func() bool { return true }
	res, err := o.CommitSettlement(ctx, third)
	if err != nil || res.GuildEssence.AP != 15 || res.Replayed {
		t.Fatal("retry", res.GuildEssence, err)
	}
}

func TestGuildEssenceOldReceiptDoesNotBackfill(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	o, _ := NewOwner(g)
	if _, err := o.CommitSettlement(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.GuildEssence, _ = guild.ParseEssenceCatalog("[daily max ap]\n5000\n[ap reward]\n10 15 15 30 0\n[/ap reward]")
	cmd.GuildEssenceRevalidate = func() bool { return true }
	res, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !res.Replayed || res.GuildEssence.AP != 0 {
		t.Fatal(res.GuildEssence, err)
	}
}
