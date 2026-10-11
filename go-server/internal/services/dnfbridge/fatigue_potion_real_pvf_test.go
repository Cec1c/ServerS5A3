package dnfbridge

import (
	doc "longheng.io/server/internal/modules/dnf/pvf"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"testing"
)

func TestRealPVFFatiguePotionContracts(t *testing.T) {
	source := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if source == "" {
		t.Skip("set real PVF smoke source")
	}
	a, e := archive.Open(source)
	if e != nil {
		t.Fatal(e)
	}
	text, e := a.ReadText(dungeonDropStackableList)
	if e != nil {
		t.Fatal(e)
	}
	list, e := doc.Parse(dungeonDropStackableList, text)
	if e != nil {
		t.Fatal(e)
	}
	count, limited, maintained := 0, 0, 0
	for _, entry := range doc.ParseList(list) {
		path := "stackable/" + entry.Path
		text, e := a.ReadText(path)
		if e != nil {
			continue
		}
		d, e := doc.Parse(path, text)
		if e != nil {
			continue
		}
		definition, found := fatiguePotionDefinition(entry.ID, path, d)
		if !found {
			continue
		}
		count++
		if definition.DailyLimit > 0 {
			limited++
		}
		if definition.CoolTimeMS >= 22*3600*1000 {
			maintained++
		}
		name, _ := d.Text("name")
		t.Logf("id=%d name=%s points=%d daily=%d cooldown_ms=%d group=%s", entry.ID, name, definition.Points, definition.DailyLimit, definition.CoolTimeMS, definition.Group)
	}
	if count == 0 || limited == 0 || maintained == 0 {
		t.Fatalf("incomplete potion audit count=%d explicit_daily=%d long_cooldown=%d", count, limited, maintained)
	}
	t.Logf("fatigue potion audit count=%d explicit_daily=%d maintained_daily=%d", count, limited, maintained)
}
