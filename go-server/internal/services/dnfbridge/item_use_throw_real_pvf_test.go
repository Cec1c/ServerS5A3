package dnfbridge

import (
	"os"
	"testing"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFThrowConsumableAudit(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("set DNFBRIDGE_REAL_PVF_SMOKE")
	}
	archive, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(archive)
	if err != nil {
		t.Fatal(err)
	}
	text, err := archive.ReadText(dungeonDropStackableList)
	if err != nil {
		t.Fatal(err)
	}
	document, err := dnfpvf.Parse(dungeonDropStackableList, text)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range dnfpvf.ParseList(document) {
		item, err := catalog.ResolveItem(uint32(entry.ID))
		if err != nil || item.StackableType != "[throw]" {
			continue
		}
		count++
		definition, err := archive.ReadText(item.PVFPath)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := dnfpvf.Parse(item.PVFPath, definition)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := doc.Text("name")
		t.Logf("throw consumable id=%d name=%s path=%s", entry.ID, name, item.PVFPath)
	}
	item, err := catalog.ResolveItem(6004)
	if err != nil || item.PVFPath != "stackable/throw/berry_sky.stk" || item.StackableType != "[throw]" {
		t.Fatalf("sky fruit=%+v %v", item, err)
	}
	if count < 2 {
		t.Fatalf("throw family audit found only %d", count)
	}
	t.Logf("throw family audit: %d items", count)
}
