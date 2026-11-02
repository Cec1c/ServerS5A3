package dnfbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/experienceitem"
	"longheng.io/server/internal/modules/dnf/progression"
	"longheng.io/server/internal/modules/dnf/pvf"
	platformpvf "longheng.io/server/internal/platform/pvf"
)

func TestRealPVFAllExperienceCapsuleDefinitions(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.ReadText("stackable/stackable.lst")
	if err != nil {
		t.Fatal(err)
	}
	list, err := pvf.Parse("stackable/stackable.lst", raw)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[experienceitem.CapsuleKind]int{}
	for _, entry := range pvf.ParseList(list) {
		p := "stackable/" + strings.TrimPrefix(entry.Path, "stackable/")
		text, err := a.ReadText(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "[increase status type]") || (!strings.Contains(text, "[expUp") && !strings.Contains(text, "[expPercentUp]")) {
			continue
		}
		doc, err := pvf.Parse(p, text)
		if err != nil {
			t.Fatal(err)
		}
		d, found, err := experienceitem.ParseCapsule(int64(entry.ID), doc)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", p, found, err)
		}
		counts[d.Kind]++
		for level := max(1, d.MinimumLevel); level <= min(89, d.MaximumLevel); level++ {
			gain, err := d.Gain(tables, level)
			if err != nil || gain == 0 {
				t.Fatalf("%s level%d gain%d err%v", p, level, gain, err)
			}
		}
	}
	// The archive also has an unused fourth crack capsule script (10146849),
	// which is absent from stackable.lst. Coverage follows registered items.
	if counts[experienceitem.CapsuleFixed] < 100 || counts[experienceitem.CapsulePercent] < 400 || counts[experienceitem.CapsuleTable] < 10 || counts[experienceitem.CapsuleCrack] < 3 {
		t.Fatalf("missing definitions: %v", counts)
	}
	t.Logf("real capsule definitions: %v", counts)
}

func TestRealPVFActivityFiveAndFifteenCapsules(t *testing.T) {
	path := os.Getenv("DNFBRIDGE_REAL_PVF_SMOKE")
	if path == "" {
		t.Skip("requires actual PVF")
	}
	a, err := platformpvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newPVFDungeonDropCatalog(a)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := progression.Load(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	for id, percent := range map[int64]uint32{490008922: 5, 490008923: 15} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			ctx := context.Background()
			s, p, g := experienceCapsuleBridgeFixture(t, experienceCapsuleBridgeFiveID, 50, 1)
			item, err := catalog.ResolveItem(uint32(id))
			if err != nil {
				t.Fatal(err)
			}
			before, err := tables.ThresholdToNext(49)
			if err != nil {
				t.Fatal(err)
			}
			next, err := tables.ThresholdToNext(50)
			if err != nil {
				t.Fatal(err)
			}
			c, _, _ := g.Character.Load(ctx, "29")
			c.Stats["exp"] = int64(before)
			if err = g.Character.Save(ctx, c); err != nil {
				t.Fatal(err)
			}
			bag, _, _ := g.Inventory.Load(ctx, "29")
			stack := bag.Slots["0:9"]
			stack.ItemID = id
			stack.Extra["pvf_path"] = item.PVFPath
			bag.Slots["0:9"] = stack
			if err = g.Inventory.Save(ctx, bag); err != nil {
				t.Fatal(err)
			}
			s.pvfItemCatalog = catalog
			s.initialEquipmentArchive = a
			packets := experienceCapsuleBridgeCall(t, s, p)
			want := before + uint32(uint64(next-before)*uint64(percent)/100)
			experienceCapsuleBridgeAssertSuccess(t, packets, 50, want)
			after, _, err := g.Character.Load(ctx, "29")
			if err != nil || after.Stats["exp"] != int64(want) {
				t.Fatalf("character=%+v err=%v", after, err)
			}
			bag, _, err = g.Inventory.Load(ctx, "29")
			if err != nil || bag.Slots["0:9"].Count != 1 {
				t.Fatalf("bag=%+v err=%v", bag, err)
			}
		})
	}
}
