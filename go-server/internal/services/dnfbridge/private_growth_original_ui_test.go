package dnfbridge

import (
	"fmt"
	"os"
	"strings"
	"testing"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

// These native display templates are never exchanged for or inserted into
// player inventories. Check every dynamically selected native coupon definition.
func TestPrivateGrowthOriginalUIDisplayTemplates(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired candidate PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	for kind := int64(0); kind < 5; kind++ {
		for level := int64(1); level <= 31; level++ {
			id := int64(990034000) + kind*100 + level
			t.Run(fmt.Sprint(id), func(t *testing.T) {
				d, err := catalog.ResolveItem(uint32(id))
				if err != nil {
					t.Fatal(err)
				}
				if d.Kind != dungeonDropItemStackable || d.AttachType != "[account]" || !strings.HasPrefix(d.PVFPath, "stackable/private_seria_growth_ui/") {
					t.Fatalf("native definition identity: %+v", d)
				}
				mode := "reinforce"
				if kind > 0 {
					mode = "amplify"
				}
				r, err := resolveCurrentUpgradeTicketMetadata(catalog, a, id, 101040493)
				if err != nil || r.TicketMode != mode || r.TargetLevel != level || r.SuccessWeight != 100000 || r.TicketRandom || r.TargetKind != "equipment" {
					t.Fatalf("native display level/probability mismatch: %+v / %v", r, err)
				}
				if !strings.Contains(d.Name, fmt.Sprintf("+%d ", level)) {
					t.Fatal("display name level mismatch", d.Name)
				}
			})
		}
	}
}
