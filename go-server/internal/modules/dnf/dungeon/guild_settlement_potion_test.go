package dungeon

import (
	"context"
	"reflect"
	"testing"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	"longheng.io/server/internal/modules/dnf/guild"
)

func TestGuildSettlementPreservesPotionBonusAcrossDurableReplay(t *testing.T) {
	ctx := context.Background()
	g, cmd := guildSettlementFixture(t)
	ch, _, err := g.Character.Load(ctx, cmd.CharacterID)
	if err != nil {
		t.Fatal(err)
	}
	if err := experiencebuff.Record(&ch, experiencebuff.Effect{Kind: experiencebuff.Clear, BasisPoints: 20000, DurationMillis: 60000}, 490003127, cmd.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := g.Character.Save(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cmd.ClearPotionBase = 100
	o, _ := NewOwner(g)
	first, err := o.CommitSettlement(ctx, cmd)
	if err != nil || first.PotionBonus != 200 || first.ExperienceGain != cmd.Experience+200 || first.GuildMileage.Amount != 10 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second := cmd
	second.CompletionKey += "-next"
	second.UpdatedAt = cmd.UpdatedAt.Add(2 * time.Minute)
	result, err := o.CommitSettlement(ctx, second)
	if err != nil || result.PotionBonus != 0 || result.GuildMileage.Amount != 10 {
		t.Fatalf("second=%+v err=%v", result, err)
	}
	before, _, _ := g.Character.Load(ctx, cmd.CharacterID)
	o, _ = NewOwner(g)
	cmd.UpdatedAt = second.UpdatedAt
	replay, err := o.CommitSettlement(ctx, cmd)
	if err != nil || !replay.Replayed || replay.PotionBonus != first.PotionBonus || replay.ExperienceGain != first.ExperienceGain || !reflect.DeepEqual(replay.Character, before) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	reg, err := guild.Load(ctx, g.Settings)
	if err != nil || reg.Guilds[1].Mileage != 20 {
		t.Fatalf("duplicate mileage: %+v %v", reg, err)
	}
}
