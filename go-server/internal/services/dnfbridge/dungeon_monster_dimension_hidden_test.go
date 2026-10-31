package dnfbridge

import (
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func TestParseDungeonMonsterDefinitionDimensionHiddenBoss(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{name: "missing", body: "[name]\n`ordinary`\n", want: false},
		{name: "zero", body: "[name]\n`ordinary`\n[dimension hidden boss]\n0\n", want: false},
		{name: "one", body: "[name]\n`iflius`\n[dimension hidden boss]\n1\n", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc, err := dnfpvf.Parse("monster/test.mob", test.body)
			if err != nil {
				t.Fatal(err)
			}
			got := parseDungeonMonsterDefinition(1, "monster/test.mob", doc)
			if got.DimensionHiddenBoss != test.want {
				t.Fatalf("DimensionHiddenBoss = %t, want %t", got.DimensionHiddenBoss, test.want)
			}
		})
	}
}
