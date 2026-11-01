package skill

import (
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"testing"
)

func TestBattlePassColorlessClassificationUsesDungeonDefinition(t *testing.T) {
	for _, row := range []struct {
		text string
		want bool
	}{
		{"[consume item]\n3037 2 2\n[/consume item]\n[pvp]\n[consume item]\n[/consume item]\n[/pvp]", true},
		{"[consume item]\n3038 2 2\n[/consume item]", false},
		{"[consume item]\n3037 0 0\n[/consume item]", false},
		{"[consume item]\n3037 2\n[/consume item]", false},
		{"[pvp]\n[consume item]\n3037 1 1\n[/consume item]\n[/pvp]", false},
		{"[consume item]\n3037 1 1\n[/consume item]\n[dungeon]\n[consume item]\n[/consume item]\n[/dungeon]", false},
	} {
		doc, err := dnfpvf.Parse("skill/test.skl", row.text)
		if err != nil {
			t.Fatal(err)
		}
		if got := consumesColorlessCube(doc); got != row.want {
			t.Fatal(row, got)
		}
	}
}
