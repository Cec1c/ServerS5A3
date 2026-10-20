package skill

import (
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	"testing"
)

func TestFixedLevelContainerFromQA39(t *testing.T) {
	// Minimal exact structure extracted from skill/swordman/hundredsword.skl.
	raw := "[required level]\n50\n[maximum level]\n40\n[fixed level skill]\n[interval level]\n5\n[add level per interval]\n1\n[is apply contract skillmaster]\n1\n[/fixed level skill]\n"
	doc, err := pvf.Parse("skill/swordman/hundredsword.skl", raw)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := parseSkill(0, 86, doc.Path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !definition.FixedLevelSkill || definition.FixedLevelForCharacter(49) != 0 || definition.FixedLevelForCharacter(90) != 9 {
		t.Fatalf("real container rule=%+v level90=%d", definition, definition.FixedLevelForCharacter(90))
	}
}
