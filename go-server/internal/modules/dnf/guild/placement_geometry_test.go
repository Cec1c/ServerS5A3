package guild

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"testing"

	"longheng.io/server/internal/modules/dnf/pvf"
)

func placementTestMap(t *testing.T) *PlacementMap {
	t.Helper()
	m, err := NewPlacementMap(0, []PlacementRect{{750, 210, 500, 160}, {290, 370, 900, 450}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func placementTestBook(w, h int) PlacementBook {
	b := PlacementBook{ItemID: 10156101, ObjectID: 71021, Maintenance: 15, Width: w, Height: h, Mask: make([]uint32, w*h)}
	for i := range b.Mask {
		b.Mask[i] = 1
	}
	return b
}

func TestGuildPlacementMapMatchesNativeEntireGrid(t *testing.T) {
	m := placementTestMap(t)
	if m.OriginX != 290 || m.OriginY != 210 || m.Width != 960 || m.Height != 610 || len(m.cells) != 1011 {
		t.Fatalf("grid: %+v cells=%d", m, len(m.cells))
	}
	cells := make([]PlacementCell, 0, len(m.cells))
	for c := range m.cells {
		cells = append(cells, c)
	}
	slices.SortFunc(cells, func(a, b PlacementCell) int {
		if a.X != b.X {
			return a.X - b.X
		}
		return a.Y - b.Y
	})
	h := sha256.New()
	row := make([]byte, 8)
	for _, c := range cells {
		binary.LittleEndian.PutUint32(row, uint32(c.X))
		binary.LittleEndian.PutUint32(row[4:], uint32(c.Y))
		h.Write(row)
	}
	// Original V176 03C502D0 builds this exact set from the two PVF rectangles.
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "122819b35928904f32990f3e1c45ba9efdafbba026be7818e9b8576b5b304817" {
		t.Fatalf("native grid digest: %s", got)
	}
}

func TestGuildPlacementNativeSenderCenterAndBounds(t *testing.T) {
	m := placementTestMap(t)
	book := placementTestBook(3, 3)
	cells, err := m.CellsAt(book, 902, 276)
	if err != nil || len(cells) != 9 {
		t.Fatalf("native 837 center: %v %v", cells, err)
	}
	for _, p := range [][2]int{{903, 276}, {902, 277}, {0, 0}, {32767, 32767}, {400, 240}} {
		if _, err = m.CellsAt(book, p[0], p[1]); err == nil {
			t.Fatalf("accepted invalid center %v", p)
		}
	}
	book.Mask[4] = 0
	if cells, err = m.CellsAt(book, 902, 276); err != nil || len(cells) != 8 {
		t.Fatalf("hollow mask: %v %v", cells, err)
	}
	// Every footprint shape present in the matching merchant catalog.
	for _, wh := range [][2]int{{3, 3}, {6, 4}, {4, 4}, {8, 6}, {7, 6}, {2, 2}} {
		b := placementTestBook(wh[0], wh[1])
		x := 290 + 20*23 + (b.Width*36+b.Height*23)/2
		y := 210 + 20*12 + b.Height*12/2
		if _, err = m.CellsAt(b, x, y); err != nil {
			t.Fatalf("footprint %v: %v", wh, err)
		}
	}
}

func TestGuildPlacementCatalogChecksActualActionAndMask(t *testing.T) {
	stk, _ := pvf.Parse("book.stk", "[stackable type]\n`[expert town potion]` 0\n[action type]\n`[deploy in isometric cell map]` 71021 15 30\n[/action type]")
	obj, _ := pvf.Parse("object.obj", "[isometric cell map info]\n`guild agit` `3 X 3` 1 1 1 1 1 1 1 1 1\n[/isometric cell map info]")
	b, err := ParsePlacementBook(10156101, stk, obj)
	if err != nil || b.ObjectID != 71021 || b.Maintenance != 15 || b.Width != 3 || len(b.Mask) != 9 {
		t.Fatalf("book: %+v %v", b, err)
	}
	bad, _ := pvf.Parse("bad.obj", "[isometric cell map info]\n`guild agit` `3 X 3` 1 1\n[/isometric cell map info]")
	if _, err = ParsePlacementBook(10156101, stk, bad); err == nil {
		t.Fatal("accepted truncated shape")
	}
	mapDoc, _ := pvf.Parse("map/guildagit/test.map", "[isometric cell map]\n`guild agit` 750 210 500 160 290 370 900 450\n[/isometric cell map]\n[agitwar area index]\n0")
	m, err := ParsePlacementMap(mapDoc)
	if err != nil || len(m.cells) != 1011 {
		t.Fatalf("map parse: %v", err)
	}
}
