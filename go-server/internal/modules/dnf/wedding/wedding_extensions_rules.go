package wedding

import (
	"fmt"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const TreePVFPath = "decoration/coupletree.etc"
const CapsulePVFPath = "etc/couplegrowthcapsule.etc"

type TreeReward struct{ ItemID, Weight, Count int64 }
type TreeRule struct {
	Index, Days, DailyWater, MaximumGrowth int64
	Water                                  map[int64]int64
	Rewards                                []TreeReward
}
type CapsuleRules struct{ MaximumOwn, MaximumTaken, Gain, MaximumGauge, ItemID int64 }
type ExtensionRules struct {
	Seeds        map[int64]int64
	Trees        map[int64]TreeRule
	RewardLetter [2]int64
	Capsule      CapsuleRules
	DivorceGold  [2]int64
	DivorceDays  int64
	PartnerMove  PartnerMoveRules
}

func LoadExtensionRules(source pvf.Source) (*ExtensionRules, error) {
	if source == nil {
		return nil, ErrUnavailable
	}
	read := func(path string) (*pvf.Document, error) {
		text, e := source.ReadText(path)
		if e != nil {
			return nil, e
		}
		return pvf.Parse(path, text)
	}
	system, e := read(SystemPath)
	if e != nil {
		return nil, e
	}
	tree, e := read(TreePVFPath)
	if e != nil {
		return nil, e
	}
	capsule, e := read(CapsulePVFPath)
	if e != nil {
		return nil, e
	}
	r := &ExtensionRules{Seeds: map[int64]int64{}, Trees: map[int64]TreeRule{}}
	alimony := system.Ints("alimony")
	days := system.Ints("days after divorce")
	if len(alimony) != 2 || alimony[0] < 0 || alimony[1] < 0 || len(days) != 1 || days[0] < 0 || days[0] > 36500 {
		return nil, fmt.Errorf("wedding: invalid divorce rules")
	}
	copy(r.DivorceGold[:], alimony)
	r.DivorceDays = days[0]
	seed := tree.Ints("seed")
	letter := tree.Ints("reward letter")
	if len(seed) == 0 || len(seed)%2 != 0 || len(letter) != 2 || letter[0] <= 0 || letter[1] <= 0 {
		return nil, fmt.Errorf("wedding: invalid tree seed/letter table")
	}
	copy(r.RewardLetter[:], letter)
	for i := 0; i < len(seed); i += 2 {
		if seed[i] <= 0 || seed[i] > 0xffffffff || seed[i+1] <= 0 || r.Seeds[seed[i]] != 0 {
			return nil, ErrItem
		}
		r.Seeds[seed[i]] = seed[i+1]
	}
	var current *TreeRule
	for _, section := range tree.Sections {
		tokens := tree.Tokens[section.Start:section.End]
		values := []int64{}
		for _, t := range tokens {
			if t.Kind == pvf.TokenInt {
				values = append(values, t.Int)
			}
		}
		name := strings.ToLower(strings.TrimSpace(section.Name))
		if name == "tree" {
			if current != nil {
				return nil, ErrState
			}
			current = &TreeRule{Water: map[int64]int64{}}
			continue
		}
		if current == nil {
			continue
		}
		switch name {
		case "index":
			if len(values) != 1 {
				return nil, ErrState
			}
			current.Index = values[0]
		case "expire day":
			if len(values) != 1 {
				return nil, ErrState
			}
			current.Days = values[0]
		case "day water count":
			if len(values) != 1 {
				return nil, ErrState
			}
			current.DailyWater = values[0]
		case "grow point":
			if len(tokens) == 0 || len(tokens)%2 != 0 {
				return nil, ErrState
			}
			for i := 0; i < len(tokens); i += 2 {
				a, b := tokens[i], tokens[i+1]
				if a.Kind != pvf.TokenInt || b.Kind != pvf.TokenInt || a.Int <= 0 || b.Int <= 0 || current.Water[a.Int] != 0 {
					return nil, ErrItem
				}
				current.Water[a.Int] = b.Int
			}
		case "step":
			if len(tokens) == 0 || len(tokens)%2 != 0 {
				return nil, ErrState
			}
			for i := 0; i < len(tokens); i += 2 {
				if tokens[i].Kind != pvf.TokenInt || tokens[i].Int <= current.MaximumGrowth || tokens[i+1].Kind != pvf.TokenString {
					return nil, ErrState
				}
				current.MaximumGrowth = tokens[i].Int
			}
		case "reward":
			if len(tokens) == 0 || len(tokens)%3 != 0 {
				return nil, ErrState
			}
			for i := 0; i < len(tokens); i += 3 {
				a, b, c := tokens[i], tokens[i+1], tokens[i+2]
				if a.Kind != pvf.TokenInt || b.Kind != pvf.TokenInt || c.Kind != pvf.TokenInt || a.Int <= 0 || a.Int > 0xffffffff || b.Int <= 0 || b.Int > 1000000000 || c.Int <= 0 || c.Int > 0x7fffffff {
					return nil, ErrItem
				}
				current.Rewards = append(current.Rewards, TreeReward{a.Int, b.Int, c.Int})
			}
		case "/tree":
			if current.Index <= 0 || current.Index > 0xffffffff || current.Days <= 0 || current.Days > 36500 || current.DailyWater <= 0 || current.DailyWater > 127 || current.MaximumGrowth <= 0 || current.MaximumGrowth > 0xffffffff || len(current.Water) == 0 || len(current.Rewards) == 0 {
				return nil, ErrState
			}
			if _, duplicate := r.Trees[current.Index]; duplicate {
				return nil, ErrState
			}
			r.Trees[current.Index] = *current
			current = nil
		}
	}
	if current != nil || len(r.Trees) == 0 {
		return nil, ErrState
	}
	for _, index := range r.Seeds {
		if _, found := r.Trees[index]; !found {
			return nil, ErrState
		}
	}
	for key, pointer := range map[string]*int64{"max own count": &r.Capsule.MaximumOwn, "max taken count": &r.Capsule.MaximumTaken, "gage": &r.Capsule.Gain, "max gage": &r.Capsule.MaximumGauge, "capsule id": &r.Capsule.ItemID} {
		values := capsule.Ints(key)
		if len(values) != 1 {
			return nil, ErrState
		}
		*pointer = values[0]
	}
	if !r.Capsule.valid() {
		return nil, ErrState
	}
	r.PartnerMove, e = LoadPartnerMoveRules(source)
	if e != nil {
		return nil, e
	}
	return r, nil
}

func (r CapsuleRules) valid() bool {
	return r.MaximumOwn > 0 && r.MaximumOwn <= 255 && r.MaximumTaken > 0 && r.MaximumTaken <= 255 && r.Gain > 0 && r.Gain <= r.MaximumGauge && r.MaximumGauge <= 0x7fffffff && r.ItemID > 0 && r.ItemID <= 0xffffffff
}
