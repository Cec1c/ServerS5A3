package dungeon

import (
	"context"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	"longheng.io/server/internal/modules/dnf/guild"
	"reflect"
	"testing"
	"time"
)

func TestGuildPotionDurableReceiptPreservesBonusAcrossLaterClear(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	ch, _, err := g.Character.Load(ctx, cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := experiencebuff.Record(&ch, experiencebuff.Effect{Kind: experiencebuff.Clear, BasisPoints: 10000, DurationMillis: 60000}, 7440, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cmd.ClearPotionBase = 10
	owner, _ := NewOwner(g)
	first, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || first.PotionBonus != 10 || first.GuildMileage.Amount != 10 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second := cmd
	second.CompletionKey = "guild-potion-later"
	second.UpdatedAt = cmd.UpdatedAt.Add(time.Hour)
	if _, err := owner.CommitSettlement(ctx, second); err != nil {
		t.Fatal(err)
	}
	before, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	owner, _ = NewOwner(g)
	cmd.UpdatedAt = second.UpdatedAt.Add(time.Hour)
	replay, err := owner.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || replay.PotionBonus != first.PotionBonus || replay.ExperienceGain != first.ExperienceGain || replay.GuildMileage != first.GuildMileage || !reflect.DeepEqual(replay.Character, before) {
		t.Fatalf("durable replay lost potion or changed assets: %+v %v", replay, err)
	}
	reg, err := guild.Load(ctx, g.Settings)
	if err != nil || reg.Guilds[1].Mileage != 20 {
		t.Fatalf("repeated guild credit %+v %v", reg, err)
	}
}
