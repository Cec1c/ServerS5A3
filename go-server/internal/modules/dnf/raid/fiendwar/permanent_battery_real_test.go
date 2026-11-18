package fiendwar

import (
	"os"
	"testing"
	"time"

	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealFiendWarBatteryStaysClearedThroughBattle(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires paired private PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadFiendWarConfig(a)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFiendWarBattle(c.Battle)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = b.Apply(FiendWarEvent{}, now); err != nil {
		t.Fatal(err)
	}
	area := 0
	for id, row := range c.Battle.Areas {
		if row.Kind == "battery" {
			area = id
		}
	}
	if area == 0 || !b.AreaOpen(area) {
		t.Fatal("paired battery is not initially available")
	}
	b.GroupAreas[1] = area
	if err = b.Enter(1, c.Battle.Areas[area].Dungeon, now); err != nil {
		t.Fatal(err)
	}
	if err = b.Clear(c.Battle.Areas[area].Dungeon, now); err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{0, 59 * time.Second, 61 * time.Second, 180 * time.Second} {
		if err = b.Tick(now.Add(elapsed)); err != nil {
			t.Fatal(err)
		}
		if b.AreaOpen(area) || b.Value("battery state") != 1 {
			t.Fatal("cleared battery respawned", elapsed, b.Value("battery state"))
		}
	}
	clone := b.Clone()
	if clone.AreaOpen(area) || clone.Value("battery state") != 1 {
		t.Fatal("projection clone lost the clear")
	}
}
