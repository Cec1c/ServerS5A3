package guild

import (
	"errors"
	"fmt"

	"longheng.io/server/internal/modules/dnf/pvf"
)

var (
	ErrPlacementCatalog  = errors.New("invalid guild placement catalog")
	ErrPlacementPosition = errors.New("guild placement position is unavailable")
)

// PlacementCell uses the native guild-agit isometric lattice: (36,12,23).
type PlacementCell struct{ X, Y int }
type PlacementRect struct{ X, Y, Width, Height int }
type PlacementMap struct {
	Area                            uint8
	Rects                           []PlacementRect
	OriginX, OriginY, Width, Height int
	cells                           map[PlacementCell]bool
}
type PlacementBook struct {
	ItemID, ObjectID, Maintenance uint32
	Width, Height                 int
	Mask                          []uint32
}

func ParsePlacementBook(itemID uint32, stk, object *pvf.Document) (PlacementBook, error) {
	bad := func() (PlacementBook, error) { return PlacementBook{}, ErrPlacementCatalog }
	if itemID == 0 || stk == nil || object == nil {
		return bad()
	}
	typeTokens, ok := stk.Section("stackable type")
	if !ok || len(typeTokens) != 2 || typeTokens[0].Value != "[expert town potion]" {
		return bad()
	}
	a, ok := stk.Section("action type")
	if !ok || len(a) != 4 || a[0].Value != "[deploy in isometric cell map]" {
		return bad()
	}
	for _, t := range a[1:] {
		if t.Kind != pvf.TokenInt || t.Int <= 0 || t.Int > 0xffffffff {
			return bad()
		}
	}
	f, ok := object.Section("isometric cell map info")
	if !ok || len(f) < 3 || f[0].Value != "guild agit" {
		return bad()
	}
	var w, h int
	if _, err := fmt.Sscanf(f[1].Value, "%d X %d", &w, &h); err != nil || w < 1 || h < 1 || w > 32 || h > 32 || len(f) != w*h+2 {
		return bad()
	}
	book := PlacementBook{ItemID: itemID, ObjectID: uint32(a[1].Int), Maintenance: uint32(a[2].Int), Width: w, Height: h, Mask: make([]uint32, w*h)}
	filled := false
	for i, t := range f[2:] {
		if t.Kind != pvf.TokenInt || (t.Int != 0 && t.Int != 1) {
			return bad()
		}
		book.Mask[i] = uint32(t.Int)
		filled = filled || t.Int != 0
	}
	if !filled {
		return bad()
	}
	return book, nil
}

func ParsePlacementMap(doc *pvf.Document) (*PlacementMap, error) {
	if doc == nil {
		return nil, ErrPlacementCatalog
	}
	t, ok := doc.Section("isometric cell map")
	a, areaOK := doc.Int("agitwar area index")
	if !ok || !areaOK || a < 0 || a > 7 || len(t) < 5 || (len(t)-1)%4 != 0 || len(t) > 129 || t[0].Value != "guild agit" {
		return nil, ErrPlacementCatalog
	}
	rects := make([]PlacementRect, 0, (len(t)-1)/4)
	for i := 1; i < len(t); i += 4 {
		for _, v := range t[i : i+4] {
			if v.Kind != pvf.TokenInt || v.Int < 0 || v.Int > 32767 {
				return nil, ErrPlacementCatalog
			}
		}
		r := PlacementRect{int(t[i].Int), int(t[i+1].Int), int(t[i+2].Int), int(t[i+3].Int)}
		if r.Width == 0 || r.Height == 0 || r.X+r.Width > 32767 || r.Y+r.Height > 32767 {
			return nil, ErrPlacementCatalog
		}
		rects = append(rects, r)
	}
	return NewPlacementMap(uint8(a), rects)
}

func NewPlacementMap(area uint8, rects []PlacementRect) (*PlacementMap, error) {
	if area > 7 || len(rects) == 0 || len(rects) > 32 {
		return nil, ErrPlacementCatalog
	}
	m := &PlacementMap{Area: area, Rects: append([]PlacementRect(nil), rects...), OriginX: 32767, OriginY: 32767, cells: map[PlacementCell]bool{}}
	right, bottom := 0, 0
	for _, r := range rects {
		if r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || r.X+r.Width > 32767 || r.Y+r.Height > 32767 {
			return nil, ErrPlacementCatalog
		}
		m.OriginX = min(m.OriginX, r.X)
		m.OriginY = min(m.OriginY, r.Y)
		right = max(right, r.X+r.Width)
		bottom = max(bottom, r.Y+r.Height)
	}
	m.Width = right - m.OriginX
	m.Height = bottom - m.OriginY
	// Bound work before enumerating a malformed PVF map.
	if (m.Width/36+2)*(m.Height/12+2) > 65536 {
		return nil, ErrPlacementCatalog
	}
	inside := func(x, y int) bool {
		for _, r := range rects {
			if x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height {
				return true
			}
		}
		return false
	}
	// Native 03C502D0 and 03C4FAE0 test all four vertices independently.
	for cy, py := 0, m.OriginY; py <= bottom; cy, py = cy+1, py+12 {
		for cx, px := -(cy * 23 / 36), m.OriginX+(cy*23)%36; px <= right; cx, px = cx+1, px+36 {
			if inside(px, py) && inside(px+36, py) && inside(px+23, py+12) && inside(px+59, py+12) {
				m.cells[PlacementCell{cx, cy}] = true
			}
		}
	}
	return m, nil
}

func (b PlacementBook) Validate() error {
	if b.ItemID == 0 || b.ObjectID == 0 || b.Maintenance == 0 || b.Maintenance > 300 || b.Width < 1 || b.Height < 1 || b.Width > 32 || b.Height > 32 || len(b.Mask) != b.Width*b.Height {
		return ErrPlacementCatalog
	}
	any := false
	for _, v := range b.Mask {
		if v > 1 {
			return ErrPlacementCatalog
		}
		any = any || v != 0
	}
	if !any {
		return ErrPlacementCatalog
	}
	return nil
}

// CellsAt accepts only centers actually emitted by the native sender, then
// reconstructs the exact occupied mask from the PVF rather than client input.
func (m *PlacementMap) CellsAt(b PlacementBook, x, y int) ([]PlacementCell, error) {
	if m == nil || len(m.cells) == 0 || b.Validate() != nil {
		return nil, ErrPlacementCatalog
	}
	leftY := y - m.OriginY - b.Height*12/2
	if leftY < 0 || leftY%12 != 0 {
		return nil, ErrPlacementPosition
	}
	cy := leftY / 12
	leftX := x - m.OriginX - (b.Width*36+b.Height*23)/2 - cy*23
	if leftX%36 != 0 {
		return nil, ErrPlacementPosition
	}
	cx := leftX / 36
	cells := make([]PlacementCell, 0, len(b.Mask))
	for i, v := range b.Mask {
		if v == 0 {
			continue
		}
		cell := PlacementCell{cx + i%b.Width, cy + i/b.Width}
		if !m.cells[cell] {
			return nil, ErrPlacementPosition
		}
		cells = append(cells, cell)
	}
	return cells, nil
}
