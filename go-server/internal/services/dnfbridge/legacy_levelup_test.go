package dnfbridge

import (
	"context"
	"os"
	"testing"
	"time"

	dnfonlineevent "longheng.io/server/internal/modules/dnf/onlineevent"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestPortedLegacyLevelRealPVFRewards(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("real PVF smoke path is unset")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := dnfonlineevent.LoadLegacyLevelCatalog(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Main) != 6 || len(catalog.Stage) != 8 {
		t.Fatalf("catalog=%+v", catalog)
	}
	service := &Service{initialEquipmentArchive: archive}
	items, err := service.currentPVFItemCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range append(catalog.Main, catalog.Stage...) {
		item, err := items.ResolveItem(uint32(row.Item.ItemID))
		if err == nil {
			item, err = currentPVFItemDefinitionForGrantAt(item, time.Now())
		}
		if err != nil || item.Kind != dungeonDropItemStackable || (!item.ExpirationDate.IsZero() && !item.ExpirationDate.After(time.Now())) {
			t.Fatalf("reward=%+v item=%+v err=%v", row, item, err)
		}
	}
}
