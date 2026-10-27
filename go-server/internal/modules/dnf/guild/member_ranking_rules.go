package guild

import (
	"longheng.io/server/internal/modules/dnf/pvf"
	"strings"
)

type MemberRankingRules struct {
	RaidClears                      [3]float64
	SameGuildRaidBonus, MileageRate float64
	PartyClears                     map[uint32]RankingPoints
}

// ParseMemberRankingRules keeps personal factors separate from the similarly
// named guild and guild-member blocks (247, for example, has different points).
func ParseMemberRankingRules(text string) (*MemberRankingRules, error) {
	doc, err := pvf.Parse(RankingRulesPath, text)
	if err != nil {
		return nil, err
	}
	rules := &MemberRankingRules{PartyClears: map[uint32]RankingPoints{}}
	active, finished, raid := false, false, false
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		values := doc.Tokens[section.Start:section.End]
		if name == "guild member ranking factor" {
			if active || finished || len(values) != 0 {
				return nil, ErrRankingRules
			}
			active = true
			continue
		}
		if !active {
			continue
		}
		if name == "/guild member ranking factor" {
			if raid || len(values) != 0 {
				return nil, ErrRankingRules
			}
			active = false
			finished = true
			continue
		}
		if strings.HasPrefix(name, "/") {
			if len(values) != 0 {
				return nil, ErrRankingRules
			}
			switch name {
			case "/raid clear":
				if !raid {
					return nil, ErrRankingRules
				}
				raid = false
			case "/point":
				if !raid || !seen["point"] {
					return nil, ErrRankingRules
				}
			case "/guild party clear":
				if raid || !seen["guild party clear"] {
					return nil, ErrRankingRules
				}
			default:
				return nil, ErrRankingRules
			}
			continue
		}
		if seen[name] {
			return nil, ErrRankingRules
		}
		seen[name] = true
		switch name {
		case "raid clear":
			if raid || len(values) != 0 {
				return nil, ErrRankingRules
			}
			raid = true
		case "point":
			if !raid || len(values) != 6 {
				return nil, ErrRankingRules
			}
			modes := map[string]int{"anton": 0, "luke": 1, "fiendwar": 2}
			modesSeen := map[int]bool{}
			for i := 0; i < len(values); i += 2 {
				mode, ok := modes[strings.ToLower(values[i].Value)]
				if !ok || values[i].Kind != pvf.TokenString || modesSeen[mode] {
					return nil, ErrRankingRules
				}
				modesSeen[mode] = true
				rules.RaidClears[mode], err = rankingNumber(values[i+1])
				if err != nil {
					return nil, err
				}
			}
		case "with guild member bonus point", "mileage count point rate":
			if len(values) != 1 || (name == "with guild member bonus point") != raid {
				return nil, ErrRankingRules
			}
			value, e := rankingNumber(values[0])
			if e != nil {
				return nil, e
			}
			if raid {
				rules.SameGuildRaidBonus = value
			} else {
				rules.MileageRate = value
			}
		case "guild party clear":
			if raid || len(values) == 0 || len(values)%3 != 0 {
				return nil, ErrRankingRules
			}
			for i := 0; i < len(values); i += 3 {
				id, e := rankingID(values[i])
				if e != nil {
					return nil, e
				}
				if _, ok := rules.PartyClears[id]; ok {
					return nil, ErrRankingRules
				}
				pair, e := rankingPair(values[i+1 : i+3])
				if e != nil {
					return nil, e
				}
				rules.PartyClears[id] = pair
			}
		default:
			return nil, ErrRankingRules
		}
	}
	if active || !finished || !seen["point"] || !seen["with guild member bonus point"] || !seen["mileage count point rate"] || len(rules.PartyClears) == 0 {
		return nil, ErrRankingRules
	}
	return rules, nil
}
