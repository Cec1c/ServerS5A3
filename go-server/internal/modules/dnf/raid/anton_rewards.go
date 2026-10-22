package raid

import (
	"fmt"
	"math"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

type AntonRewardChoice struct {
	Type        string
	Rank        int
	Weight      uint32
	ContainerID uint32
	Flags       byte
}
type AntonRankRange struct {
	Minimum, Maximum uint32
	Rank             byte
}
type AntonPhaseRewards struct {
	MinimumClears uint32
	Ranks         []AntonRankRange
	Choices       []AntonRewardChoice
}
type AntonRewardConfiguration struct{ Phases []AntonPhaseRewards }

func (c AntonRewardConfiguration) Clone() AntonRewardConfiguration {
	c.Phases = append([]AntonPhaseRewards(nil), c.Phases...)
	for i := range c.Phases {
		c.Phases[i].Ranks = append([]AntonRankRange(nil), c.Phases[i].Ranks...)
		c.Phases[i].Choices = append([]AntonRewardChoice(nil), c.Phases[i].Choices...)
	}
	return c
}

// Reads only normal Anton PHASE sections in the actual archive. Single raid
// and item-container expansion have separate owners; no legacy items are used
// when a reward dependency is missing.
func LoadAntonRewardConfiguration(source dnfpvf.Source) (AntonRewardConfiguration, error) {
	return loadConfiguredRaidRewards(source, "etc/raid/anton.etc", 0)
}

// Luke uses its own actual reward table: phase zero qualifies all admitted
// members and has no squad reward; phase one requires two owned clears.
func LoadLukeRewardConfiguration(source dnfpvf.Source) (AntonRewardConfiguration, error) {
	return loadConfiguredRaidRewards(source, "etc/raid/luke/luke.etc", 1)
}

func LoadFiendWarRewardConfiguration(source dnfpvf.Source) (AntonRewardConfiguration, error) {
	return loadConfiguredRaidRewards(source, "etc/raid/fiendwar/fiendwar.etc", 2)
}

func loadConfiguredRaidRewards(source dnfpvf.Source, path string, family byte) (AntonRewardConfiguration, error) {
	luke, fiend := family == 1, family == 2
	phaseCount := 2
	if fiend {
		phaseCount = 1
	}
	if !fiend {
		if _, err := LoadBattleConfiguration(source, path); err != nil {
			return AntonRewardConfiguration{}, err
		}
	}
	text, err := source.ReadText(path)
	if err != nil {
		return AntonRewardConfiguration{}, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return AntonRewardConfiguration{}, err
	}
	var result AntonRewardConfiguration
	phase, inPhase, inRanks, inRewards := -1, false, false, false
	var minimumSeen []bool
	for _, section := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(section.Name))
		values := sectionValues(doc, section)
		switch name {
		case "PHASE":
			if inPhase {
				return result, fmt.Errorf("nested Anton reward phase")
			}
			phase++
			inPhase = true
			result.Phases = append(result.Phases, AntonPhaseRewards{})
			minimumSeen = append(minimumSeen, false)
		case "/PHASE":
			if !inPhase || inRanks || inRewards {
				return result, fmt.Errorf("unclosed Anton reward/rank section")
			}
			inPhase, inRanks, inRewards = false, false, false
		case "CONDITION FOR RANK":
			if inPhase {
				inRanks = true
			}
		case "/CONDITION FOR RANK":
			inRanks = false
		case "CLEAR REWARD ITEM":
			if inPhase {
				inRewards = true
			}
		case "/CLEAR REWARD ITEM":
			inRewards = false
		case "MINIMAL DUNGEON CLEAR COUNT":
			if !inPhase {
				continue
			}
			if len(values) != 1 {
				return result, fmt.Errorf("Anton minimum clear count unavailable")
			}
			count, err := antonUint(values[0])
			if err != nil || count == 0 && !(luke && phase == 0) || minimumSeen[phase] {
				return result, fmt.Errorf("invalid Anton minimum clear count")
			}
			result.Phases[phase].MinimumClears = count
			minimumSeen[phase] = true
		case "DEAD COUNT":
			if !inPhase || !inRanks {
				continue
			}
			if len(values) != 3 {
				return result, fmt.Errorf("incomplete Anton rank range")
			}
			minimum, e1 := antonUint(values[0])
			maximum, e2 := antonUint(values[1])
			rank, e3 := antonUint(values[2])
			if e1 != nil || e2 != nil || e3 != nil || minimum > maximum || rank > 3 {
				return result, fmt.Errorf("invalid Anton rank range")
			}
			ranges := result.Phases[phase].Ranks
			if len(ranges) == 0 && minimum != 0 || len(ranges) > 0 && (uint64(ranges[len(ranges)-1].Maximum)+1 != uint64(minimum) || rank <= uint32(ranges[len(ranges)-1].Rank)) {
				return result, fmt.Errorf("overlapping/discontinuous Anton rank ranges")
			}
			result.Phases[phase].Ranks = append(ranges, AntonRankRange{minimum, maximum, byte(rank)})
		case "STATE REWARD":
			if !inPhase || !inRewards {
				continue
			}
			if len(values) != 5 || values[0].Kind != dnfpvf.TokenString || values[1].Kind != dnfpvf.TokenInt || values[1].Int < -1 || values[1].Int > 3 {
				return result, fmt.Errorf("incomplete Anton state reward")
			}
			typ := strings.ToLower(values[0].Value)
			if typ != "party_card" && typ != "pcroom_card" && typ != "squad_item" && !(fiend && typ == "fw_party_card") {
				return result, fmt.Errorf("unsupported configured Anton reward type %q", typ)
			}
			weight, e1 := antonUint(values[2])
			item, e2 := antonUint(values[3])
			flags, e3 := antonUint(values[4])
			if e1 != nil || e2 != nil || e3 != nil || weight == 0 || item == 0 || flags > 1 {
				return result, fmt.Errorf("invalid Anton state reward item/weight/flags")
			}
			result.Phases[phase].Choices = append(result.Phases[phase].Choices, AntonRewardChoice{typ, int(values[1].Int), weight, item, byte(flags)})
		}
	}
	if inPhase || len(result.Phases) != phaseCount {
		return result, fmt.Errorf("incomplete normal Anton reward phases")
	}
	for phaseIndex, p := range result.Phases {
		if !minimumSeen[phaseIndex] || p.MinimumClears == 0 && !(luke && phaseIndex == 0) || len(p.Ranks) == 0 || len(p.Choices) == 0 {
			return result, fmt.Errorf("Anton reward phase dependencies missing")
		}
		for rank := byte(0); rank <= 3; rank++ {
			types := []string{"party_card", "pcroom_card", "squad_item"}
			if fiend {
				types = append(types, "fw_party_card")
			}
			for _, typ := range types {
				if luke && phaseIndex == 0 && typ == "squad_item" {
					continue
				}
				if _, total, err := p.Pool(typ, rank); err != nil || total == 0 {
					return result, fmt.Errorf("Anton reward pool missing for %s/rank%d", typ, rank)
				}
			}
		}
	}
	return result, nil
}

func (p AntonPhaseRewards) RankForDeaths(deaths uint32) (byte, error) {
	for _, r := range p.Ranks {
		if deaths >= r.Minimum && deaths <= r.Maximum {
			return r.Rank, nil
		}
	}
	// The raid reference retains the worst rank after its terminal range.
	// Use the actual table's terminal rank; never substitute an absent table.
	if len(p.Ranks) > 0 {
		last := p.Ranks[len(p.Ranks)-1]
		if last.Rank == 3 && deaths > last.Maximum {
			return last.Rank, nil
		}
	}
	return 0, fmt.Errorf("Anton death count has no configured rank")
}

func (p AntonPhaseRewards) Pool(typ string, rank byte) ([]AntonRewardChoice, uint32, error) {
	if rank > 3 {
		return nil, 0, fmt.Errorf("Anton reward rank invalid")
	}
	var exact, common []AntonRewardChoice
	for _, c := range p.Choices {
		if c.Type != typ {
			continue
		}
		if c.Rank == int(rank) {
			exact = append(exact, c)
		} else if c.Rank == -1 {
			common = append(common, c)
		}
	}
	choices := exact
	if len(choices) == 0 {
		choices = common
	}
	var total uint64
	for _, c := range choices {
		total += uint64(c.Weight)
	}
	if total == 0 || total > math.MaxUint32 {
		return nil, 0, fmt.Errorf("Anton reward pool weight unavailable")
	}
	return append([]AntonRewardChoice(nil), choices...), uint32(total), nil
}

// Caller supplies a uniform draw within the actual configured total and freezes
// the returned choice before a transaction/retry; this function performs no IO.
func (p AntonPhaseRewards) Select(typ string, rank byte, draw uint32) (AntonRewardChoice, error) {
	choices, total, err := p.Pool(typ, rank)
	if err != nil || draw >= total {
		return AntonRewardChoice{}, fmt.Errorf("Anton reward draw outside pool")
	}
	for _, c := range choices {
		if draw < c.Weight {
			return c, nil
		}
		draw -= c.Weight
	}
	return AntonRewardChoice{}, fmt.Errorf("Anton reward draw unresolved")
}
