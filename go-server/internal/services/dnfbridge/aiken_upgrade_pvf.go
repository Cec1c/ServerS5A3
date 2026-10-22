package dnfbridge

import (
	"fmt"
	aiken "longheng.io/server/internal/modules/dnf/aikenupgrade"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	pvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strings"
	"time"
)

func aikenDoc(source pvf.Source, path string) (*pvf.Document, error) {
	text, e := source.ReadText(path)
	if e != nil {
		return nil, e
	}
	return pvf.Parse(path, text)
}
func aikenGrade(s string) (int, bool) {
	switch s {
	case "D":
		return 0, true
	case "C":
		return 1, true
	case "B":
		return 2, true
	case "A":
		return 3, true
	}
	return 0, false
}
func aikenSection(d *pvf.Document, name string) ([]pvf.Token, error) {
	var out []pvf.Token
	n := 0
	for _, s := range d.Sections {
		if s.Name == name {
			n++
			out = d.Tokens[s.Start:s.End]
		}
	}
	if n != 1 {
		return nil, aiken.ErrInvalid
	}
	return out, nil
}
func aikenString(d *pvf.Document, name, value string) bool {
	v, e := aikenSection(d, name)
	return e == nil && len(v) == 1 && v[0].Kind == pvf.TokenString && v[0].Value == value
}
func aikenInt(d *pvf.Document, name string, want int64) bool {
	v, e := aikenSection(d, name)
	return e == nil && len(v) == 1 && v[0].Kind == pvf.TokenInt && v[0].Int == want
}

// This supports the observed current-resource model only. No ordinary recipe
// need-material exemption, success-rate buff, gold fee or initial-grade roll
// is imported into the dedicated target-grade upgrade rows.
func currentAikenRules(c *pvfDungeonDropCatalog) (aiken.Rules, error) {
	var r aiken.Rules
	if c == nil || c.source == nil {
		return r, aiken.ErrInvalid
	}
	d, e := aikenDoc(c.source, "etc/customability/individualcustominfo.etc")
	if e != nil {
		return r, e
	}
	allowed := map[string]bool{}
	for _, n := range []string{"custom ability type", "group", "/group", "key", "indexes", "/indexes", "disjoint", "/disjoint", "grade", "item", "/item", "group name", "upgrade max", "info", "/info", "index", "material", "/material", "list", "/list", "upgrade info", "/upgrade info", "assign material", "/assign material", "ui info", "/ui info", "ability info", "add item name", "grade probability", "/grade probability", "disjoint disable item", "/disjoint disable item"} {
		allowed[n] = true
	}
	for _, s := range d.Sections {
		if !allowed[s.Name] {
			return r, fmt.Errorf("%w: unsupported individual section %s", aiken.ErrInvalid, s.Name)
		}
	}
	if !aikenString(d, "custom ability type", "warriors dungeon") || !aikenInt(d, "upgrade max", 4) {
		return r, aiken.ErrInvalid
	}
	seen := map[int]bool{}
	for i, s := range d.Sections {
		if s.Name != "upgrade info" {
			continue
		}
		if i+4 >= len(d.Sections) || s.Start != s.End {
			return r, aiken.ErrInvalid
		}
		want := []string{"grade", "assign material", "/assign material", "/upgrade info"}
		for j, n := range want {
			if d.Sections[i+j+1].Name != n {
				return r, aiken.ErrInvalid
			}
		}
		gs, ms := d.Sections[i+1], d.Sections[i+2]
		g, m := d.Tokens[gs.Start:gs.End], d.Tokens[ms.Start:ms.End]
		if len(g) != 1 || g[0].Kind != pvf.TokenString || len(m) != 2 || m[0].Kind != pvf.TokenInt || m[1].Kind != pvf.TokenInt {
			return r, aiken.ErrInvalid
		}
		grade, ok := aikenGrade(g[0].Value)
		if !ok || seen[grade] || m[0].Int != 10161536 || m[1].Int <= 0 || m[1].Int > math.MaxUint32 {
			return r, aiken.ErrInvalid
		}
		if d.Sections[i+3].Start != d.Sections[i+3].End || d.Sections[i+4].Start != d.Sections[i+4].End {
			return r, aiken.ErrInvalid
		}
		seen[grade] = true
		r.Costs[grade] = m[1].Int
		r.Material = m[0].Int
	}
	if len(seen) != 4 {
		return r, aiken.ErrInvalid
	}
	// Fail closed on changed upgrade prices until the new resource contract is reviewed.
	if r.Costs != [4]int64{11, 11, 18, 34} {
		return r, aiken.ErrInvalid
	}
	options := map[byte]bool{}
	for _, id := range []byte{1, 2, 3, 4, 5, 6, 7, 11, 12, 13, 14, 21, 22, 23, 24, 25} {
		options[id] = true
	}
	// The option list and all three resource group indexes must agree.
	groupOptions := map[byte]bool{}
	groups := 0
	for _, sec := range d.Sections {
		if sec.Name != "indexes" {
			continue
		}
		groups++
		for _, tok := range d.Tokens[sec.Start:sec.End] {
			if tok.Kind != pvf.TokenInt || tok.Int < 1 || tok.Int > 255 || !options[byte(tok.Int)] || groupOptions[byte(tok.Int)] {
				return r, aiken.ErrInvalid
			}
			groupOptions[byte(tok.Int)] = true
		}
	}
	if groups != 3 || len(groupOptions) != len(options) {
		return r, aiken.ErrInvalid
	}
	r.CostPolicy = func(itemID int64) (itemplacement.SlotPolicy, error) {
		if itemID <= 0 || itemID > math.MaxUint32 {
			return itemplacement.SlotPolicy{}, aiken.ErrInvalid
		}
		item, e := c.ResolveItem(uint32(itemID))
		if e != nil || item.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(item.StackableType) != "[material]" {
			return itemplacement.SlotPolicy{}, aiken.ErrInvalid
		}
		return currentPVFItemPlacementPolicy(item), nil
	}
	list, e := aikenDoc(c.source, "etc/customability/individualcustomability.lst")
	if e != nil {
		return r, e
	}
	entries := pvf.ParseList(list)
	if len(entries) != 16 {
		return r, aiken.ErrInvalid
	}
	found := map[byte]bool{}
	for _, ent := range entries {
		if ent.ID < 1 || ent.ID > 255 || !options[byte(ent.ID)] || found[byte(ent.ID)] || strings.Contains(ent.Path, "..") {
			return r, aiken.ErrInvalid
		}
		found[byte(ent.ID)] = true
		od, e := aikenDoc(c.source, "etc/customability/"+ent.Path)
		if e != nil {
			return r, e
		}
		grades := map[int]bool{}
		inside := false
		gradeInBlock := false
		attributes := 0
		for _, s := range od.Sections {
			switch s.Name {
			case "parameter":
				if inside || s.Start != s.End {
					return r, aiken.ErrInvalid
				}
				gradeInBlock = false
				attributes = 0
				inside = true
			case "custom grade":
				v := od.Tokens[s.Start:s.End]
				if !inside || gradeInBlock || len(v) != 1 || v[0].Kind != pvf.TokenString {
					return r, aiken.ErrInvalid
				}
				g, ok := aikenGrade(v[0].Value)
				if !ok || grades[g] {
					return r, aiken.ErrInvalid
				}
				grades[g] = true
				gradeInBlock = true
			case "/parameter":
				if !inside || !gradeInBlock || attributes == 0 || s.Start != s.End {
					return r, aiken.ErrInvalid
				}
				inside = false
			case "physical attack", "magical attack", "physical defense", "magical defense", "move speed", "attack speed", "cast speed", "equipment physical attack", "equipment magical attack", "physical critical hit", "magical critical hit", "fire attack", "water attack", "light attack", "dark attack", "all elemental attack", "separate attack":
				if !inside || !gradeInBlock {
					return r, aiken.ErrInvalid
				}
				vals := od.Tokens[s.Start:s.End]
				want := 1
				if s.Name == "equipment physical attack" || s.Name == "equipment magical attack" || s.Name == "separate attack" {
					want = 2
				}
				if len(vals) != want {
					return r, aiken.ErrInvalid
				}
				for _, v := range vals {
					if v.Kind != pvf.TokenInt || v.Int < 0 {
						return r, aiken.ErrInvalid
					}
				}
				attributes++
			default:
				return r, fmt.Errorf("%w: unsupported option section %s", aiken.ErrInvalid, s.Name)
			}
		}
		if inside || len(grades) != 4 {
			return r, aiken.ErrInvalid
		}
	}
	material, e := c.ResolveItem(uint32(r.Material))
	if e != nil || material.Kind != dungeonDropItemStackable || normalizeDungeonDropStackableType(material.StackableType) != "[material]" {
		return r, aiken.ErrInvalid
	}
	r.MaterialPolicy = currentPVFItemPlacementPolicy(material)
	r.SourcePolicy = func(s repo.ItemStack) (itemplacement.SlotPolicy, error) {
		d, e := c.ResolveItem(uint32(s.ItemID))
		return currentPVFItemPlacementPolicy(d), e
	}
	r.Expired = func(s repo.ItemStack, now time.Time) bool { return currentDungeonDiscardStackExpired(s, now) }
	r.SetCount = currentAikenSetCount
	r.Options = options
	r.State = currentAikenState
	r.SetState = currentAikenSetState
	r.Validate = func(s repo.ItemStack) error {
		if s.ItemID <= 0 || s.ItemID > math.MaxUint32 {
			return aiken.ErrInvalid
		}
		item, e := c.ResolveItem(uint32(s.ItemID))
		if e != nil || item.Kind != dungeonDropItemEquipment {
			return aiken.ErrInvalid
		}
		d, e := aikenDoc(c.source, item.PVFPath)
		if e != nil {
			return e
		}
		if !aikenString(d, "custom ability type", "warriors dungeon") || !aikenInt(d, "custom ability count", 2) {
			return aiken.ErrInvalid
		}
		_, e = currentAikenState(s)
		return e
	}
	return r, nil
}
