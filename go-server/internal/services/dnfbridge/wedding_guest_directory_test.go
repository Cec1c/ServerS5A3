package dnfbridge

import (
	"context"
	"testing"

	"longheng.io/server/internal/modules/dnf/guild"
)

func TestWeddingNamedGuestGuildDirectoryRevalidatesOwnership(t *testing.T) {
	for _, mode := range []string{"same-guild", "other-guild", "source-account", "target-account", "departed", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s, _, repos := guildTextFixture(t)
			ctx := context.Background()
			source, _, err := repos.Character.Load(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			targetID := "2"
			switch mode {
			case "other-guild":
				targetID = "3"
			case "source-account":
				source.AccountID = "foreign"
			case "target-account":
				target, _, err := repos.Character.Load(ctx, "2")
				if err != nil {
					t.Fatal(err)
				}
				target.AccountID = "changed"
				if err := repos.Character.Save(ctx, target); err != nil {
					t.Fatal(err)
				}
			case "departed":
				reg, err := guild.Load(ctx, repos.Settings)
				if err != nil {
					t.Fatal(err)
				}
				delete(reg.Guilds, 1)
				if err := guild.Save(ctx, repos.Settings, reg, s.gameplayNow()); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				record, _, err := repos.Settings.Load(ctx, guild.RegistryScope)
				if err != nil {
					t.Fatal(err)
				}
				record.Values["state"] = "{invalid"
				if err := repos.Settings.Save(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			if got := s.currentWeddingNamedGuestAllowed(ctx, source, targetID); got != (mode == "same-guild") {
				t.Fatal(mode, got)
			}
		})
	}
}
