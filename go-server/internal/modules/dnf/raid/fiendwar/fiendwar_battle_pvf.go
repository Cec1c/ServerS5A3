package fiendwar

import (
	"fmt"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

type FiendWarArea struct {
	Index, WorldMap, Dungeon, LimitSeconds, MemberMax int
	Symbol                                            uint32
	Kind                                              string
	Next                                              []int
}

type FiendWarInstruction struct {
	Op     string
	Symbol uint32
	Args   []dnfpvf.Token
}

type FiendWarRule struct {
	Conditions, Actions []FiendWarInstruction
}

type FiendWarMerchantRate struct {
	Kind, Weight, DurationSeconds int
}

type FiendWarMerchantPosition struct {
	Area, X, Y int
}

type FiendWarBattleConfig struct {
	Raid                     *AntonConfig
	WaitingTown, WaitingArea int
	Areas                    map[int]FiendWarArea
	Symbols                  map[string]uint32
	Rules                    []FiendWarRule
	EnemyRates               []int
	MerchantPositions        []int
	MerchantRates            []FiendWarMerchantRate
	MerchantPositionRows     []FiendWarMerchantPosition
	DieImprisonSeconds       []int
	DailyAccountEnterLimit   int
	WaitingNPCs              []int
}

// Native sub_382A910. These are timer kinds, not packet opcodes.
var FiendWarTimerKinds = map[string]byte{
	"move_enemy": 0, "create_monster": 1, "activate_radar": 2,
	"activate_battery": 3, "top_create_enemy": 4, "mid_create_enemy": 5,
	"bot_create_enemy": 6, "top_restore_center": 7, "mid_restore_center": 8,
	"bot_restore_center": 9, "warning_enemy": 10, "delete_monster": 11,
}

func parseFiendWarBattle(recruitment *FiendWarConfig, doc, symbolDoc *dnfpvf.Document) (*FiendWarBattleConfig, error) {
	c := &FiendWarBattleConfig{Areas: make(map[int]FiendWarArea), Symbols: make(map[string]uint32)}
	// The .symbol file pairs an integer with the following section token.
	for i, token := range symbolDoc.Tokens {
		if token.Kind != dnfpvf.TokenSection {
			continue
		}
		if i == 0 || symbolDoc.Tokens[i-1].Kind != dnfpvf.TokenInt || symbolDoc.Tokens[i-1].Int <= 0 {
			return nil, fmt.Errorf("fiendwar symbol %q has no numeric index", token.Value)
		}
		name := antonSectionName(token.Value)
		if _, exists := c.Symbols[name]; exists {
			return nil, fmt.Errorf("duplicate fiendwar symbol %s", name)
		}
		c.Symbols[name] = uint32(symbolDoc.Tokens[i-1].Int)
	}
	phases, err := antonFindBlocks(doc, "phase", 0, len(doc.Sections))
	if err != nil || len(phases) != 1 {
		return nil, fmt.Errorf("fiendwar phase: %v", err)
	}
	end := phases[0].open
	c.DailyAccountEnterLimit, err = antonReadPositiveIntSection(doc, "daily account enter limit", 0, end)
	if err != nil || c.DailyAccountEnterLimit > 255 {
		return nil, fmt.Errorf("invalid fiendwar daily account limit: %v", err)
	}
	base := &AntonConfig{PhaseMax: 1, MemberMax: recruitment.MemberMax, StartMinimumMembers: recruitment.StartMinimumMembers, EntryCost: recruitment.EntryCost}
	if base.StartDelaySeconds, err = antonReadPositiveIntSection(doc, "start delay time", 0, end); err != nil {
		return nil, err
	}
	if base.PhaseTimeoutSeconds, err = antonReadPositiveIntListSection(doc, "phase time over", 0, end, 1); err != nil {
		return nil, err
	}
	if base.MovieEndSeconds, err = antonReadPositiveIntListSection(doc, "movie end time", 0, end, 2); err != nil {
		return nil, err
	}
	values, err := fiendWarInts(doc, "waiting room", 0, end)
	if err != nil || len(values) != 2 {
		return nil, fmt.Errorf("fiendwar waiting room: %v", err)
	}
	c.WaitingTown, c.WaitingArea = values[0], values[1]
	if c.WaitingNPCs, err = fiendWarInts(doc, "waiting room npc", 0, end); err != nil {
		return nil, err
	}
	if c.DieImprisonSeconds, err = fiendWarInts(doc, "die imprison time", 0, end); err != nil {
		return nil, err
	}
	if len(c.DieImprisonSeconds) == 0 {
		return nil, fmt.Errorf("missing fiendwar imprisonment durations")
	}
	for _, seconds := range c.DieImprisonSeconds {
		if seconds <= 0 {
			return nil, fmt.Errorf("invalid fiendwar imprisonment duration")
		}
	}
	if c.EnemyRates, err = fiendWarInts(doc, "enemy create rate", 0, end); err != nil {
		return nil, err
	}
	if len(c.EnemyRates) == 0 || len(c.EnemyRates)%2 != 0 {
		return nil, fmt.Errorf("invalid fiendwar enemy rates")
	}
	for i := 0; i < len(c.EnemyRates); i += 2 {
		if c.EnemyRates[i] < 1 || c.EnemyRates[i] > 4 || c.EnemyRates[i+1] <= 0 {
			return nil, fmt.Errorf("invalid fiendwar enemy weight")
		}
	}
	if values, err = fiendWarInts(doc, "merchant create pos", 0, end); err != nil {
		return nil, err
	}
	if len(values)%3 != 0 {
		return nil, fmt.Errorf("invalid fiendwar merchant positions")
	}
	for i := 0; i < len(values); i += 3 {
		row := FiendWarMerchantPosition{Area: values[i], X: values[i+1], Y: values[i+2]}
		c.MerchantPositionRows = append(c.MerchantPositionRows, row)
		c.MerchantPositions = append(c.MerchantPositions, row.Area)
	}
	if values, err = fiendWarInts(doc, "merchant create rate", 0, end); err != nil {
		return nil, err
	}
	if len(values) == 0 || len(values)%3 != 0 {
		return nil, fmt.Errorf("invalid fiendwar merchant rates")
	}
	for i := 0; i < len(values); i += 3 {
		if values[i] < 0 || values[i+1] <= 0 || values[i+2] <= 0 {
			return nil, fmt.Errorf("invalid fiendwar merchant rate")
		}
		// 与独立FiendWarConfiguration一致：种类、秒数、权重；第三列合计100000。
		c.MerchantRates = append(c.MerchantRates, FiendWarMerchantRate{Kind: values[i], DurationSeconds: values[i+1], Weight: values[i+2]})
	}
	p := phases[0]
	name, err := antonParseSingleModeName(doc, p)
	if err != nil {
		return nil, err
	}
	phase := AntonPhase{Name: name}
	phase.MinimalDungeonClearCount, err = antonReadPositiveIntSection(doc, "minimal dungeon clear count", p.open+1, p.close)
	if err != nil {
		return nil, err
	}
	rewardBlocks, err := antonFindBlocks(doc, "clear reward item", p.open+1, p.close)
	if err != nil || len(rewardBlocks) != 1 {
		return nil, fmt.Errorf("fiendwar rewards: %v", err)
	}
	if phase.Rewards, err = antonParseRewards(doc, rewardBlocks[0], 0); err != nil {
		return nil, err
	}
	areaBlocks, err := antonFindBlocks(doc, "area info", p.open+1, p.close)
	if err != nil {
		return nil, err
	}
	dungeons := make(map[int]bool)
	for _, b := range areaBlocks {
		a := FiendWarArea{}
		for field, dst := range map[string]*int{"area index": &a.Index, "worldmap index": &a.WorldMap, "dungeon index": &a.Dungeon, "member max": &a.MemberMax} {
			v, e := fiendWarInts(doc, field, b.open+1, b.close)
			if e != nil || len(v) != 1 {
				return nil, fmt.Errorf("fiendwar area %s: %v", field, e)
			}
			*dst = v[0]
		}
		if a.Index < 1 || a.Index > 127 || a.MemberMax < 1 || a.MemberMax > recruitment.MemberMax {
			return nil, fmt.Errorf("invalid fiendwar area %+v", a)
		}
		if _, exists := c.Areas[a.Index]; exists {
			return nil, fmt.Errorf("duplicate fiendwar area %d", a.Index)
		}
		a.Symbol = c.Symbols[fmt.Sprintf("%d area state", a.Index)]
		if a.Symbol == 0 {
			return nil, fmt.Errorf("missing fiendwar area symbol %d", a.Index)
		}
		if a.Next, err = fiendWarInts(doc, "move available map", b.open+1, b.close); err != nil {
			return nil, err
		}
		for i := b.open + 1; i < b.close; i++ {
			field := antonSectionName(doc.Sections[i].Name)
			if strings.HasSuffix(field, " area") && field != "on giveup area" {
				a.Kind = strings.TrimSuffix(field, " area")
			}
		}
		if a.Dungeon > 0 {
			if dungeons[a.Dungeon] {
				return nil, fmt.Errorf("duplicate fiendwar dungeon %d", a.Dungeon)
			}
			dungeons[a.Dungeon] = true
			if a.LimitSeconds, err = antonReadPositiveIntSection(doc, "dungeon limit time", b.open+1, b.close); err != nil {
				return nil, err
			}
			// Each area clear is one completed dungeon, not Anton's HP counter.
			phase.Dungeons = append(phase.Dungeons, AntonDungeonInfo{Index: uint32(a.Dungeon), InitState: AntonDungeonHide, HP: 1, PartyMax: 1})
		}
		c.Areas[a.Index] = a
	}
	if _, ok := c.Areas[c.WaitingArea]; !ok {
		return nil, fmt.Errorf("missing fiendwar waiting area")
	}
	for _, a := range c.Areas {
		for _, n := range a.Next {
			if _, ok := c.Areas[n]; !ok {
				return nil, fmt.Errorf("unknown fiendwar adjacent area %d", n)
			}
		}
	}
	triggers, err := antonFindBlocks(doc, "trigger", p.open+1, p.close)
	if err != nil {
		return nil, err
	}
	behaviors, err := antonFindBlocks(doc, "behavior", p.open+1, p.close)
	if err != nil || len(triggers) != len(behaviors) || len(triggers) == 0 {
		return nil, fmt.Errorf("fiendwar rule pairs: %v", err)
	}
	for i, t := range triggers {
		b := behaviors[i]
		if t.close+1 != b.open {
			return nil, fmt.Errorf("fiendwar unpaired trigger %d", i)
		}
		conditions, e := c.parseInstructions(doc, t, true)
		if e != nil {
			return nil, e
		}
		actions, e := c.parseInstructions(doc, b, false)
		if e != nil {
			return nil, e
		}
		c.Rules = append(c.Rules, FiendWarRule{conditions, actions})
	}
	base.Phases = []AntonPhase{phase}
	c.Raid = base
	return c, nil
}

func fiendWarInts(doc *dnfpvf.Document, name string, start, end int) ([]int, error) {
	i, err := antonUniqueSection(doc, name, start, end)
	if err != nil {
		return nil, err
	}
	tokens, err := antonSectionTokens(doc, i)
	if err != nil {
		return nil, err
	}
	values := make([]int, len(tokens))
	for j, t := range tokens {
		values[j], err = antonIntToken(t, name)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (c *FiendWarBattleConfig) parseInstructions(doc *dnfpvf.Document, b antonSectionBlock, condition bool) ([]FiendWarInstruction, error) {
	var out []FiendWarInstruction
	for i := b.open + 1; i < b.close; i++ {
		name := antonSectionName(doc.Sections[i].Name)
		args, err := antonSectionTokens(doc, i)
		if err != nil {
			return nil, err
		}
		inst := FiendWarInstruction{Op: name, Args: args}
		if name == "if" || name == "set" || name == "on change symbol" {
			i++
			if i >= b.close {
				return nil, fmt.Errorf("fiendwar missing symbol after %s", name)
			}
			inst.Symbol = c.Symbols[antonSectionName(doc.Sections[i].Name)]
			if inst.Symbol == 0 {
				return nil, fmt.Errorf("unknown fiendwar symbol %s", doc.Sections[i].Name)
			}
			inst.Args, err = antonSectionTokens(doc, i)
			if err != nil {
				return nil, err
			}
			if name == "if" && len(inst.Args) == 0 {
				i++
				if i >= b.close || antonSectionName(doc.Sections[i].Name) != "<" {
					return nil, fmt.Errorf("unsupported fiendwar comparison")
				}
				inst.Op = "less"
				inst.Args, err = antonSectionTokens(doc, i)
				if err != nil {
					return nil, err
				}
			}
		}
		shape := ""
		if condition {
			switch inst.Op {
			case "if", "less", "on change area state", "on giveup area", "on enter dungeon", "on enter map", "on giveup dungeon", "on change dungeon state":
				shape = "i"
			case "on change symbol":
				shape = ""
			case "check area state", "check dungeon state":
				shape = "is"
			case "check timer end":
				shape = "si"
			default:
				return nil, fmt.Errorf("unsupported fiendwar condition %s", inst.Op)
			}
		} else {
			switch inst.Op {
			case "set", "create enemy", "delete monster", "warning enemy":
				shape = "i"
			case "move enemy":
				shape = "ii"
			case "set timer":
				shape = "sii"
			case "reset timer":
				shape = "si"
			case "create merchant", "create monsters", "delete all enemy", "boss pattern init", "clear phase", "fail fiendwar":
				shape = ""
			default:
				return nil, fmt.Errorf("unsupported fiendwar action %s", inst.Op)
			}
		}
		if len(inst.Args) != len(shape) {
			return nil, fmt.Errorf("fiendwar %s argument count", inst.Op)
		}
		for j, k := range shape {
			if k == 'i' && inst.Args[j].Kind != dnfpvf.TokenInt || k == 's' && inst.Args[j].Kind != dnfpvf.TokenString {
				return nil, fmt.Errorf("fiendwar %s argument type", inst.Op)
			}
		}
		if strings.Contains(inst.Op, "timer") {
			if _, ok := FiendWarTimerKinds[inst.Args[0].Value]; !ok {
				return nil, fmt.Errorf("unknown fiendwar timer")
			}
		}
		out = append(out, inst)
	}
	return out, nil
}
