package wedding

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const LovePointItemID int64 = 10002606
const RoomInventoryCapacity = 40
const RoomPlacementCapacity = 28

type Furniture struct {
	ItemID, Points, Gold int64
	Positions            map[uint16]bool
}

func loadFurniture(source pvf.Source, room *pvf.Document) (map[int64]Furniture, error) {
	text, err := source.ReadText("stackable/stackable.lst")
	if err != nil {
		return nil, err
	}
	index, err := pvf.Parse("stackable/stackable.lst", text)
	if err != nil {
		return nil, err
	}
	paths := map[int64]string{}
	for _, entry := range pvf.ParseList(index) {
		paths[entry.ID] = entry.Path
	}
	validPositions := map[uint16]bool{}
	positions := room.Ints("position list")
	if len(positions)%4 != 0 || len(positions) == 0 {
		return nil, fmt.Errorf("wedding: position table shape")
	}
	for i := 0; i < len(positions); i += 4 {
		if positions[i] < 0 || positions[i] > 65535 || validPositions[uint16(positions[i])] {
			return nil, ErrState
		}
		validPositions[uint16(positions[i])] = true
	}
	result := map[int64]Furniture{}
	for _, section := range room.Sections {
		if strings.Trim(strings.ToLower(section.Name), "[] ") != "coupleroom item" {
			continue
		}
		t := room.Tokens[section.Start:section.End]
		if len(t) < 4 || t[0].Kind != pvf.TokenInt || t[1].Kind != pvf.TokenInt {
			return nil, ErrState
		}
		id, n := t[0].Int, t[1].Int
		if id <= 0 || n <= 0 || n > 128 || len(t) != int(4+5*n) {
			return nil, fmt.Errorf("wedding: furniture %d shape", id)
		}
		f := Furniture{ItemID: id, Positions: map[uint16]bool{}}
		for i := int64(0); i < n; i++ {
			at := 2 + int(i)*5
			if t[at].Kind != pvf.TokenInt || t[at].Int < 0 || t[at].Int > 65535 || !validPositions[uint16(t[at].Int)] {
				return nil, ErrState
			}
			f.Positions[uint16(t[at].Int)] = true
		}
		path := paths[id]
		if path == "" {
			return nil, fmt.Errorf("wedding: furniture %d missing", id)
		}
		if !strings.HasPrefix(strings.ToLower(path), "stackable/") {
			path = "stackable/" + path
		}
		text, err = source.ReadText(path)
		if err != nil {
			return nil, err
		}
		doc, parseErr := pvf.Parse(path, text)
		if parseErr != nil {
			return nil, parseErr
		}
		kind, _ := doc.Text("stackable type")
		if strings.Trim(kind, "[] ") != "couple room item" {
			return nil, fmt.Errorf("wedding: furniture %d type %s", id, kind)
		}
		need := doc.Ints("need material")
		if len(need) != 2 || need[0] != LovePointItemID || need[1] < 0 {
			return nil, fmt.Errorf("wedding: furniture %d material shape", id)
		}
		f.Points = need[1]
		f.Gold, _ = doc.Int("price")
		if f.Gold < 0 {
			return nil, ErrGold
		}
		if _, duplicate := result[id]; duplicate {
			return nil, ErrState
		}
		result[id] = f
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("wedding: no furniture")
	}
	return result, nil
}
