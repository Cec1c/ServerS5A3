package raid

import (
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"strings"
)

// LegacyAntonSingle describes values present in the native 90 PVF. WeeklyEnter
// remains a raw pair because the two values' individual ownership is unproven.
type LegacyAntonSingle struct {
	WeeklyEnter []int64
	Timeout     uint32
	Routes      map[int64][]uint32
	Rewards     AntonPhaseRewards
	Symbols     map[uint32]uint32
}

// ReadLegacyAntonSingle parses actual trigger edges and reward rows. It does not
// select a route or interpret the two weekly-enter values as clear limits.
func ReadLegacyAntonSingle(source dnfpvf.Source) (LegacyAntonSingle, error) {
	var out LegacyAntonSingle
	text, err := source.ReadText("etc/raid/anton.etc")
	if err != nil {
		return out, err
	}
	doc, err := dnfpvf.Parse("etc/raid/anton.etc", text)
	if err != nil {
		return out, err
	}
	out.WeeklyEnter = doc.Ints("WEEKLY ENTER COUNT")
	t, _ := doc.Int("SINGLE RAID TIME OVER")
	if t <= 0 || t > 3600 || len(out.WeeklyEnter) != 2 {
		return out, fmt.Errorf("legacy single raid global configuration invalid")
	}
	out.Timeout = uint32(t)
	out.Routes = map[int64][]uint32{}
	out.Symbols = map[uint32]uint32{}
	syms, err := LoadAntonSymbols(source)
	if err != nil {
		return out, err
	}
	phase, done, ranks, rewards, inDungeon, inTrigger, inBehavior, inIf := false, false, false, false, false, false, false, false
	var current uint32
	var first uint32
	var mode int64 = -1
	var from, to uint32
	var checkedClear, terminal bool
	terminals := map[uint32]bool{}
	type edge struct {
		mode     int64
		from, to uint32
	}
	var edges []edge
	for _, sec := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(sec.Name))
		v := sectionValues(doc, sec)
		if name == "SINGLE RAID PHASE" {
			if phase || done {
				return out, fmt.Errorf("repeated legacy single phase")
			}
			phase = true
			continue
		}
		if !phase {
			continue
		}
		if name == "/SINGLE RAID PHASE" {
			done = true
			break
		}
		switch name {
		case "CONDITION FOR RANK":
			ranks = true
		case "/CONDITION FOR RANK":
			ranks = false
		case "CLEAR REWARD ITEM":
			rewards = true
		case "/CLEAR REWARD ITEM":
			rewards = false
		case "DEAD COUNT":
			if ranks {
				if len(v) != 3 {
					return out, fmt.Errorf("legacy rank shape")
				}
				a, e1 := antonUint(v[0])
				b, e2 := antonUint(v[1])
				c, e3 := antonUint(v[2])
				if e1 != nil || e2 != nil || e3 != nil || a > b || c > 3 {
					return out, fmt.Errorf("legacy rank invalid")
				}
				out.Rewards.Ranks = append(out.Rewards.Ranks, AntonRankRange{Minimum: a, Maximum: b, Rank: byte(c)})
			}
		case "STATE REWARD":
			if rewards {
				if len(v) != 5 || v[0].Kind != dnfpvf.TokenString || v[1].Kind != dnfpvf.TokenInt {
					return out, fmt.Errorf("legacy reward shape")
				}
				w, e1 := antonUint(v[2])
				id, e2 := antonUint(v[3])
				f, e3 := antonUint(v[4])
				if e1 != nil || e2 != nil || e3 != nil || w == 0 || id == 0 || f > 1 || v[1].Int < -1 || v[1].Int > 3 {
					return out, fmt.Errorf("legacy reward invalid")
				}
				out.Rewards.Choices = append(out.Rewards.Choices, AntonRewardChoice{Type: strings.ToLower(v[0].Value), Rank: int(v[1].Int), Weight: w, ContainerID: id, Flags: byte(f)})
			}
		case "DUNGEON INFO":
			if inDungeon {
				return out, fmt.Errorf("nested legacy dungeon")
			}
			inDungeon = true
			current = 0
		case "/DUNGEON INFO":
			inDungeon = false
		case "INDEX":
			if inDungeon {
				if len(v) != 1 {
					return out, fmt.Errorf("legacy index shape")
				}
				id, e := antonUint(v[0])
				if e != nil || id == 0 {
					return out, fmt.Errorf("legacy index invalid")
				}
				current = id
			}
		case "INIT STATE":
			if inDungeon && len(v) == 1 && strings.EqualFold(v[0].Value, "open") {
				if first != 0 || current == 0 {
					return out, fmt.Errorf("legacy first dungeon ambiguous")
				}
				first = current
			}
		case "TRIGGER":
			inTrigger = true
			inIf = false
			mode = -1
			from = 0
			to = 0
			checkedClear, terminal = false, false
		case "IF":
			if inTrigger {
				inIf = true
			}
		case "SINGLE RAID TYPE":
			if inTrigger && inIf && len(v) == 1 && v[0].Kind == dnfpvf.TokenInt {
				mode = v[0].Int
			}
		case "ON CHANGE DUNGEON STATE":
			if inTrigger && len(v) == 1 {
				id, e := antonUint(v[0])
				if e != nil {
					return out, e
				}
				from = id
			}
		case "CHECK DUNGEON STATE":
			if inTrigger && from != 0 && len(v) == 2 && strings.EqualFold(v[1].Value, "clear") {
				id, e := antonUint(v[0])
				checkedClear = e == nil && id == from
			}
		case "/TRIGGER":
			inTrigger = false
			inBehavior = true
		case "SET DUNGEON STATE":
			if inBehavior && from != 0 && len(v) == 2 && strings.EqualFold(v[1].Value, "open") {
				id, e := antonUint(v[0])
				if e != nil || to != 0 {
					return out, fmt.Errorf("legacy trigger open ambiguous")
				}
				to = id
			}
		case "CLEAR PHASE":
			if inBehavior && from != 0 {
				terminal = true
			}
		case "/BEHAVIOR":
			if inBehavior && from != 0 && (to != 0 || terminal) && !checkedClear {
				return out, fmt.Errorf("legacy trigger lacks matching clear condition")
			}
			if inBehavior && from != 0 && to != 0 {
				edges = append(edges, edge{mode, from, to})
			}
			if inBehavior && terminal {
				terminals[from] = true
			}
			inBehavior = false
		default:
			if inDungeon && current != 0 && name == fmt.Sprintf("%d DGN HP", current) {
				if len(v) != 1 || v[0].Int != 1 || syms[name] == 0 || out.Symbols[current] != 0 {
					return out, fmt.Errorf("legacy single HP invalid")
				}
				out.Symbols[current] = syms[name]
			}
		}
	}
	if !done || first == 0 || len(out.Symbols) == 0 || len(out.Rewards.Ranks) == 0 || len(out.Rewards.Choices) == 0 {
		return out, fmt.Errorf("legacy single data incomplete")
	}
	modes := map[int64]bool{}
	for _, e := range edges {
		if e.mode >= 0 {
			modes[e.mode] = true
		}
	}
	for m := range modes {
		route := []uint32{first}
		seen := map[uint32]bool{first: true}
		for {
			cur := route[len(route)-1]
			var next uint32
			for _, e := range edges {
				if e.from == cur && (e.mode < 0 || e.mode == m) {
					if next != 0 && next != e.to {
						return out, fmt.Errorf("legacy route ambiguous")
					}
					next = e.to
				}
			}
			if next == 0 {
				break
			}
			if seen[next] || out.Symbols[next] == 0 {
				return out, fmt.Errorf("legacy route cyclic or missing HP")
			}
			seen[next] = true
			route = append(route, next)
		}
		if !terminals[route[len(route)-1]] {
			return out, fmt.Errorf("legacy route has no clear-phase terminal")
		}
		out.Routes[m] = route
	}
	if len(out.Routes) == 0 {
		return out, fmt.Errorf("legacy routes unavailable")
	}
	return out, nil
}

// LegacyAntonWorldMapOptions validates every branch and returns only actual
// supplemental identities absent from the primary dungeon index.
func LegacyAntonWorldMapOptions(source dnfpvf.Source) (worldmap.Options, error) {
	legacy, err := ReadLegacyAntonSingle(source)
	if err != nil {
		return worldmap.Options{}, err
	}
	ids := make([]uint32, 0, len(legacy.Symbols))
	for id := range legacy.Symbols {
		ids = append(ids, id)
	}
	if err := ValidateDungeonDependencies(source, ids); err != nil {
		return worldmap.Options{}, err
	}
	primary, err := actualRaidIndex(source, "dungeon/dungeon.lst")
	if err != nil {
		return worldmap.Options{}, err
	}
	o := worldmap.Options{SupplementalDungeonListPath: "dungeon/tutorial/dungeon.lst", SupplementalDungeonReferenceRoot: "dungeon"}
	for _, id := range ids {
		if primary[int64(id)] == "" {
			o.SupplementalDungeonIDs = append(o.SupplementalDungeonIDs, int64(id))
		}
	}
	return o, nil
}
