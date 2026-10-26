package fiendwar

import (
	"fmt"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

const FiendWarPVFPath = "etc/raid/fiendwar/fiendwar.etc"

// FiendWarConfig keeps the area graph separate from the shared ticket/reward grammar.
type FiendWarConfig struct {
	MemberMax           int
	StartMinimumMembers int
	WeeklyClearLimit    uint32
	EntryCost           AntonEntryCost
	// These values live in the global section of fiendwar.etc and are used by
	// the battle owner when a run is interrupted or failed.
	DisconnectMemberWaitSeconds int
	Retry                       bool
	RetryItem                   AntonEntryCost
	// Ranking rows belong to PHASE / CONDITION FOR RANK, not the globals.
	DeadCount []FiendWarDeadCountRule
	Battle    *FiendWarBattleConfig
}

// FiendWarDeadCountRule maps an inclusive death-count range to the native
// result tier. The PVF uses four rows: min, max, tier.
type FiendWarDeadCountRule struct {
	Min, Max, Tier int
}

// FiendWarDeadCountTier preserves the inclusive ranges from the PVF. Counts
// outside the configured rows have no proven native tier.
func FiendWarDeadCountTier(config *FiendWarConfig, count int) (int, bool) {
	if config != nil {
		for _, rule := range config.DeadCount {
			if count >= rule.Min && count <= rule.Max {
				return rule.Tier, true
			}
		}
	}
	return 0, false
}

func LoadFiendWarConfig(source dnfpvf.Source) (*FiendWarConfig, error) {
	if source == nil {
		return nil, fmt.Errorf("fiendwar raid PVF source is required")
	}
	text, err := source.ReadText(FiendWarPVFPath)
	if err != nil {
		return nil, err
	}
	document, err := dnfpvf.Parse(FiendWarPVFPath, text)
	if err != nil {
		return nil, err
	}
	config, err := ParseFiendWarConfig(document)
	if err != nil {
		return nil, err
	}
	symbolText, err := source.ReadText(FiendWarPVFPath + ".symbol")
	if err != nil {
		return nil, err
	}
	symbols, err := dnfpvf.Parse(FiendWarPVFPath+".symbol", symbolText)
	if err != nil {
		return nil, err
	}
	config.Battle, err = parseFiendWarBattle(config, document, symbols)
	return config, err
}

func ParseFiendWarConfig(document *dnfpvf.Document) (*FiendWarConfig, error) {
	if document == nil || antonPathKey(document.Path) != antonPathKey(FiendWarPVFPath) {
		return nil, fmt.Errorf("invalid fiendwar raid PVF document")
	}
	phases, err := antonFindBlocks(document, "phase", 0, len(document.Sections))
	if err != nil || len(phases) != 1 {
		return nil, fmt.Errorf("invalid fiendwar phase boundary: %v", err)
	}
	end := phases[0].open
	weekly, err := antonReadPositiveIntSection(document, "weekly clear count", 0, end)
	if err != nil || weekly > 255 {
		return nil, fmt.Errorf("invalid fiendwar weekly clear count: %v", err)
	}
	maximum, err := antonReadPositiveIntSection(document, "fiendwar member max", 0, end)
	if err != nil {
		return nil, err
	}
	minimum, err := antonReadPositiveIntSection(document, "fiendwar start minimum member", 0, end)
	if err != nil {
		return nil, err
	}
	if maximum > MaxAttackPartyMembers || minimum > maximum {
		return nil, fmt.Errorf("invalid fiendwar roster bounds: %d..%d", minimum, maximum)
	}
	cost, err := antonParseEntryCost(document, 0, end)
	if err != nil {
		return nil, err
	}
	wait, err := antonReadPositiveIntSection(document, "disconn member wait time", 0, end)
	if err != nil {
		return nil, err
	}
	retryValues, err := fiendWarGlobalInts(document, "retry", 0, end)
	if err != nil || len(retryValues) != 1 || (retryValues[0] != 0 && retryValues[0] != 1) {
		return nil, fmt.Errorf("invalid fiendwar retry: %v", err)
	}
	retryItem, err := antonParseEntryCostNamed(document, "retry item", 0, end)
	if err != nil {
		return nil, err
	}
	ranks, err := antonFindBlocks(document, "condition for rank", phases[0].open+1, phases[0].close)
	if err != nil || len(ranks) != 1 {
		return nil, fmt.Errorf("invalid fiendwar rank boundary: %v", err)
	}
	dead, err := parseFiendWarDeadCounts(document, ranks[0].open+1, ranks[0].close)
	if err != nil {
		return nil, err
	}
	return &FiendWarConfig{
		MemberMax: maximum, StartMinimumMembers: minimum, EntryCost: cost,
		WeeklyClearLimit:            uint32(weekly),
		DisconnectMemberWaitSeconds: wait, Retry: retryValues[0] == 1,
		RetryItem: retryItem, DeadCount: dead,
	}, nil
}

func fiendWarGlobalInts(document *dnfpvf.Document, name string, start, end int) ([]int, error) {
	i, err := antonUniqueSection(document, name, start, end)
	if err != nil {
		return nil, err
	}
	tokens, err := antonSectionTokens(document, i)
	if err != nil {
		return nil, err
	}
	values := make([]int, len(tokens))
	for j, token := range tokens {
		values[j], err = antonIntToken(token, name)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func antonParseEntryCostNamed(document *dnfpvf.Document, name string, start, end int) (AntonEntryCost, error) {
	sectionIndex, found, err := antonOptionalUniqueSection(document, name, start, end)
	if err != nil {
		return AntonEntryCost{}, err
	}
	if !found {
		return AntonEntryCost{}, antonMissing(name, "required")
	}
	tokens, err := antonSectionTokens(document, sectionIndex)
	if err != nil {
		return AntonEntryCost{}, err
	}
	if len(tokens) != 2 {
		return AntonEntryCost{}, fmt.Errorf("%s: want two values", name)
	}
	item, err := antonPositiveUint32Token(tokens[0], name+" item id")
	if err != nil {
		return AntonEntryCost{}, err
	}
	count, err := antonPositiveUint32Token(tokens[1], name+" count")
	if err != nil {
		return AntonEntryCost{}, err
	}
	return AntonEntryCost{ItemID: item, Count: count}, nil
}

func parseFiendWarDeadCounts(document *dnfpvf.Document, start, end int) ([]FiendWarDeadCountRule, error) {
	want := antonSectionName("dead count")
	sections := make([]int, 0, 4)
	for i := start; i < end; i++ {
		if antonSectionName(document.Sections[i].Name) == want {
			sections = append(sections, i)
		}
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("dead count: missing rows")
	}
	out := make([]FiendWarDeadCountRule, 0, len(sections))
	for _, section := range sections {
		tokens, e := antonSectionTokens(document, section)
		if e != nil || len(tokens) != 3 {
			return nil, fmt.Errorf("dead count: want min/max/tier: %v", e)
		}
		values := [3]int{}
		for i, token := range tokens {
			values[i], e = antonIntToken(token, "dead count")
			if e != nil {
				return nil, e
			}
		}
		if values[0] < 0 || values[1] < values[0] || values[2] < 0 {
			return nil, fmt.Errorf("invalid dead count row %v", values)
		}
		out = append(out, FiendWarDeadCountRule{Min: values[0], Max: values[1], Tier: values[2]})
	}
	return out, nil
}
