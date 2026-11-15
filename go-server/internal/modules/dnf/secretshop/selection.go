package secretshop

import (
	"errors"
	"fmt"
	"math"
)

const maxSelectedOffers = 8

var (
	ErrSelection          = errors.New("secret shop selection failed")
	ErrSelectionExhausted = errors.New("secret shop selection candidates exhausted")
	ErrWeightOverflow     = errors.New("secret shop weight total exceeds uint32")
	ErrInvalidRoll        = errors.New("secret shop roll is invalid")
	ErrPriceAdjustment    = errors.New("secret shop price adjustment failed")
)

// Selected identifies whether a candidate came from the normal or extra pool.
// Count remains the unscaled PVF value. Price is adjusted by RollOffers to a
// value in 1..math.MaxInt32. Callers decide whether the item is
// equipment/stackable and apply the appropriate stock percentage for a
// black-diamond member.
type Selected struct {
	Candidate
	Extra bool
}

// PickNPC deterministically chooses one NPC from the selected neutral weight
// column. blackMembers is passed directly as column 0..4. A zero-total table is
// rejected so a missing configuration cannot silently become NPC 1000.
func PickNPC(
	c *Catalog,
	dungeonID uint32,
	basisLevel, blackMembers int,
	roll func(uint32) (uint32, error),
) (uint32, error) {
	if c == nil {
		return 0, fmt.Errorf("%w: catalog is nil", ErrSelection)
	}
	if roll == nil {
		return 0, fmt.Errorf("%w: nil NPC roll", ErrInvalidRoll)
	}
	pairs, err := c.WeightPairs(dungeonID, basisLevel, blackMembers)
	if err != nil {
		return 0, fmt.Errorf("%w: resolve NPC weights: %w", ErrSelection, err)
	}
	total := uint64(0)
	for _, pair := range pairs {
		next := total + uint64(pair.Weight)
		if next > math.MaxUint32 {
			return 0, fmt.Errorf("%w: %d", ErrWeightOverflow, next)
		}
		total = next
	}
	if total == 0 {
		return 0, ErrSelectionExhausted
	}
	draw, err := drawBounded(roll, uint32(total), "NPC")
	if err != nil {
		return 0, err
	}
	cursor := uint64(0)
	for _, pair := range pairs {
		if pair.Weight == 0 {
			continue
		}
		cursor += uint64(pair.Weight)
		if uint64(draw) < cursor {
			return pair.NPCID, nil
		}
	}
	return 0, fmt.Errorf("%w: NPC draw %d did not resolve", ErrSelection, draw)
}

// RollOffers selects normal.SelectionCount candidates and, for black members,
// extra.SelectionCount additional candidates. Selection is weighted, excludes
// zero-weight rows, and never emits the same ItemID twice across either pool.
// Draws are made in source order, normal first and then extra for black members.
// All offer draws happen first, followed by one AdjustPrice draw per selected
// row in returned order. The returned Count is copied unchanged from the PVF
// candidate and Price contains the adjusted value.
func RollOffers(
	c *Catalog,
	npcID, dungeonID uint32,
	basisLevel int,
	black bool,
	valid func(Candidate) bool,
	roll func(uint32) (uint32, error),
) ([]Selected, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: catalog is nil", ErrSelection)
	}
	normal, extra, err := c.Pools(npcID, dungeonID, basisLevel)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve pools: %w", ErrSelection, err)
	}
	if normal.SelectionCount < 0 || normal.SelectionCount > maxSelectedOffers ||
		extra.SelectionCount < 0 || extra.SelectionCount > maxSelectedOffers {
		return nil, fmt.Errorf("%w: invalid pool selection count normal=%d extra=%d", ErrSelection, normal.SelectionCount, extra.SelectionCount)
	}
	totalRows := normal.SelectionCount
	if black {
		totalRows += extra.SelectionCount
	}
	if totalRows > maxSelectedOffers {
		return nil, fmt.Errorf("%w: %d requested rows exceeds %d", ErrSelection, totalRows, maxSelectedOffers)
	}
	if totalRows == 0 {
		return []Selected{}, nil
	}
	if roll == nil {
		return nil, fmt.Errorf("%w: nil offer roll", ErrInvalidRoll)
	}

	used := make(map[uint32]struct{}, totalRows)
	selected := make([]Selected, 0, totalRows)
	selected, err = rollPool(selected, normal, false, valid, used, roll)
	if err != nil {
		return nil, err
	}
	if black {
		selected, err = rollPool(selected, extra, true, valid, used, roll)
		if err != nil {
			return nil, err
		}
	}
	if len(selected) != totalRows {
		return nil, fmt.Errorf("%w: selected %d rows, expected %d", ErrSelection, len(selected), totalRows)
	}
	for i := range selected {
		adjusted, adjustErr := AdjustPrice(c, selected[i].Price, roll)
		if adjustErr != nil {
			return nil, fmt.Errorf("%w: adjust %s row %d price: %w", ErrSelection, poolName(selected[i].Extra), i, adjustErr)
		}
		selected[i].Price = adjusted
	}
	return selected, nil
}

func rollPool(
	selected []Selected,
	pool Pool,
	extra bool,
	valid func(Candidate) bool,
	used map[uint32]struct{},
	roll func(uint32) (uint32, error),
) ([]Selected, error) {
	if pool.SelectionCount == 0 {
		return selected, nil
	}
	eligible := make([]Candidate, 0, len(pool.Candidates))
	for _, candidate := range pool.Candidates {
		if candidate.ItemID == 0 {
			return nil, fmt.Errorf("%w: %s pool contains item zero", ErrSelection, poolName(extra))
		}
		if candidate.Price <= 0 || candidate.Price > math.MaxInt32 {
			return nil, fmt.Errorf("%w: %s pool item %d price %d outside [1,%d]", ErrSelection, poolName(extra), candidate.ItemID, candidate.Price, int64(math.MaxInt32))
		}
		if valid == nil || valid(candidate) {
			eligible = append(eligible, candidate)
		}
	}
	for count := 0; count < pool.SelectionCount; count++ {
		candidate, ok, err := pickCandidate(eligible, used, roll)
		if err != nil {
			return nil, fmt.Errorf("%w: %s pool row %d: %w", ErrSelection, poolName(extra), count, err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s pool row %d (%d/%d)", ErrSelectionExhausted, poolName(extra), count, count, pool.SelectionCount)
		}
		used[candidate.ItemID] = struct{}{}
		selected = append(selected, Selected{Candidate: candidate, Extra: extra})
	}
	return selected, nil
}

func pickCandidate(
	candidates []Candidate,
	used map[uint32]struct{},
	roll func(uint32) (uint32, error),
) (Candidate, bool, error) {
	total := uint64(0)
	for _, candidate := range candidates {
		if _, exists := used[candidate.ItemID]; exists {
			continue
		}
		next := total + uint64(candidate.Weight)
		if next > math.MaxUint32 {
			return Candidate{}, false, fmt.Errorf("%w: %d", ErrWeightOverflow, next)
		}
		total = next
	}
	if total == 0 {
		return Candidate{}, false, nil
	}
	draw, err := drawBounded(roll, uint32(total), "offer")
	if err != nil {
		return Candidate{}, false, err
	}
	cursor := uint64(0)
	for _, candidate := range candidates {
		if _, exists := used[candidate.ItemID]; exists || candidate.Weight == 0 {
			continue
		}
		cursor += uint64(candidate.Weight)
		if uint64(draw) < cursor {
			return candidate, true, nil
		}
	}
	return Candidate{}, false, fmt.Errorf("%w: offer draw %d did not resolve", ErrSelection, draw)
}

func poolName(extra bool) string {
	if extra {
		return "extra"
	}
	return "normal"
}

func drawBounded(roll func(uint32) (uint32, error), bound uint32, what string) (uint32, error) {
	if roll == nil || bound == 0 {
		return 0, fmt.Errorf("%w: %s bound %d", ErrInvalidRoll, what, bound)
	}
	draw, err := roll(bound)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %w", ErrInvalidRoll, what, err)
	}
	if draw >= bound {
		return 0, fmt.Errorf("%w: %s draw %d outside [0,%d)", ErrInvalidRoll, what, draw, bound)
	}
	return draw, nil
}

// AdjustPrice applies a uniform integer percentage deviation in the inclusive
// range [-PriceRandomPercent,+PriceRandomPercent]. The input and result must be
// in 1..math.MaxInt32. All percent multiplication and addition are checked.
func AdjustPrice(c *Catalog, price int64, roll func(uint32) (uint32, error)) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("%w: catalog is nil", ErrPriceAdjustment)
	}
	percent := c.PriceRandomPercent
	if percent < 0 || percent >= 100 {
		return 0, fmt.Errorf("%w: random percent %d outside [0,100)", ErrPriceAdjustment, percent)
	}
	if price <= 0 || price > math.MaxInt32 {
		return 0, fmt.Errorf("%w: price %d outside [1,%d]", ErrPriceAdjustment, price, int64(math.MaxInt32))
	}
	if percent == 0 {
		return price, nil
	}
	bound64 := uint64(percent)*2 + 1
	if bound64 > math.MaxUint32 {
		return 0, fmt.Errorf("%w: random percent %d is too large", ErrPriceAdjustment, percent)
	}
	draw, err := drawBounded(roll, uint32(bound64), "price")
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrPriceAdjustment, err)
	}
	deviation := int64(draw) - int64(percent)
	if deviation == 0 {
		return price, nil
	}
	magnitude := deviation
	if magnitude < 0 {
		magnitude = -magnitude
	}
	change, err := percentageOf(price, magnitude)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrPriceAdjustment, err)
	}
	if deviation < 0 {
		if change >= price {
			return 0, fmt.Errorf("%w: %d price -%d%% is not positive", ErrPriceAdjustment, price, magnitude)
		}
		return price - change, nil
	}
	if change > math.MaxInt32-price {
		return 0, fmt.Errorf("%w: %d +%d%% exceeds %d", ErrPriceAdjustment, price, magnitude, int64(math.MaxInt32))
	}
	return price + change, nil
}

func percentageOf(price, percent int64) (int64, error) {
	if price < 0 || percent < 0 {
		return 0, fmt.Errorf("negative percentage operands (%d,%d)", price, percent)
	}
	whole := price / 100
	remainder := price % 100
	if percent != 0 && whole > math.MaxInt64/percent {
		return 0, fmt.Errorf("percentage multiplication overflows int64")
	}
	change := whole * percent
	if remainder != 0 && percent > math.MaxInt64/remainder {
		return 0, fmt.Errorf("percentage remainder multiplication overflows int64")
	}
	partial := remainder * percent / 100
	if change > math.MaxInt64-partial {
		return 0, fmt.Errorf("percentage addition overflows int64")
	}
	return change + partial, nil
}
