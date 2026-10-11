package raid

import (
	"fmt"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type lukeGeneratorTier struct{ Value, Minimum, Maximum uint32 }
type lukeGeneratorConfiguration struct {
	Caps    map[uint32]uint32
	Objects map[int64]uint32
	Tiers   []lukeGeneratorTier
}

// Compile the actual sanctum budgets and object action contracts. No client
// operand, object name, arbitrary symbol or default numeric table grants credit.
func loadLukeGeneratorConfiguration(source dnfpvf.Source, doc *dnfpvf.Document, names map[uint32]string, symbols map[string]uint32) (*lukeGeneratorConfiguration, error) {
	c := &lukeGeneratorConfiguration{Caps: map[uint32]uint32{}, Objects: map[int64]uint32{}}
	inside := false
	for _, sec := range doc.Sections {
		name := strings.ToUpper(strings.TrimSpace(sec.Name))
		v := sectionValues(doc, sec)
		if name == "DEFAULT OBJECT COUNT" {
			if inside {
				return nil, fmt.Errorf("duplicate Luke object budget")
			}
			inside = true
			continue
		}
		if name == "/DEFAULT OBJECT COUNT" {
			inside = false
			continue
		}
		if inside {
			if len(v) != 2 {
				return nil, fmt.Errorf("invalid Luke object budget row")
			}
			count, e := antonUint(v[0])
			if e != nil || count == 0 || count > 255 {
				return nil, fmt.Errorf("invalid Luke object count")
			}
			id, e := antonUint(v[1])
			if e != nil || names[id] != name+" OBJECT" || c.Caps[id] != 0 {
				return nil, fmt.Errorf("Luke object budget owner mismatch")
			}
			c.Caps[id] = count
		}
		if name == "1PHASE OBJ DESTROY" {
			if len(c.Tiers) != 0 || len(v) == 0 || len(v)%3 != 0 {
				return nil, fmt.Errorf("invalid Luke generator tiers")
			}
			for i := 0; i < len(v); i += 3 {
				a, e := antonUint(v[i])
				if e != nil {
					return nil, e
				}
				lo, e := antonUint(v[i+1])
				if e != nil {
					return nil, e
				}
				hi, e := antonUint(v[i+2])
				if e != nil || lo > hi || len(c.Tiers) == 0 && lo != 0 || len(c.Tiers) > 0 && lo != c.Tiers[len(c.Tiers)-1].Maximum+1 {
					return nil, fmt.Errorf("Luke generator tier range gap")
				}
				c.Tiers = append(c.Tiers, lukeGeneratorTier{a, lo, hi})
			}
		}
	}
	if inside || len(c.Caps) != len(names) || len(c.Tiers) == 0 {
		return nil, fmt.Errorf("incomplete Luke generator configuration")
	}
	var total uint32
	for _, cap := range c.Caps {
		total += cap
	}
	if c.Tiers[len(c.Tiers)-1].Maximum != total {
		return nil, fmt.Errorf("Luke generator tier coverage mismatch")
	}
	// Discover the exact objects from the indexed names and their own destruction
	// actions, not all passive objects in a sanctum or the visual rear machinery.
	text, e := source.ReadText("passiveobject/passiveobject.lst")
	if e != nil {
		return nil, e
	}
	for _, m := range regexp.MustCompile("([0-9]+)\\s+`([^`]+)`").FindAllStringSubmatch(text, -1) {
		n := strings.ToLower(m[2])
		if !strings.HasPrefix(n, "actionobject/map/luke_raid/phase1/") || !strings.HasSuffix(n, "_raid.obj") {
			continue
		}
		id, e := strconv.ParseInt(m[1], 10, 64)
		if e != nil {
			return nil, e
		}
		obj := path.Join("passiveobject", m[2])
		t, e := source.ReadText(obj)
		if e != nil {
			return nil, e
		}
		od, e := dnfpvf.Parse(obj, t)
		if e != nil {
			return nil, e
		}
		for _, os := range od.Sections {
			if !strings.EqualFold(os.Name, "etc action") {
				continue
			}
			for _, token := range sectionValues(od, os) {
				action := strings.ToLower(path.Base(token.Value))
				if action != "luke_mainmchine_destroy_raid.act" && action != "luke_mainmchine_destroy_mistral_raid.act" && action != "mainmchine_destroy_raid.act" && action != "mainmchine_destroy.act" && action != "censor_machine_broken_front_raid.act" {
					continue
				}
				act := path.Join(path.Dir(obj), token.Value)
				at, e := source.ReadText(act)
				if e != nil {
					return nil, e
				}
				ad, e := dnfpvf.Parse(act, at)
				if e != nil {
					return nil, e
				}
				for i, as := range ad.Sections {
					if !strings.EqualFold(as.Name, "RAID SET SYMBOL") {
						continue
					}
					v := sectionValues(ad, as)
					if len(v) != 1 || i+1 >= len(ad.Sections) || ad.Sections[i+1].Name != "+" {
						return nil, fmt.Errorf("Luke machine action shape mismatch")
					}
					sym, e := antonUint(v[0])
					if e != nil {
						return nil, e
					}
					operand := sectionValues(ad, ad.Sections[i+1])
					if len(operand) != 1 || operand[0].Kind != dnfpvf.TokenInt || operand[0].Int != 1 {
						return nil, fmt.Errorf("Luke machine action operand mismatch")
					}
					for d, name := range names {
						if symbols[name] == sym {
							if c.Objects[id] != 0 && c.Objects[id] != d {
								return nil, fmt.Errorf("ambiguous Luke object")
							}
							c.Objects[id] = d
						}
					}
				}
			}
		}
	}
	for id := range c.Caps {
		found := false
		for _, d := range c.Objects {
			found = found || d == id
		}
		if !found {
			return nil, fmt.Errorf("Luke generator object source missing for %d", id)
		}
	}
	return c, nil
}

func (s *LukeProgression) SanctumGeneratorOwner(id uint32, objectID int64) bool {
	return s != nil && s.program != nil && s.program.lukeGenerators != nil && s.program.lukeGenerators.Objects[objectID] == id
}
func (s *LukeProgression) CreditSanctumGenerator(run, event string, id uint32, epoch uint64, now time.Time) (*LukeProgression, bool, error) {
	if s == nil || !s.IsLuke() || s.phase != 0 || s.phaseCleared || s.phaseFailed || epoch != s.epoch || run == "" || event == "" || len(event) > 512 || now.Before(s.last) || s.enteredRuns[run] != id || s.dungeons[id] != 0 || s.program.lukeGenerators == nil {
		return nil, false, fmt.Errorf("Luke generator has no active owned sanctum run")
	}
	name := s.program.sanctumSymbols[id]
	cap := s.program.lukeGenerators.Caps[id]
	if name == "" || cap == 0 {
		return nil, false, fmt.Errorf("Luke sanctum generator budget missing")
	}
	key := "luke-generator/" + event
	if s.barrierEvents[key] || s.symbols[name] >= cap {
		return s.Clone(), true, nil
	}
	if len(s.barrierEvents) >= 4096 {
		return nil, false, fmt.Errorf("Luke generator journal limit")
	}
	n, e := s.Tick(now)
	if e != nil {
		return nil, false, e
	}
	if n.PhaseCleared() || n.PhaseFailed() || n.Epoch() != epoch || n.enteredRuns[run] != id || n.dungeons[id] != 0 {
		return nil, false, fmt.Errorf("Luke generator run expired during clock advance")
	}
	events := []antonEvent{}
	n.setSymbol(name, n.symbols[name]+1, &events)
	if e = n.evaluate(now, events); e != nil {
		return nil, false, e
	}
	n.barrierEvents[key] = true
	return n, false, nil
}

func carryLukeGeneratorTier(old, next *AntonProgression) error {
	if old == nil || next == nil || old.program.lukeGenerators == nil {
		return fmt.Errorf("Luke generator tier context unavailable")
	}
	var total uint32
	for id, cap := range old.program.lukeGenerators.Caps {
		name := old.program.sanctumSymbols[id]
		value := old.symbols[name]
		if value > cap {
			return fmt.Errorf("Luke generator count exceeds actual budget")
		}
		total += value
		next.assignSymbolValue(name, value)
	}
	for _, tier := range old.program.lukeGenerators.Tiers {
		if total >= tier.Minimum && total <= tier.Maximum {
			next.assignSymbolValue("ENERGY CONTROL OPEN", tier.Value)
			return nil
		}
	}
	return fmt.Errorf("Luke generator tier unavailable")
}

func (s *LukeProgression) SanctumGeneratorSymbol(id uint32) uint32 {
	if s == nil || s.program == nil {
		return 0
	}
	return s.program.symbols[s.program.sanctumSymbols[id]]
}
