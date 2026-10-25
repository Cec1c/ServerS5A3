package guild

import (
	"errors"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const RankingRulesPath = "etc/guild/guildranking.etc"

var ErrRankingRules = errors.New("invalid guild ranking rules")

// RankingFactors follows the native 108-byte model, including its hidden event
// component. Scores are points, not raw member counts or purification units.
type RankingFactors struct {
	Members, WeeklyActive, Dungeon, War, PVP, Contents float32
	Adventure, PartyClear, Event, RaidClear            float32
}

func (f RankingFactors) Values() [10]float32 {
	return [10]float32{f.Members, f.WeeklyActive, f.Dungeon, f.War, f.PVP, f.Contents, f.Adventure, f.PartyClear, f.Event, f.RaidClear}
}

func (f RankingFactors) Total() (float32, error) {
	var total float64
	for _, value := range f.Values() {
		if value < 0 || math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return 0, ErrRankingRules
		}
		total += float64(value)
	}
	if total > math.MaxInt32 || float64(float32(total)) > math.MaxInt32 {
		return 0, ErrRankingRules
	}
	return float32(total), nil
}

type RankingRate struct{ Maximum, Rate float64 }
type RankingRange struct {
	Min, Max             int
	Points, NewbiePoints float64
}
type RankingPoints struct{ Points, NewbiePoints float64 }
type RankingConquest struct {
	First, Second uint32
	Points        float64
}

type RankingRules struct {
	Members, WeeklyActive, Dungeon, Contents RankingRate
	MinimumConquestPoint                     float64
	Conquests                                []RankingConquest
	WarAttack, WarDefence, WarBattle         float64
	Adventure, PVP                           []RankingRange
	PartyClears                              map[uint32]RankingPoints
	RaidClears                               map[string]RankingPoints
	EventID                                  uint32
	EventPoint                               float64
	NewbieAfter                              time.Time
}

func LoadRankingRules(source pvf.Source) (*RankingRules, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(RankingRulesPath)
	if err != nil {
		return nil, err
	}
	return ParseRankingRules(text)
}

func ParseRankingRules(text string) (*RankingRules, error) {
	doc, err := pvf.Parse(RankingRulesPath, text)
	if err != nil {
		return nil, err
	}
	r := &RankingRules{PartyClears: map[uint32]RankingPoints{}, RaidClears: map[string]RankingPoints{}}
	var root, group string
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		name := strings.ToLower(strings.TrimSpace(section.Name))
		values := doc.Tokens[section.Start:section.End]
		switch name {
		case "guild factor", "guild member factor", "guild member ranking factor", "guild mark add effect", "reward mail", "newbie member requirement":
			if root != "" || len(values) != 0 || seen[name] {
				return nil, ErrRankingRules
			}
			seen[name] = true
			root, group = name, ""
			continue
		case "/guild factor", "/guild member factor", "/guild member ranking factor", "/guild mark add effect", "/reward mail", "/newbie member requirement":
			if root != strings.TrimPrefix(name, "/") || group != "" || len(values) != 0 {
				return nil, ErrRankingRules
			}
			root, group = "", ""
			continue
		}
		if root != "guild factor" && root != "guild member factor" && root != "newbie member requirement" {
			continue
		}
		if strings.HasPrefix(name, "/") {
			// PVF sections are flat. A leaf's explicit closing tag does not
			// close its enclosing factor (notably dungeon conquest/point).
			if name == "/"+group {
				group = ""
			} else if !seen[root+"/"+group+"/"+strings.TrimPrefix(name, "/")] {
				return nil, ErrRankingRules
			}
			continue
		}
		if len(values) == 0 {
			switch name {
			case "member count", "weekly uv", "guild dungeon", "agitwar", "guild contents", "raid clear", "event":
				key := root + "/" + name
				if seen[key] || group != "" {
					return nil, ErrRankingRules
				}
				seen[key] = true
				group = name
			default:
				return nil, ErrRankingRules
			}
			continue
		}
		key := root + "/" + group + "/" + name
		if seen[key] {
			return nil, ErrRankingRules
		}
		seen[key] = true
		one := func() (float64, error) {
			if len(values) != 1 {
				return 0, ErrRankingRules
			}
			return rankingNumber(values[0])
		}
		var rate *RankingRate
		if root == "guild factor" {
			switch group {
			case "member count":
				rate = &r.Members
			case "weekly uv":
				rate = &r.WeeklyActive
			case "guild dungeon":
				rate = &r.Dungeon
			case "guild contents":
				rate = &r.Contents
			}
		}
		if rate != nil && (name == "max point" || name == "rate" || name == "purify point rate") {
			v, e := one()
			if e != nil {
				return nil, e
			}
			if name == "max point" {
				rate.Maximum = v
			} else {
				rate.Rate = v
			}
			continue
		}
		switch {
		case root == "guild factor" && group == "guild dungeon" && name == "min conquest point":
			r.MinimumConquestPoint, err = one()
		case root == "guild factor" && group == "guild dungeon" && name == "dungeon conquest":
			if len(values)%3 != 0 {
				return nil, ErrRankingRules
			}
			for i := 0; i < len(values); i += 3 {
				a, e := rankingID(values[i])
				if e != nil {
					return nil, e
				}
				b, e := rankingID(values[i+1])
				if e != nil {
					return nil, e
				}
				p, e := rankingNumber(values[i+2])
				if e != nil {
					return nil, e
				}
				r.Conquests = append(r.Conquests, RankingConquest{a, b, p})
			}
		case root == "guild factor" && group == "agitwar":
			v, e := one()
			if e != nil {
				return nil, e
			}
			switch name {
			case "battle point rate":
				r.WarBattle = v
			case "defence point rate":
				r.WarDefence = v
			case "attack point rate":
				r.WarAttack = v
			default:
				return nil, ErrRankingRules
			}
		case root == "guild factor" && name == "guild party clear":
			if len(values)%3 != 0 {
				return nil, ErrRankingRules
			}
			for i := 0; i < len(values); i += 3 {
				id, e := rankingID(values[i])
				if e != nil {
					return nil, e
				}
				p, e := rankingPair(values[i+1 : i+3])
				if e != nil {
					return nil, e
				}
				if _, ok := r.PartyClears[id]; ok {
					return nil, ErrRankingRules
				}
				r.PartyClears[id] = p
			}
		case root == "guild member factor" && (name == "adventure level" || name == "pvp"):
			rows, e := rankingRanges(values)
			if e != nil {
				return nil, e
			}
			if name == "adventure level" {
				r.Adventure = rows
			} else {
				r.PVP = rows
			}
		case root == "guild member factor" && group == "raid clear" && name == "point":
			if len(values)%3 != 0 {
				return nil, ErrRankingRules
			}
			for i := 0; i < len(values); i += 3 {
				if values[i].Kind != pvf.TokenString {
					return nil, ErrRankingRules
				}
				key := strings.ToLower(values[i].Value)
				if key != "anton" && key != "luke" && key != "fiendwar" {
					return nil, ErrRankingRules
				}
				if _, ok := r.RaidClears[key]; ok {
					return nil, ErrRankingRules
				}
				p, e := rankingPair(values[i+1 : i+3])
				if e != nil {
					return nil, e
				}
				r.RaidClears[key] = p
			}
		case root == "guild member factor" && group == "event" && name == "event id":
			if len(values) != 1 || values[0].Kind != pvf.TokenInt || values[0].Int < 0 || values[0].Int > math.MaxInt32 {
				return nil, ErrRankingRules
			}
			r.EventID = uint32(values[0].Int)
		case root == "guild member factor" && group == "event" && name == "point":
			r.EventPoint, err = one()
		case root == "newbie member requirement" && name == "join date":
			if len(values) != 1 || values[0].Kind != pvf.TokenString {
				return nil, ErrRankingRules
			}
			r.NewbieAfter, err = time.ParseInLocation("2006-01-02 15:04:05", values[0].Value, time.FixedZone("DNF UTC+8", 8*3600))
		default:
			return nil, ErrRankingRules
		}
		if err != nil {
			return nil, err
		}
	}
	if root != "" || group != "" {
		return nil, ErrRankingRules
	}
	for _, rate := range []RankingRate{r.Members, r.WeeklyActive, r.Dungeon, r.Contents} {
		if rate.Maximum <= 0 || rate.Rate <= 0 {
			return nil, ErrRankingRules
		}
	}
	for _, key := range []string{
		"guild factor/agitwar/battle point rate", "guild factor/agitwar/defence point rate", "guild factor/agitwar/attack point rate",
		"guild factor/guild dungeon/min conquest point", "guild member factor/event/event id", "guild member factor/event/point",
	} {
		if !seen[key] {
			return nil, ErrRankingRules
		}
	}
	if len(r.Conquests) == 0 || len(r.Adventure) == 0 || len(r.PVP) == 0 || len(r.PartyClears) == 0 || len(r.RaidClears) != 3 || r.NewbieAfter.IsZero() {
		return nil, ErrRankingRules
	}
	return r, nil
}

func rankingNumber(t pvf.Token) (float64, error) {
	var v float64
	switch t.Kind {
	case pvf.TokenInt:
		v = float64(t.Int)
	case pvf.TokenFloat:
		v = t.Float
	default:
		return 0, ErrRankingRules
	}
	if v < 0 || v > math.MaxInt32 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, ErrRankingRules
	}
	return v, nil
}
func rankingID(t pvf.Token) (uint32, error) {
	if t.Kind != pvf.TokenInt || t.Int <= 0 || t.Int > math.MaxInt32 {
		return 0, ErrRankingRules
	}
	return uint32(t.Int), nil
}
func rankingPair(v []pvf.Token) (RankingPoints, error) {
	if len(v) != 2 {
		return RankingPoints{}, ErrRankingRules
	}
	a, e := rankingNumber(v[0])
	if e != nil {
		return RankingPoints{}, e
	}
	b, e := rankingNumber(v[1])
	return RankingPoints{a, b}, e
}
func rankingRanges(values []pvf.Token) ([]RankingRange, error) {
	if len(values) == 0 || len(values)%4 != 0 {
		return nil, ErrRankingRules
	}
	out := []RankingRange{}
	for i := 0; i < len(values); i += 4 {
		a, b := values[i], values[i+1]
		if a.Kind != pvf.TokenInt || b.Kind != pvf.TokenInt || a.Int < 0 || b.Int < a.Int || b.Int > math.MaxInt32 || len(out) > 0 && int(a.Int) <= out[len(out)-1].Max {
			return nil, ErrRankingRules
		}
		p, e := rankingPair(values[i+2 : i+4])
		if e != nil {
			return nil, e
		}
		out = append(out, RankingRange{int(a.Int), int(b.Int), p.Points, p.NewbiePoints})
	}
	return out, nil
}

func RankingLevelPoints(rows []RankingRange, level int, newbie bool) float64 {
	for _, row := range rows {
		if level >= row.Min && level <= row.Max {
			if newbie {
				return row.NewbiePoints
			}
			return row.Points
		}
	}
	return 0
}
