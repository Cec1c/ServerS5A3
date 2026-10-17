package dungeon

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const GriefRewardSlots = 10

// GriefReward is one independently rolled card position. A zero value is an
// unallocated card and is never granted.
type GriefReward struct {
	ItemID uint32
	Count  uint32
}

// GriefRewardCandidates resolves the ten independent Tower of Grief card
// positions from the current PVF. The completion key freezes the draw across
// retries; each card has its own 10000-point probability space.
func GriefRewardCandidates(source dnfpvf.Source, floor uint16, completionKey string) ([GriefRewardSlots]GriefReward, error) {
	var empty [GriefRewardSlots]GriefReward
	if source == nil {
		return empty, griefRewardCatalogError("source is required")
	}
	if floor == 0 || floor > griefMaximumFloor {
		return empty, griefRewardCatalogError("floor %d is outside 1..%d", floor, griefMaximumFloor)
	}
	if completionKey == "" {
		return empty, griefRewardCatalogError("completion key is required")
	}

	tower, err := readGriefRewardDocument(source, "etc/towerofgrief.etc")
	if err != nil {
		return empty, err
	}
	tableName, err := griefRewardTableForFloor(tower, floor)
	if err != nil {
		return empty, err
	}

	drops, err := readGriefRewardDocument(source, "etc/itemdropinfo_togclearreward.etc")
	if err != nil {
		return empty, err
	}
	table, err := griefRewardTableRows(drops, tableName)
	if err != nil {
		return empty, err
	}

	var rolls [GriefRewardSlots]uint32
	for slot := range rolls {
		key := completionKey + "/grief/" + strconv.Itoa(slot)
		hash := sha256.Sum256([]byte(key))
		rolls[slot] = binary.LittleEndian.Uint32(hash[:4]) % 10000
	}

	var out [GriefRewardSlots]GriefReward
	var totals [GriefRewardSlots]uint32
	for index := 0; index < len(table); index += 4 {
		label := table[index]
		if label.Kind != dnfpvf.TokenString || !strings.HasPrefix(label.Value, "card ") {
			return empty, griefRewardCatalogError("table %q has an invalid card label at line %d", tableName, label.Line)
		}
		slotNumber, err := strconv.Atoi(strings.TrimPrefix(label.Value, "card "))
		if err != nil || slotNumber < 1 || slotNumber > GriefRewardSlots {
			return empty, griefRewardCatalogError("table %q has an invalid card slot %q", tableName, label.Value)
		}
		slot := slotNumber - 1

		id := table[index+1]
		probability := table[index+2]
		count := table[index+3]
		if id.Kind != dnfpvf.TokenInt || probability.Kind != dnfpvf.TokenInt || count.Kind != dnfpvf.TokenInt {
			return empty, griefRewardCatalogError("table %q card %d has a non-integer reward row", tableName, slotNumber)
		}
		if id.Int <= 0 || id.Int >= math.MaxUint32 || count.Int <= 0 || count.Int > math.MaxUint32 {
			return empty, griefRewardCatalogError("table %q card %d has an invalid item or count", tableName, slotNumber)
		}
		if probability.Int < 0 || probability.Int > 10000 {
			return empty, griefRewardCatalogError("table %q card %d has probability %d", tableName, slotNumber, probability.Int)
		}

		low := totals[slot]
		total := low + uint32(probability.Int)
		if total > 10000 {
			return empty, griefRewardCatalogError("table %q card %d exceeds 10000 points", tableName, slotNumber)
		}
		totals[slot] = total
		if rolls[slot] >= low && rolls[slot] < total {
			out[slot] = GriefReward{ItemID: uint32(id.Int), Count: uint32(count.Int)}
		}
	}

	return out, nil
}

func readGriefRewardDocument(source dnfpvf.Source, path string) (*dnfpvf.Document, error) {
	text, err := source.ReadText(path)
	if err != nil {
		return nil, fmt.Errorf("read grief reward %s: %w", path, err)
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return nil, fmt.Errorf("parse grief reward %s: %w", path, err)
	}
	return doc, nil
}

func griefRewardTableForFloor(doc *dnfpvf.Document, floor uint16) (string, error) {
	rows, ok := doc.Section("each layer dungeon clear reward")
	if !ok || len(rows) == 0 || len(rows)%3 != 0 {
		return "", griefRewardCatalogError("each-layer reward section is missing or malformed")
	}

	var occupied [griefMaximumFloor + 1]string
	for index := 0; index < len(rows); index += 3 {
		first := rows[index]
		last := rows[index+1]
		name := rows[index+2]
		if first.Kind != dnfpvf.TokenInt || last.Kind != dnfpvf.TokenInt || name.Kind != dnfpvf.TokenString {
			return "", griefRewardCatalogError("each-layer reward row at line %d has an invalid token kind", first.Line)
		}
		if first.Int < 1 || last.Int < first.Int || last.Int > griefMaximumFloor || name.Value == "" {
			return "", griefRewardCatalogError("each-layer reward row at line %d has invalid range %d..%d or name", first.Line, first.Int, last.Int)
		}
		for value := first.Int; value <= last.Int; value++ {
			if occupied[value] != "" {
				return "", griefRewardCatalogError("each-layer reward ranges overlap at floor %d", value)
			}
			occupied[value] = name.Value
		}
	}

	name := occupied[floor]
	if name == "" {
		return "", griefRewardCatalogError("grief floor %d reward table is missing", floor)
	}
	return name, nil
}

func griefRewardTableRows(doc *dnfpvf.Document, name string) ([]dnfpvf.Token, error) {
	var rows []dnfpvf.Token
	matched := false
	for _, section := range doc.Sections {
		if section.Name != "table" {
			continue
		}
		if section.Start < 0 || section.End > len(doc.Tokens) || section.Start >= section.End {
			return nil, griefRewardCatalogError("empty or malformed [table] section")
		}
		label := doc.Tokens[section.Start]
		if label.Kind != dnfpvf.TokenString || label.Value == "" {
			return nil, griefRewardCatalogError("table name at line %d is not a non-empty string token", label.Line)
		}
		if label.Value != name {
			continue
		}
		if matched {
			return nil, griefRewardCatalogError("duplicate matching table %q", name)
		}
		matched = true
		rows = append(rows, doc.Tokens[section.Start+1:section.End]...)
	}
	if !matched {
		return nil, griefRewardCatalogError("grief table %q is missing", name)
	}
	if len(rows) == 0 || len(rows)%4 != 0 {
		return nil, griefRewardCatalogError("grief table %q has %d reward tokens", name, len(rows))
	}
	return rows, nil
}

func griefRewardCatalogError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSettlementStateInvalid, fmt.Sprintf(format, args...))
}
