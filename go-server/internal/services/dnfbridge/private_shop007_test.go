package dnfbridge

import (
	"encoding/json"
	platformpvf "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

// Run against the candidate archive, without starting the game server or
// touching any player database. The expected audit is independently reviewed.
func TestPrivateShop007RuntimeCatalog(t *testing.T) {
	path := os.Getenv("PRIVATE_SHOP007_PVF")
	auditPath := os.Getenv("PRIVATE_SHOP007_AUDIT")
	if path == "" || auditPath == "" {
		t.Skip("007 candidate and audit required")
	}
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ Commodity, Item, Price uint32 }
	var audit struct {
		DisplayOrder       []row
		RemovedDirectSales []row
	}
	if err = json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	archive, err := platformpvf.LoadArchive(platformpvf.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFCeraShopCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.DisplayOrder) != 1195 || len(audit.RemovedDirectSales) != 5 {
		t.Fatal("audit scope changed")
	}
	for _, r := range audit.DisplayOrder {
		p, ok := catalog.products[r.Commodity]
		if !ok || p.ItemID != r.Item || p.CeraPrice != r.Price || p.Section != "package" {
			t.Fatalf("runtime commodity %d: %+v vs %+v", r.Commodity, p, r)
		}
	}
	for _, r := range audit.RemovedDirectSales {
		if _, ok := catalog.products[r.Commodity]; ok {
			t.Fatalf("removed commodity still purchasable %d", r.Commodity)
		}
	}
	oldPath := os.Getenv("PRIVATE_SHOP007_BASELINE")
	if oldPath == "" {
		t.Fatal("baseline required")
	}
	oldArchive, err := platformpvf.LoadArchive(platformpvf.Options{Path: oldPath})
	if err != nil {
		t.Fatal(err)
	}
	old, err := newPVFCeraShopCatalog(oldArchive)
	if err != nil {
		t.Fatal(err)
	}
	nonpackage := 0
	for id, p := range old.products {
		if p.Section != "package" {
			if !reflect.DeepEqual(catalog.products[id], p) {
				t.Fatalf("non-package commodity changed %d", id)
			}
			nonpackage++
		}
	}
	if len(old.products)-len(catalog.products) != 5 || !reflect.DeepEqual(old.bonusByCommodity, catalog.bonusByCommodity) || !reflect.DeepEqual(old.festivals, catalog.festivals) {
		t.Fatal("catalog count or gift rules changed")
	}
	t.Logf("Verified 1195 runtime package prices, 5 removed direct sales, %d other products and all festival/bonus definitions unchanged", nonpackage)
}
