package guild

import (
	"math"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const (
	GuardianInvestAP         uint32 = 0
	GuardianInvestGP         uint32 = 1
	GuardianSkillFirst       uint32 = 2
	GuardianSkillLast        uint32 = 6
	GuardianGrowthPermission uint32 = 1 << 16
)

type GuardianGrowthCatalog struct {
	levels [50]EssenceBalance
	skills [5]EssenceBalance
}

func LoadGuardianGrowthCatalog(source pvf.Source) (*GuardianGrowthCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(EssenceCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseGuardianGrowthCatalog(text)
}

func ParseGuardianGrowthCatalog(text string) (*GuardianGrowthCatalog, error) {
	doc, err := pvf.Parse(EssenceCatalogPath, text)
	if err != nil {
		return nil, err
	}
	c := &GuardianGrowthCatalog{}
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		var rows []EssenceBalance
		switch name {
		case "guardian exp":
			rows = c.levels[:]
		case "guardian skill":
			rows = c.skills[:]
		default:
			continue
		}
		tokens := doc.Tokens[section.Start:section.End]
		if seen[name] || len(tokens) != (len(rows)-1)*3 {
			return nil, ErrGuardianCatalog
		}
		seen[name] = true
		for i := 1; i < len(rows); i++ {
			t := tokens[(i-1)*3 : i*3]
			if t[0].Kind != pvf.TokenInt || t[0].Int != int64(i) || t[1].Kind != pvf.TokenInt || t[2].Kind != pvf.TokenInt || t[1].Int <= 0 || t[1].Int > math.MaxInt32 || t[2].Int < 0 || t[2].Int > math.MaxInt32 {
				return nil, ErrGuardianCatalog
			}
			rows[i] = EssenceBalance{AP: uint32(t[1].Int), GP: uint32(t[2].Int)}
		}
	}
	if !seen["guardian exp"] || !seen["guardian skill"] {
		return nil, ErrGuardianCatalog
	}
	return c, nil
}

// growth computes a single level's bounded investment or one unlocked skill
// upgrade. Caller commits the returned row, debit and receipt in one UOW.
func (c *GuardianGrowthCatalog) growth(row Guardian, action, observed, value uint32) (Guardian, EssenceBalance, error) {
	fail := func(err error) (Guardian, EssenceBalance, error) { return Guardian{}, EssenceBalance{}, err }
	if c == nil {
		return fail(ErrGuardianCatalog)
	}
	if row.ID == 0 {
		return fail(ErrGuardianNotOwned)
	}
	if row.Level < 1 || row.Level > 50 || value == 0 || value > math.MaxInt32 {
		return fail(ErrGuardianInteraction)
	}
	cost := EssenceBalance{}
	if action == GuardianInvestAP || action == GuardianInvestGP {
		if row.Level == 50 {
			return fail(ErrGuardianInteraction)
		}
		required := c.levels[row.Level]
		if required.AP == 0 || row.APProgress > required.AP || row.GPProgress > required.GP {
			return fail(ErrGuardianState)
		}
		progress, limit, debit := &row.APProgress, required.AP, &cost.AP
		if action == GuardianInvestGP {
			progress, limit, debit = &row.GPProgress, required.GP, &cost.GP
		}
		if observed != *progress || value > limit-*progress {
			return fail(ErrGuardianInteraction)
		}
		*progress += value
		*debit = value
		if row.APProgress == required.AP && row.GPProgress == required.GP {
			row.Level++
			row.APProgress = 0
			row.GPProgress = 0
		}
	} else if action >= GuardianSkillFirst && action <= GuardianSkillLast {
		slot := action - GuardianSkillFirst
		if slot > uint32((row.Level-1)/10) {
			return fail(ErrGuardianInteraction)
		}
		level := uint32(row.Skills[slot])
		if level < 1 || level >= 5 || observed != level || value != level+1 {
			return fail(ErrGuardianInteraction)
		}
		cost = c.skills[level]
		if cost.AP == 0 {
			return fail(ErrGuardianCatalog)
		}
		row.Skills[slot] = uint8(value)
	} else {
		return fail(ErrGuardianInteraction)
	}
	return row, cost, nil
}
