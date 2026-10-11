package raid

import (
	"fmt"
	"math"
	"sort"
	"strings"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Fiend War has a battlefield graph in addition to the final two DGN entries.
// Keep the graph as its own configuration; an Anton dungeon list cannot stand
// in for intermediate areas, lane movement, capacity or individual deadlines.
type FiendWarArea struct {
	ID                      uint32
	WorldmapID, DungeonID   int64
	LimitSeconds, MemberMax uint32
	InitialState            string
	Moves                   []uint32
	Role                    string
}
type FiendWarGuide struct{ From, To, Direction uint32 }
type FiendWarWeightedEnemy struct{ Kind, Weight uint32 }
type FiendWarMerchantVariant struct{ Kind, Duration, Weight uint32 }
type FiendWarMerchantPosition struct {
	Area uint32
	X, Y int64
}
type FiendWarConfiguration struct {
	Battle               BattleConfiguration
	MemberMax, MemberMin uint32
	Areas                []FiendWarArea
	Guides               []FiendWarGuide
	Symbols              map[string]uint32
	DieImprisonSeconds   []uint32
	EnemyWeights         []FiendWarWeightedEnemy
	MerchantVariants     []FiendWarMerchantVariant
	MerchantPositions    []FiendWarMerchantPosition
	MovieEndSeconds      [2]uint32
}

func (c FiendWarConfiguration) DungeonDependencies() []uint32 {
	seen := map[uint32]bool{}
	for _, a := range c.Areas {
		if a.DungeonID > 0 {
			seen[uint32(a.DungeonID)] = true
		}
	}
	var result []uint32
	for id := range seen {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func LoadFiendWarConfiguration(source dnfpvf.Source) (FiendWarConfiguration, error) {
	const ref = "etc/raid/fiendwar/fiendwar.etc"
	c := FiendWarConfiguration{}
	var err error
	c.Battle, err = LoadBattleConfiguration(source, ref)
	if err != nil {
		return c, err
	}
	if len(c.Battle.PhaseDungeons) != 1 {
		return c, fmt.Errorf("Fiend War phase configuration unavailable")
	}
	text, err := source.ReadText(ref)
	if err != nil {
		return c, err
	}
	doc, err := dnfpvf.Parse(ref, text)
	if err != nil {
		return c, err
	}
	c.Symbols, err = loadRaidSymbols(source, ref+".symbol")
	if err != nil {
		return c, err
	}
	positive := func(name string) (uint32, error) {
		x := doc.Ints(name)
		if len(x) != 1 || x[0] <= 0 || x[0] > math.MaxUint32 {
			return 0, fmt.Errorf("Fiend War %s unavailable", name)
		}
		return uint32(x[0]), nil
	}
	c.MemberMax, err = positive("FIENDWAR MEMBER MAX")
	if err != nil {
		return c, err
	}
	c.MemberMin, err = positive("FIENDWAR START MINIMUM MEMBER")
	if err != nil || c.MemberMax > 8 || c.MemberMin > c.MemberMax {
		return c, fmt.Errorf("Fiend War member limits invalid")
	}
	movies := doc.Ints("MOVIE END TIME")
	if len(movies) != 2 {
		return c, fmt.Errorf("Fiend War result timing unavailable")
	}
	for i, x := range movies {
		if x <= 0 || x > math.MaxUint32 {
			return c, fmt.Errorf("Fiend War movie timing invalid")
		}
		c.MovieEndSeconds[i] = uint32(x)
	}
	roles := map[string]bool{"RADAR AREA": true, "BATTERY AREA": true, "TOP OUTPOST AREA": true, "MID OUTPOST AREA": true, "BOT OUTPOST AREA": true, "TOP COMMAND CENTER AREA": true, "MID COMMAND CENTER AREA": true, "BOT COMMAND CENTER AREA": true, "TOP BOSS AREA": true, "BOT BOSS AREA": true}
	seenRoles := map[string]bool{}
	var current *FiendWarArea
	fields := map[string]bool{}
	for _, section := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(section.Name))
		v := sectionValues(doc, section)
		if name == "AREA INFO" {
			if current != nil {
				return c, fmt.Errorf("nested Fiend War area")
			}
			current = &FiendWarArea{WorldmapID: -2, DungeonID: -2}
			fields = map[string]bool{}
			continue
		}
		if name == "/AREA INFO" {
			if current == nil || current.ID == 0 || current.InitialState == "" || current.MemberMax == 0 || len(current.Moves) == 0 || current.WorldmapID < -1 || current.DungeonID < -1 || (current.WorldmapID == -1) != (current.DungeonID == -1) || (current.DungeonID > 0 && current.LimitSeconds == 0) {
				return c, fmt.Errorf("incomplete Fiend War area")
			}
			c.Areas = append(c.Areas, *current)
			current = nil
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(name, "/") {
			continue
		}
		if fields[name] {
			return c, fmt.Errorf("repeated Fiend War area field %s", name)
		}
		fields[name] = true
		if roles[name] {
			if len(v) != 0 || current.Role != "" || seenRoles[name] {
				return c, fmt.Errorf("invalid Fiend War area role")
			}
			current.Role = name
			seenRoles[name] = true
			continue
		}
		if name == "INIT STATE" {
			if len(v) != 0 {
				return c, fmt.Errorf("Fiend War area state wrapper invalid")
			}
			continue
		}
		if name == fmt.Sprintf("%d AREA STATE", current.ID) {
			if len(v) != 1 || v[0].Kind != dnfpvf.TokenString || strings.TrimSpace(v[0].Value) == "" {
				return c, fmt.Errorf("Fiend War initial area state invalid")
			}
			current.InitialState = v[0].Value
			continue
		}
		if name == "MOVE AVAILABLE MAP" {
			seen := map[uint32]bool{}
			for _, t := range v {
				id, e := antonUint(t)
				if e != nil || id == 0 || seen[id] {
					return c, fmt.Errorf("invalid Fiend War movement edge")
				}
				seen[id] = true
				current.Moves = append(current.Moves, id)
			}
			continue
		}
		if len(v) != 1 || v[0].Kind != dnfpvf.TokenInt {
			return c, fmt.Errorf("unknown Fiend War area field %s", name)
		}
		x := v[0].Int
		switch name {
		case "AREA INDEX":
			if x <= 0 || x > 31 {
				return c, fmt.Errorf("Fiend War area index invalid")
			}
			current.ID = uint32(x)
		case "WORLDMAP INDEX":
			if x < -1 || x > math.MaxUint32 || x == 0 {
				return c, fmt.Errorf("Fiend War worldmap invalid")
			}
			current.WorldmapID = x
		case "DUNGEON INDEX":
			if x < -1 || x > math.MaxUint32 || x == 0 {
				return c, fmt.Errorf("Fiend War dungeon invalid")
			}
			current.DungeonID = x
		case "DUNGEON LIMIT TIME":
			if x <= 0 || x > math.MaxUint32 {
				return c, fmt.Errorf("Fiend War area deadline invalid")
			}
			current.LimitSeconds = uint32(x)
		case "MEMBER MAX":
			if x <= 0 || x > int64(c.MemberMax) {
				return c, fmt.Errorf("Fiend War area capacity invalid")
			}
			current.MemberMax = uint32(x)
		default:
			return c, fmt.Errorf("unknown Fiend War area directive %s", name)
		}
	}
	if current != nil || len(c.Areas) == 0 || len(seenRoles) != len(roles) {
		return c, fmt.Errorf("Fiend War areas or roles incomplete")
	}
	areaIDs := map[uint32]FiendWarArea{}
	dungeons := map[int64]bool{}
	worlds := map[int64]bool{}
	for _, a := range c.Areas {
		if _, ok := areaIDs[a.ID]; ok {
			return c, fmt.Errorf("repeated Fiend War area")
		}
		areaIDs[a.ID] = a
		if c.Symbols[fmt.Sprintf("%d AREA STATE", a.ID)] == 0 {
			return c, fmt.Errorf("Fiend War area symbol unavailable")
		}
		if a.DungeonID > 0 {
			if dungeons[a.DungeonID] || worlds[a.WorldmapID] {
				return c, fmt.Errorf("repeated Fiend War dungeon/worldmap owner")
			}
			dungeons[a.DungeonID] = true
			worlds[a.WorldmapID] = true
		}
	}
	for _, a := range c.Areas {
		for _, id := range a.Moves {
			if _, ok := areaIDs[id]; !ok || id == a.ID {
				return c, fmt.Errorf("Fiend War movement target unavailable")
			}
		}
	}
	guide := doc.Ints("GUIDE INFO")
	if len(guide) == 0 || len(guide)%3 != 0 {
		return c, fmt.Errorf("Fiend War guide graph unavailable")
	}
	for i := 0; i < len(guide); i += 3 {
		if guide[i] <= 0 || guide[i] > 31 || guide[i+1] <= 0 || guide[i+1] > 31 || guide[i+2] < 0 || guide[i+2] > 3 {
			return c, fmt.Errorf("Fiend War guide edge invalid")
		}
		g := FiendWarGuide{uint32(guide[i]), uint32(guide[i+1]), uint32(guide[i+2])}
		a, ok := areaIDs[g.From]
		has := false
		for _, id := range a.Moves {
			has = has || id == g.To
		}
		if !ok || !has {
			return c, fmt.Errorf("Fiend War guide is outside movement graph")
		}
		c.Guides = append(c.Guides, g)
	}
	for _, x := range doc.Ints("DIE IMPRISON TIME") {
		if x <= 0 || x > math.MaxUint32 {
			return c, fmt.Errorf("Fiend War imprisonment duration invalid")
		}
		c.DieImprisonSeconds = append(c.DieImprisonSeconds, uint32(x))
	}
	if len(c.DieImprisonSeconds) == 0 {
		return c, fmt.Errorf("Fiend War imprisonment durations unavailable")
	}
	enemies := doc.Ints("ENEMY CREATE RATE")
	total := int64(0)
	seenEnemies := map[int64]bool{}
	if len(enemies) == 0 || len(enemies)%2 != 0 {
		return c, fmt.Errorf("Fiend War enemy weights unavailable")
	}
	for i := 0; i < len(enemies); i += 2 {
		if enemies[i] <= 0 || enemies[i] > math.MaxUint32 || enemies[i+1] <= 0 || enemies[i+1] > math.MaxUint32 || seenEnemies[enemies[i]] {
			return c, fmt.Errorf("Fiend War enemy weight invalid")
		}
		seenEnemies[enemies[i]] = true
		total += enemies[i+1]
		c.EnemyWeights = append(c.EnemyWeights, FiendWarWeightedEnemy{uint32(enemies[i]), uint32(enemies[i+1])})
	}
	if total > math.MaxUint32 {
		return c, fmt.Errorf("Fiend War enemy total overflow")
	}
	merchants := doc.Ints("MERCHANT CREATE RATE")
	if len(merchants) == 0 || len(merchants)%3 != 0 {
		return c, fmt.Errorf("Fiend War merchant variants unavailable")
	}
	for i := 0; i < len(merchants); i += 3 {
		if merchants[i] < 0 || merchants[i] > math.MaxUint32 || merchants[i+1] <= 0 || merchants[i+1] > math.MaxUint32 || merchants[i+2] <= 0 || merchants[i+2] > math.MaxUint32 {
			return c, fmt.Errorf("Fiend War merchant variant invalid")
		}
		c.MerchantVariants = append(c.MerchantVariants, FiendWarMerchantVariant{uint32(merchants[i]), uint32(merchants[i+1]), uint32(merchants[i+2])})
	}
	positions := doc.Ints("MERCHANT CREATE POS")
	seenPositions := map[uint32]bool{}
	if len(positions) == 0 || len(positions)%3 != 0 {
		return c, fmt.Errorf("Fiend War merchant positions unavailable")
	}
	for i := 0; i < len(positions); i += 3 {
		id := positions[i]
		if id <= 0 || id > 31 || areaIDs[uint32(id)].DungeonID <= 0 || seenPositions[uint32(id)] || positions[i+1] < 0 || positions[i+2] < 0 {
			return c, fmt.Errorf("Fiend War merchant position invalid")
		}
		seenPositions[uint32(id)] = true
		c.MerchantPositions = append(c.MerchantPositions, FiendWarMerchantPosition{uint32(id), positions[i+1], positions[i+2]})
	}
	for _, d := range c.Battle.PhaseDungeons[0] {
		if !dungeons[int64(d.ID)] {
			return c, fmt.Errorf("Fiend War boss has no battlefield area")
		}
	}
	return c, nil
}
