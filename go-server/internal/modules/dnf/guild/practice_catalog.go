package guild

import (
	"errors"
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

var ErrPracticeCatalog = errors.New("invalid guild practice catalog")

// PracticeCatalog owns only the friendly selector and guardian monster indexes
// in agitwar.etc. Admission, placed defenses and practice settlement require
// their own runtime owner; reading this catalog does not grant entry.
type PracticeCatalog struct {
	base       uint32
	barricades [3]uint32
	dummy      uint32
	guardians  map[uint32][5]uint32
}

func LoadPracticeCatalog(source pvf.Source) (*PracticeCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText("etc/agitwar.etc")
	if err != nil {
		return nil, err
	}
	return ParsePracticeCatalog(text)
}

func ParsePracticeCatalog(text string) (*PracticeCatalog, error) {
	doc, err := pvf.Parse("etc/agitwar.etc", text)
	if err != nil {
		return nil, err
	}
	c := &PracticeCatalog{guardians: make(map[uint32][5]uint32)}
	var friend, list bool
	var friendSeen, listSeen, barricadesSeen bool
	var guardian uint32
	var indexes [5]uint32
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		tokens := doc.Tokens[section.Start:section.End]
		ints := func(n int) ([]uint32, error) {
			if len(tokens) != n {
				return nil, ErrPracticeCatalog
			}
			out := make([]uint32, n)
			for i, token := range tokens {
				if token.Kind != pvf.TokenInt || token.Int <= 0 || token.Int > math.MaxInt32 {
					return nil, ErrPracticeCatalog
				}
				out[i] = uint32(token.Int)
			}
			return out, nil
		}
		switch name {
		case "friend agit":
			if friendSeen || list || len(tokens) != 0 {
				return nil, ErrPracticeCatalog
			}
			friend, friendSeen = true, true
		case "/friend agit":
			if !friend || c.base == 0 || len(tokens) != 0 {
				return nil, ErrPracticeCatalog
			}
			friend = false
		case "friend barricade dungeon":
			if barricadesSeen || friend || list {
				return nil, ErrPracticeCatalog
			}
			a, err := ints(3)
			if err != nil {
				return nil, err
			}
			copy(c.barricades[:], a)
			barricadesSeen = true
		case "guardian dummy monster":
			if c.dummy != 0 || friend || list {
				return nil, ErrPracticeCatalog
			}
			a, err := ints(1)
			if err != nil {
				return nil, err
			}
			c.dummy = a[0]
		case "guardian list":
			if listSeen || friend || len(tokens) != 0 {
				return nil, ErrPracticeCatalog
			}
			list, listSeen = true, true
		case "/guardian list":
			if !list || guardian != 0 || len(tokens) != 0 {
				return nil, ErrPracticeCatalog
			}
			list = false
		case "guardian":
			if !list || guardian != 0 {
				return nil, ErrPracticeCatalog
			}
			a, err := ints(1)
			if err != nil || (a[0] != 1 && a[0] != 2) {
				return nil, ErrPracticeCatalog
			}
			if _, exists := c.guardians[a[0]]; exists {
				return nil, ErrPracticeCatalog
			}
			guardian, indexes = a[0], [5]uint32{}
		case "/guardian":
			if !list || guardian == 0 || indexes[0] == 0 || len(tokens) != 0 {
				return nil, ErrPracticeCatalog
			}
			c.guardians[guardian], guardian = indexes, 0
		case "index":
			if friend {
				if c.base != 0 {
					return nil, ErrPracticeCatalog
				}
				a, err := ints(1)
				if err != nil {
					return nil, err
				}
				c.base = a[0]
			} else if guardian != 0 {
				if indexes[0] != 0 {
					return nil, ErrPracticeCatalog
				}
				a, err := ints(5)
				if err != nil {
					return nil, err
				}
				copy(indexes[:], a)
			}
		}
	}
	if friend || list || guardian != 0 || c.base == 0 || c.dummy == 0 || !barricadesSeen || len(c.guardians) != 2 {
		return nil, ErrPracticeCatalog
	}
	seen := map[uint32]bool{c.base: true}
	for _, id := range c.barricades {
		if seen[id] {
			return nil, ErrPracticeCatalog
		}
		seen[id] = true
	}
	return c, nil
}

// FriendlyDungeons returns the base and the barricade for the authoritative
// expansion value. V176 selects a barricade by index, not ID arithmetic.
func (c *PracticeCatalog) FriendlyDungeons(expansion uint8) (base, barricade uint32, err error) {
	if c == nil || expansion >= uint8(len(c.barricades)) {
		return 0, 0, ErrPracticeCatalog
	}
	return c.base, c.barricades[expansion], nil
}

// PracticeGuardian is a value snapshot. Later purchases or upgrades cannot
// change a prepared projection through a shared pointer.
type PracticeGuardian struct {
	Guardian  Guardian
	MonsterID uint32
	dummy     uint32
}

// GuardianProjection matches V176 01076220 -> 010690E0 -> 01069040:
// active owned guardian, star = 1 + (level-1)/10, five PVF monster indexes.
// No active guardian means no replacement, including a guild with none owned.
func (c *PracticeCatalog) GuardianProjection(state *GuardianState) (PracticeGuardian, error) {
	if c == nil {
		return PracticeGuardian{}, ErrPracticeCatalog
	}
	if err := state.Validate(); err != nil {
		return PracticeGuardian{}, err
	}
	if state == nil || state.ActiveID == 0 {
		return PracticeGuardian{}, nil
	}
	indexes, ok := c.guardians[state.ActiveID]
	if !ok {
		return PracticeGuardian{}, ErrPracticeCatalog
	}
	for _, owned := range state.Owned {
		if owned.ID == state.ActiveID {
			return PracticeGuardian{Guardian: owned, MonsterID: indexes[(owned.Level-1)/10], dummy: c.dummy}, nil
		}
	}
	return PracticeGuardian{}, ErrGuardianState
}

// ReplaceMonster mirrors 0107671E..010767C6: only native type 3 and the
// configured dummy are replaced. Object key, position and all other fields
// remain the caller's responsibility; a MAP level/rank is not nativeType.
func (p PracticeGuardian) ReplaceMonster(monsterID, nativeType uint32) uint32 {
	if nativeType == 3 && monsterID == p.dummy && p.MonsterID != 0 {
		return p.MonsterID
	}
	return monsterID
}
