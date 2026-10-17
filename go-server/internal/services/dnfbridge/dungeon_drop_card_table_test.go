package dnfbridge

import "testing"

func TestIndependentCardTableAddsFixedCardsWithoutChangingBaseRows(t *testing.T) {
	source := bridgePVFSource{
		currentDungeonIndependentDropPath: "[independent drop]\n0 907 3015 1000000 1000000 1000000 1000000 1000000 1 1 1 1 1 0 0 -1 0\n[/independent drop]",
		"Etc/Independent_Drop_Card.etc":   "[independent drop]\n0 907 3610 1000000 1000000 1000000 1000000 1000000 1 1 1 1 1 0 0 -1 3\n[/independent drop]",
	}
	catalog, err := loadCurrentDungeonSpecialDrops(source)
	if err != nil {
		t.Fatal(err)
	}
	got := currentDungeonIndependentDropItems(catalog, 907, 0, 90, 0, newCurrentDungeonDropLCG(1))
	if len(got) != 2 || got[0] != 3015 || got[1] != 3610 {
		t.Fatalf("主表与专用卡片表未共同生效：%v", got)
	}
}

func TestIndependentCardTableRejectsMalformedRows(t *testing.T) {
	for _, text := range []string{
		"[independent drop]\n0 907 3610 1000000\n[/independent drop]",
		"[independent drop]\n0 907 0 1000000 1000000 1000000 1000000 1000000 1 1 1 1 1 0 0 -1 3\n[/independent drop]",
	} {
		if _, err := loadCurrentDungeonSpecialDrops(bridgePVFSource{"Etc/Independent_Drop_Card.etc": text}); err == nil {
			t.Fatalf("卡片配置损坏未拒绝：%s", text)
		}
	}
}
