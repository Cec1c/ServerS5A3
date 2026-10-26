package guild

import (
	"errors"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

var (
	ErrGuardianState        = errors.New("invalid guild guardian state")
	ErrGuardianCatalog      = errors.New("invalid guild guardian purchase catalog")
	ErrGuardianPolicy       = errors.New("guild guardian first purchase policy is not configured")
	ErrGuardianAlreadyOwned = errors.New("guild guardian is already owned")
)

// Guardian stores the existing native row, not acquisition defaults. Skills
// are five levels; they are neither skill IDs nor visibility flags. The native
// UI separately unlocks one skill per star tier derived from Level.
type Guardian struct {
	ID         uint32   `json:"id"`
	Level      uint8    `json:"level"`
	Skills     [5]uint8 `json:"skills"`
	APProgress uint32   `json:"ap_progress"`
	GPProgress uint32   `json:"gp_progress"`
}

type GuardianState struct {
	// This lifetime fact is independent of which guardian is currently active.
	// Future reset/removal operations must retain it; clearing ActiveID cannot
	// make a guild eligible for another first acquisition.
	EverAcquired bool       `json:"ever_acquired"`
	ActiveID     uint32     `json:"active_id"`
	Owned        []Guardian `json:"owned,omitempty"`
}

func (s *GuardianState) Validate() error {
	if s == nil {
		return nil
	}
	if len(s.Owned) > 2 || (len(s.Owned) != 0 && !s.EverAcquired) {
		return ErrGuardianState
	}
	seen := map[uint32]bool{}
	for _, row := range s.Owned {
		if (row.ID != 1 && row.ID != 2) || seen[row.ID] || row.Level < 1 || row.Level > 50 {
			return ErrGuardianState
		}
		seen[row.ID] = true
		for _, level := range row.Skills {
			if level > 5 {
				return ErrGuardianState
			}
		}
	}
	if s.ActiveID != 0 && !seen[s.ActiveID] {
		return ErrGuardianState
	}
	return nil
}

type GuardianFirstPurchasePolicy string

const (
	GuardianFreeOnce GuardianFirstPurchasePolicy = "free_once"
	GuardianPaid     GuardianFirstPurchasePolicy = "paid"
)

// GuardianCatalog is deliberately limited to the two matched guardian IDs.
// It contains purchase costs only: acquisition level and skills come from an
// independently configured GuardianPolicy.
type GuardianCatalog struct {
	costs map[uint32]EssenceBalance
}

func LoadGuardianCatalog(source pvf.Source) (*GuardianCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(EssenceCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseGuardianCatalog(text)
}

func ParseGuardianCatalog(text string) (*GuardianCatalog, error) {
	doc, err := pvf.Parse(EssenceCatalogPath, text)
	if err != nil {
		return nil, err
	}
	c := &GuardianCatalog{costs: make(map[uint32]EssenceBalance)}
	var id uint32
	var inList, listSeen, apSeen, gpSeen bool
	var cost EssenceBalance
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		tokens := doc.Tokens[section.Start:section.End]
		switch name {
		case "guardian list":
			if listSeen || inList || id != 0 || len(tokens) != 0 {
				return nil, ErrGuardianCatalog
			}
			inList, listSeen = true, true
		case "/guardian list":
			if !inList || id != 0 || len(tokens) != 0 {
				return nil, ErrGuardianCatalog
			}
			inList = false
		case "guardian":
			if !inList || id != 0 || len(tokens) != 1 || tokens[0].Kind != pvf.TokenInt || (tokens[0].Int != 1 && tokens[0].Int != 2) {
				return nil, ErrGuardianCatalog
			}
			id = uint32(tokens[0].Int)
			if _, exists := c.costs[id]; exists {
				return nil, ErrGuardianCatalog
			}
			cost, apSeen, gpSeen = EssenceBalance{}, false, false
		case "/guardian":
			if !inList || id == 0 || !apSeen || !gpSeen || len(tokens) != 0 {
				return nil, ErrGuardianCatalog
			}
			c.costs[id] = cost
			id = 0
		case "ap", "gp":
			if id == 0 { // Other building costs use the same section names.
				continue
			}
			if len(tokens) != 1 || tokens[0].Kind != pvf.TokenInt || tokens[0].Int <= 0 || tokens[0].Int > math.MaxInt32 {
				return nil, ErrGuardianCatalog
			}
			if name == "ap" {
				if apSeen {
					return nil, ErrGuardianCatalog
				}
				cost.AP, apSeen = uint32(tokens[0].Int), true
			} else {
				if gpSeen {
					return nil, ErrGuardianCatalog
				}
				cost.GP, gpSeen = uint32(tokens[0].Int), true
			}
		}
	}
	if !listSeen || inList || id != 0 || len(c.costs) != 2 {
		return nil, ErrGuardianCatalog
	}
	return c, nil
}

// PurchaseCost is read-only. ApplyGuardian rechecks membership, permission,
// session, interaction and this state inside the acquisition transaction.
// An already owned ID is not a purchase; action7 is the native selection path.
func (c *GuardianCatalog) PurchaseCost(policy GuardianFirstPurchasePolicy, state *GuardianState, id uint32) (EssenceBalance, error) {
	if policy != GuardianFreeOnce && policy != GuardianPaid {
		return EssenceBalance{}, ErrGuardianPolicy
	}
	if err := state.Validate(); err != nil {
		return EssenceBalance{}, err
	}
	if c == nil {
		return EssenceBalance{}, ErrGuardianCatalog
	}
	cost, found := c.costs[id]
	if !found {
		return EssenceBalance{}, ErrGuardianCatalog
	}
	if state != nil {
		for _, owned := range state.Owned {
			if owned.ID == id {
				return EssenceBalance{}, ErrGuardianAlreadyOwned
			}
		}
	}
	if policy == GuardianFreeOnce && (state == nil || !state.EverAcquired) {
		return EssenceBalance{}, nil
	}
	return cost, nil
}
