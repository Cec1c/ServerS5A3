package dnfbridge

import (
	"context"
	repo "longheng.io/server/internal/modules/dnf/repository"
	town "longheng.io/server/internal/modules/dnf/town"
	"testing"
)

func channelSpawnReturnFixture(t *testing.T, table *town.Table) (*Service, *gameSession, repo.Group) {
	t.Helper()
	if table == nil {
		var err error
		table, err = town.Load(context.Background(), townMoveTestSource{
			"town/town.lst":     "10 `pvp.twn` 19 `anton.twn` 38 `ordinary.twn` 39 `hendon.twn` 98 `fiend.twn`",
			"town/pvp.twn":      "[area]\n0 `Fair_PVP/Gate_Hendon.map` `[gate]` 474 234\n[/area]\n",
			"town/anton.twn":    "[area]\n0 `Common/(R)Gate_Seria.map` `[gate]` 400 240\n[/area]\n",
			"town/ordinary.twn": townSeriaFixtureElvengard,
			"town/hendon.twn":   "[area]\n0 `new_HendonMyre.map` `[normal]`\n[/area]\n",
			"town/fiend.twn":    "[area]\n0 `GContents/180621_Fiendwar/Town/Gate_Fiend.map` `[gate]` 474 234\n[/area]\n",
		}, town.Options{})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, p, repos := pvpReturnTownFixture(t, table)
	s.options.currentClientVersion = "official-24.9"
	return s, p, repos
}
