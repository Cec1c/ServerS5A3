package dnfbridge

import (
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// currentDimensionHiddenDropRow preserves the three PVF fields without
// assigning a probability unit or a number of rolls to RawValue.
type currentDimensionHiddenDropRow struct {
	Tier     int
	ItemID   uint32
	RawValue uint32
}

func parseCurrentDimensionHiddenDropTable(doc *dnfpvf.Document) (map[int][]currentDimensionHiddenDropRow, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: hidden boss drop document missing", errCurrentDungeonDimensionDropInvalid)
	}
	tokens, found := doc.Section("hidden boss drop table")
	if !found || len(tokens) == 0 || len(tokens)%3 != 0 {
		return nil, fmt.Errorf("%w: %s hidden boss drop table has %d tokens", errCurrentDungeonDimensionDropInvalid, doc.Path, len(tokens))
	}
	rows := make(map[int][]currentDimensionHiddenDropRow)
	seen := make(map[[2]uint32]struct{})
	for i := 0; i < len(tokens); i += 3 {
		if tokens[i].Kind != dnfpvf.TokenInt || tokens[i+1].Kind != dnfpvf.TokenInt || tokens[i+2].Kind != dnfpvf.TokenInt {
			return nil, fmt.Errorf("%w: %s hidden boss drop row %d is not three integers", errCurrentDungeonDimensionDropInvalid, doc.Path, i/3)
		}
		tier, itemID, rawValue := tokens[i].Int, tokens[i+1].Int, tokens[i+2].Int
		if tier < 0 || tier > 2 || itemID <= 0 || itemID > math.MaxUint32 || rawValue <= 0 || rawValue > currentDungeonDropDenominator {
			return nil, fmt.Errorf("%w: %s hidden boss drop row %d has invalid values %d/%d/%d", errCurrentDungeonDimensionDropInvalid, doc.Path, i/3, tier, itemID, rawValue)
		}
		key := [2]uint32{uint32(tier), uint32(itemID)}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: %s duplicate hidden boss drop tier=%d item=%d", errCurrentDungeonDimensionDropInvalid, doc.Path, tier, itemID)
		}
		seen[key] = struct{}{}
		rows[int(tier)] = append(rows[int(tier)], currentDimensionHiddenDropRow{Tier: int(tier), ItemID: uint32(itemID), RawValue: uint32(rawValue)})
	}
	return rows, nil
}

// The matching PVF's appearance table and the ordinary dungeon item roll both
// use 10000 as their threshold. Apply each hidden-boss row once on death; a row
// can award its item when its raw value exceeds the room-stream roll.
func currentDimensionHiddenDropItems(rows []currentDimensionHiddenDropRow, rng *currentDungeonDropLCG) ([]uint32, error) {
	if rng == nil || len(rows) == 0 {
		return nil, fmt.Errorf("%w: hidden boss drop rows or RNG missing", errCurrentDungeonDimensionDropInvalid)
	}
	selected := make([]uint32, 0, len(rows))
	for _, row := range rows {
		if row.RawValue == 0 || row.RawValue > currentDungeonDropDenominator {
			return nil, fmt.Errorf("%w: hidden boss item %d has invalid threshold %d", errCurrentDungeonDimensionDropInvalid, row.ItemID, row.RawValue)
		}
		if int(row.RawValue) > rng.Next(currentDungeonDropDenominator) {
			selected = append(selected, row.ItemID)
		}
	}
	return selected, nil
}
