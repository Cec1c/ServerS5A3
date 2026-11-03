package main

import (
	"bytes"
	script "longheng.io/server/internal/modules/dnf/pvf"
	archive "longheng.io/server/internal/platform/pvf"
	"os"
	"reflect"
	"testing"
)

func TestOfficialArchiveRepairsOnlyMinimapAndShopRegistration(t *testing.T) {
	p := os.Getenv("PUBLIC_REQUIEM_PVF")
	if p == "" {
		t.Skip("official snapshot required")
	}
	before, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	out, n, e := repair(before)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := archive.OpenBytes(before)
	b, e := archive.OpenBytes(out)
	if e != nil {
		t.Fatal(e)
	}
	if n != a.FileCount()-2 || a.FileCount() != b.FileCount() {
		t.Fatal("unexpected file counts")
	}
	raw, e := b.ReadRaw(minimapPath)
	if e != nil {
		t.Fatal(e)
	}
	ts, e := tokens(raw, b.ResolveString)
	if e != nil {
		t.Fatal(e)
	}
	oldRaw, e := a.ReadRaw(minimapPath)
	if e != nil {
		t.Fatal(e)
	}
	oldTokens, e := tokens(oldRaw, a.ResolveString)
	if e != nil {
		t.Fatal(e)
	}
	oldIDs, e := townIDs(oldTokens)
	if e != nil {
		t.Fatal(e)
	}
	ids, e := townIDs(ts)
	if e != nil || !reflect.DeepEqual(ids, append([]int32{13}, oldIDs...)) {
		t.Fatalf("town ids=%v err=%v", ids, e)
	}
	text, e := b.ReadText(listPath)
	if e != nil {
		t.Fatal(e)
	}
	doc, e := script.Parse(listPath, text)
	if e != nil {
		t.Fatal(e)
	}
	found := 0
	for _, v := range script.ParseList(doc) {
		if v.ID == 63 {
			found++
			if v.Path != "Iris2.shp" {
				t.Fatal(v)
			}
		}
	}
	if found != 1 {
		t.Fatal("shop63 not unique")
	}
	for _, path := range []string{"map/town/timegaterequiem/timegaterequiemwr.map", "npc/iris2.npc", shopPath, "etc/itemdropinfo_monster_hell.etc", "etc/itemdropinfo_monseter.etc", "town/town.lst", "etc/sequential_dungeon_info.etc"} {
		x, e := a.ReadRaw(path)
		if e != nil {
			t.Fatal(e)
		}
		y, e := b.ReadRaw(path)
		if e != nil || !bytes.Equal(x, y) {
			t.Fatalf("unrelated resource changed: %s", path)
		}
	}
	if _, _, e := repair(out); e == nil {
		t.Fatal("repeated application accepted")
	}
	t.Logf("Preserved %d archive files; official drop settings, products, NPCs and sequence unchanged", n)
}
