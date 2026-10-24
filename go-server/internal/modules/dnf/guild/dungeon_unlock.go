package guild

import (
	"errors"
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

var ErrDungeonUnlockRules = errors.New("guild dungeon unlock rules are invalid")

type dungeonUnlockRule struct {
	limit, gain, threshold uint32
	requires               []uint32
}

// DungeonUnlockRules evaluates the matched client's prerequisite contract.
// Counts are clear-count units, not raw purification gauge. This read-only
// catalog neither authorizes a member's entry nor commits a dungeon clear.
type DungeonUnlockRules struct {
	byID map[uint32]dungeonUnlockRule
}

// ParseDungeonUnlockRules projects only the unlock fields from an extracted
// Etc/Guild/GuildDungeon catalog. Rewards and lifecycle policy remain separate.
func ParseDungeonUnlockRules(text string) (*DungeonUnlockRules, error) {
	doc, err := dnfpvf.Parse("guild-dungeon-unlock.etc", text)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDungeonUnlockRules, err)
	}
	result := &DungeonUnlockRules{byID: make(map[uint32]dungeonUnlockRule)}
	var current map[string][]uint32
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		tokens := doc.Tokens[section.Start:section.End]
		switch name {
		case "dungeon":
			if current != nil || len(tokens) != 0 {
				return nil, ErrDungeonUnlockRules
			}
			current = make(map[string][]uint32)
		case "/dungeon":
			if current == nil || len(tokens) != 0 {
				return nil, ErrDungeonUnlockRules
			}
			for _, key := range []string{"dungeon index", "gauge limit", "gauge on clear", "conquest gauge ratio"} {
				if len(current[key]) != 1 {
					return nil, fmt.Errorf("%w: missing [%s]", ErrDungeonUnlockRules, key)
				}
			}
			id := current["dungeon index"][0]
			if id == 0 || current["conquest gauge ratio"][0] > 100 {
				return nil, ErrDungeonUnlockRules
			}
			if _, exists := result.byID[id]; exists {
				return nil, fmt.Errorf("%w: duplicate dungeon %d", ErrDungeonUnlockRules, id)
			}
			result.byID[id] = dungeonUnlockRule{limit: current["gauge limit"][0], gain: current["gauge on clear"][0], threshold: current["conquest gauge ratio"][0], requires: current["required dungeon conquest"]}
			current = nil
		case "dungeon index", "gauge limit", "gauge on clear", "conquest gauge ratio", "required dungeon conquest":
			if current == nil {
				return nil, ErrDungeonUnlockRules
			}
			if _, exists := current[name]; exists {
				return nil, fmt.Errorf("%w: duplicate [%s]", ErrDungeonUnlockRules, name)
			}
			if name != "required dungeon conquest" && len(tokens) != 1 {
				return nil, ErrDungeonUnlockRules
			}
			values := make([]uint32, 0, len(tokens))
			seen := make(map[uint32]bool)
			for _, token := range tokens {
				if token.Kind != dnfpvf.TokenInt || token.Int < 0 || token.Int > math.MaxInt32 {
					return nil, fmt.Errorf("%w: non-native integer in [%s]", ErrDungeonUnlockRules, name)
				}
				value := uint32(token.Int)
				if name == "required dungeon conquest" && (value == 0 || seen[value]) {
					return nil, ErrDungeonUnlockRules
				}
				values = append(values, value)
				seen[value] = true
			}
			current[name] = values
		}
	}
	if current != nil || len(result.byID) == 0 {
		return nil, ErrDungeonUnlockRules
	}
	// Invalid references and cycles otherwise silently create unreachable maps.
	visits := make(map[uint32]byte)
	var visit func(uint32) bool
	visit = func(id uint32) bool {
		if visits[id] == 1 {
			return false
		}
		if visits[id] == 2 {
			return true
		}
		rule, found := result.byID[id]
		if !found {
			return false
		}
		visits[id] = 1
		for _, required := range rule.requires {
			if !visit(required) {
				return false
			}
		}
		visits[id] = 2
		return true
	}
	for id := range result.byID {
		if !visit(id) {
			return nil, fmt.Errorf("%w: invalid dependency graph", ErrDungeonUnlockRules)
		}
	}
	return result, nil
}

// PurificationRatio follows V164 VA 03C73EE0: signed integer product, float32
// multiplication/division, truncation and clamping to [0,100]. Reject inputs
// that would wrap the native signed product instead of publishing corrupt state.
func (r *DungeonUnlockRules) PurificationRatio(id, count uint32) (uint32, error) {
	if r == nil {
		return 0, ErrDungeonUnlockRules
	}
	rule, found := r.byID[id]
	if !found || count > math.MaxInt32 {
		return 0, ErrDungeonUnlockRules
	}
	if rule.limit == 0 {
		return 100, nil
	}
	product := uint64(count) * uint64(rule.gain)
	if product > math.MaxInt32 {
		return 0, ErrDungeonUnlockRules
	}
	ratio := float32(product) * float32(100) / float32(rule.limit)
	if ratio >= float32(2147483648) {
		return 0, ErrDungeonUnlockRules // native cvttss2si would overflow
	}
	if ratio >= 100 {
		return 100, nil
	}
	return uint32(ratio), nil
}

// Allows requires each predecessor to reach its own conquest threshold, as in
// V164 VA 013A0610. A known rule without predecessors is open at zero clears.
// Missing catalogs/IDs stay closed; membership, cycle and map entry are callers'
// responsibilities. No production sender/entry owner is connected yet.
func (r *DungeonUnlockRules) Allows(id uint32, counts map[uint32]uint32) (bool, error) {
	if r == nil {
		return false, nil
	}
	rule, found := r.byID[id]
	if !found {
		return false, nil
	}
	for _, required := range rule.requires {
		ratio, err := r.PurificationRatio(required, counts[required])
		if err != nil {
			return false, err
		}
		if ratio < r.byID[required].threshold {
			return false, nil
		}
	}
	return true, nil
}
