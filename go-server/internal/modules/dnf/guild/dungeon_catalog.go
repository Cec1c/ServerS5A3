package guild

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const DungeonCatalogPath = "etc/guild/guilddungeon/0_2way6dungeon.etc"

type DungeonRewardItem struct{ ItemID, Count uint32 }
type DungeonReward struct {
	Ratio        uint32
	Items        []DungeonRewardItem
	Contribution uint32
}

// DungeonCatalog is immutable after parsing. The digest binds admission to the
// same PVF rules at settlement, including reward quantities and prerequisites.
type DungeonCatalog struct {
	Rules   *DungeonUnlockRules
	digest  string
	rewards map[uint32][]DungeonReward
}

func LoadDungeonCatalog(source pvf.Source) (*DungeonCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(DungeonCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseDungeonCatalog(text)
}

func ParseDungeonCatalog(text string) (*DungeonCatalog, error) {
	rules, err := ParseDungeonUnlockRules(text)
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse(DungeonCatalogPath, text)
	if err != nil {
		return nil, err
	}
	c := &DungeonCatalog{Rules: rules, digest: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), rewards: map[uint32][]DungeonReward{}}
	var id uint32
	var inDungeon, inReward bool
	var tier *DungeonReward
	var hasContribution bool
	for _, s := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(s.Name))
		tokens := doc.Tokens[s.Start:s.End]
		ints := func(n int) ([]uint32, error) {
			if n >= 0 && len(tokens) != n {
				return nil, ErrDungeonUnlockRules
			}
			a := make([]uint32, len(tokens))
			for i, t := range tokens {
				if t.Kind != pvf.TokenInt || t.Int < 0 || t.Int > math.MaxInt32 {
					return nil, ErrDungeonUnlockRules
				}
				a[i] = uint32(t.Int)
			}
			return a, nil
		}
		switch name {
		case "dungeon":
			if inDungeon {
				return nil, ErrDungeonUnlockRules
			}
			inDungeon = true
			id = 0
		case "dungeon index":
			a, e := ints(1)
			if e != nil {
				return nil, e
			}
			id = a[0]
		case "reward":
			if inDungeon {
				if inReward {
					return nil, ErrDungeonUnlockRules
				}
				inReward = true
			}
		case "gauge ratio":
			if !inReward || id == 0 || tier != nil {
				return nil, ErrDungeonUnlockRules
			}
			a, e := ints(1)
			if e != nil || a[0] > 100 {
				return nil, ErrDungeonUnlockRules
			}
			tier = &DungeonReward{Ratio: a[0]}
			hasContribution = false
		case "item":
			if !inReward {
				continue
			}
			if tier == nil {
				return nil, ErrDungeonUnlockRules
			}
			a, e := ints(-1)
			if e != nil || len(a) == 0 || len(a)%2 != 0 {
				return nil, ErrDungeonUnlockRules
			}
			for i := 0; i < len(a); i += 2 {
				if a[i] == 0 || a[i+1] == 0 {
					return nil, ErrDungeonUnlockRules
				}
				tier.Items = append(tier.Items, DungeonRewardItem{a[i], a[i+1]})
			}
		case "contribute":
			if !inReward {
				continue
			}
			if tier == nil || hasContribution {
				return nil, ErrDungeonUnlockRules
			}
			a, e := ints(1)
			if e != nil {
				return nil, e
			}
			tier.Contribution = a[0]
			hasContribution = true
		case "/gauge ratio":
			if tier == nil {
				return nil, ErrDungeonUnlockRules
			}
			c.rewards[id] = append(c.rewards[id], *tier)
			tier = nil
		case "party play bonus":
			// Its native reward vector needs its own eligibility owner before use.
			if inReward {
				return nil, fmt.Errorf("%w: unsupported party bonus", ErrDungeonUnlockRules)
			}
		case "/reward":
			if inDungeon {
				if tier != nil || !inReward {
					return nil, ErrDungeonUnlockRules
				}
				inReward = false
			}
		case "/dungeon":
			if inReward || tier != nil {
				return nil, ErrDungeonUnlockRules
			}
			inDungeon = false
		default:
			if inReward {
				return nil, fmt.Errorf("%w: unsupported reward field [%s]", ErrDungeonUnlockRules, name)
			}
		}
	}
	for id := range rules.byID {
		rows := c.rewards[id]
		if len(rows) == 0 {
			return nil, ErrDungeonUnlockRules
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Ratio < rows[j].Ratio })
		if rows[len(rows)-1].Ratio != 100 {
			return nil, ErrDungeonUnlockRules
		}
		for i := 1; i < len(rows); i++ {
			if rows[i-1].Ratio == rows[i].Ratio {
				return nil, ErrDungeonUnlockRules
			}
		}
	}
	return c, nil
}

// V166 03C74270 selects the first threshold greater than or equal to the
// purification ratio. It is not a minimum-threshold reward lookup.
func (c *DungeonCatalog) Reward(id, count uint32) (DungeonReward, error) {
	if c == nil {
		return DungeonReward{}, ErrDungeonUnlockRules
	}
	ratio, err := c.Rules.PurificationRatio(id, count)
	if err != nil {
		return DungeonReward{}, err
	}
	for _, row := range c.rewards[id] {
		if row.Ratio >= ratio {
			row.Items = append([]DungeonRewardItem(nil), row.Items...)
			return row, nil
		}
	}
	return DungeonReward{}, ErrDungeonUnlockRules
}

func (c *DungeonCatalog) ItemIDs() []uint32 {
	seen := map[uint32]bool{}
	for _, rows := range c.rewards {
		for _, row := range rows {
			for _, item := range row.Items {
				seen[item.ItemID] = true
			}
		}
	}
	ids := make([]uint32, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
