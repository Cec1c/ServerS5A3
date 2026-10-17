package chronicle

import (
	"fmt"
	"testing"
)

func TestEquipmentDurabilityFollowsPVFTypeAndDeclaration(t *testing.T) {
	for _, typ := range []string{"amulet", "wrist", "ring", "support", "magic stone", "weapon", "coat", "shoulder", "pants", "shoes", "waist"} {
		for _, field := range []string{"", "[durability] 40\n", "[durability] `bad`\n", "[durability] -1\n", "[durability] 65536\n"} {
			t.Run(fmt.Sprintf("%s/%q", typ, field), func(t *testing.T) {
				source := testSource{"target.equ": fmt.Sprintf("[rarity] 5\n[equipment type] `[%s]` 20\n%s", typ, field)}
				catalog := NewCatalog(source, map[int64]Reference{1: {Path: "target.equ", Equipment: true}})
				e, err := catalog.Equipment(1)
				noDur := typ == "amulet" || typ == "wrist" || typ == "ring" || typ == "support" || typ == "magic stone"
				allowed := field == "[durability] 40\n" || (field == "" && noDur)
				if (err == nil) != allowed {
					t.Fatalf("type=%s field=%q result=%+v err=%v", typ, field, e, err)
				}
				if err == nil && ((field == "" && e.Durability != 0) || (field != "" && e.Durability != 40)) {
					t.Fatalf("PVF durability changed: %+v", e)
				}
			})
		}
	}
}
