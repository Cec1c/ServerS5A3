package chronicle

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"longheng.io/server/internal/modules/dnf/pvf"
)

type Check struct {
	Job, Grow     byte
	EquipmentType string
	Skills        map[byte]int64
}
type Material struct {
	ItemID        int64
	AuraType      byte
	Probabilities [2]int
	FragmentID    int64
	Checks        []Check
}
type Equipment struct {
	ItemID          int64
	Path, Type      string
	TypeID          byte
	Rarity          int64
	Durability      uint16
	Jobs            []string
	CharacterChecks []Check
}
type Reference struct {
	Path      string
	Equipment bool
}
type Catalog struct {
	source    pvf.Source
	refs      map[int64]Reference
	mu        sync.Mutex
	materials map[int64]Material
	equipment map[int64]Equipment
	imports   map[string][]Check
}

func NewCatalog(source pvf.Source, refs map[int64]Reference) *Catalog {
	copied := make(map[int64]Reference, len(refs))
	for id, ref := range refs {
		copied[id] = ref
	}
	return &Catalog{source: source, refs: copied, materials: map[int64]Material{}, equipment: map[int64]Equipment{}, imports: map[string][]Check{}}
}
func (c *Catalog) document(id int64, equipment bool) (*pvf.Document, Reference, error) {
	ref, ok := c.refs[id]
	if !ok || ref.Equipment != equipment || c.source == nil {
		return nil, ref, ErrRules
	}
	text, err := c.source.ReadText(ref.Path)
	if err != nil {
		return nil, ref, err
	}
	doc, err := pvf.Parse(ref.Path, text)
	return doc, ref, err
}
func normalized(s string) string                                 { return strings.ToLower(strings.Trim(strings.TrimSpace(s), "`[]")) }
func sectionTokens(doc *pvf.Document, s pvf.Section) []pvf.Token { return doc.Tokens[s.Start:s.End] }

// 90的气息通过[import script]复用职业/技能表，概率保留本道具的值。
func (c *Catalog) Material(id int64) (Material, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.materials[id]; ok {
		return m, nil
	}
	doc, ref, err := c.document(id, false)
	if err != nil {
		return Material{}, err
	}
	aura, found := doc.Int("type")
	p := doc.Ints("probability")
	fragment := doc.Ints("need material")
	if !found || aura < 0 || aura > 2 || len(p) != 2 || p[0] < 0 || p[0] > 100 || p[1] < 0 || p[1] > 100 || len(fragment) < 1 || fragment[0] <= 0 {
		return Material{}, ErrRules
	}
	checks := parseChecks(doc)
	if imported, ok := doc.Text("import script"); ok {
		imported = strings.ReplaceAll(imported, "\\", "/")
		file := path.Clean(path.Join(path.Dir(ref.Path), imported))
		if !strings.HasPrefix(file, "stackable/") || strings.Contains(imported, "..") {
			return Material{}, ErrRules
		}
		cached, err := c.importChecks(file, path.Clean(path.Join("stackable", imported)))
		if err != nil {
			return Material{}, err
		}
		checks = append(checks, cached...)
	} else if _, ok := doc.Section("3choro enchant"); !ok {
		return Material{}, ErrRules
	}
	if len(checks) == 0 {
		return Material{}, ErrRules
	}
	m := Material{ItemID: id, AuraType: byte(aura), Probabilities: [2]int{int(p[0]), int(p[1])}, FragmentID: fragment[0], Checks: checks}
	c.materials[id] = m
	return m, nil
}

func (c *Catalog) importChecks(files ...string) ([]Check, error) {
	for _, file := range files {
		if cached, ok := c.imports[file]; ok {
			return cached, nil
		}
		text, err := c.source.ReadText(file)
		if err != nil {
			continue
		}
		rules, err := pvf.Parse(file, text)
		if err != nil {
			return nil, err
		}
		if _, found := rules.Section("3choro enchant"); !found {
			return nil, ErrRules
		}
		checks := parseChecks(rules)
		if len(checks) == 0 {
			return nil, ErrRules
		}
		c.imports[file] = checks
		return checks, nil
	}
	return nil, ErrRules
}
func parseChecks(doc *pvf.Document) []Check {
	var out []Check
	active := -1
	inside := false
	for _, s := range doc.Sections {
		name := normalized(s.Name)
		tokens := sectionTokens(doc, s)
		switch name {
		case "check":
			active = -1
			inside = false
			if len(tokens) < 3 || tokens[0].Kind != pvf.TokenInt || tokens[1].Kind != pvf.TokenInt || tokens[0].Int < 0 || tokens[0].Int > 255 || tokens[1].Int < 0 || tokens[1].Int > 15 {
				continue
			}
			out = append(out, Check{Job: byte(tokens[0].Int), Grow: byte(tokens[1].Int), EquipmentType: normalized(tokens[2].Value), Skills: map[byte]int64{}})
			active = len(out) - 1
			inside = true
		case "/check":
			active = -1
			inside = false
		case "skill":
			if !inside || active < 0 || len(tokens) < 3 || tokens[0].Kind != pvf.TokenInt || tokens[0].Int < 0 || tokens[0].Int > 31 || tokens[2].Kind != pvf.TokenInt || tokens[2].Int < 0 {
				continue
			}
			if JobMatches([]string{tokens[1].Value}, out[active].Job) {
				out[active].Skills[byte(tokens[0].Int)] = tokens[2].Int
			}
		}
	}
	return out
}

var jobNames = []string{"swordman", "fighter", "gunner", "mage", "priest", "at gunner", "thief", "at fighter", "at mage", "demonic swordman", "creator mage", "at swordman", "knight", "demonic lancer", "at priest", "gun blader"}

func JobMatches(names []string, job byte) bool {
	if int(job) >= len(jobNames) {
		return false
	}
	for _, s := range names {
		if normalized(s) == "all" || normalized(s) == jobNames[job] {
			return true
		}
	}
	return false
}

func (c *Catalog) Equipment(id int64) (Equipment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.equipment[id]; ok {
		return e, nil
	}
	doc, ref, err := c.document(id, true)
	if err != nil {
		return Equipment{}, err
	}
	rarity, ok := doc.Int("rarity")
	kind, _ := doc.Text("equipment type")
	typ := normalized(kind)
	typeID, valid := equipmentTypes[typ]
	dur, hasDur := doc.Int("durability")
	_, declaresDurability := doc.Section("durability")
	// The paired PVF has no durability field for jewelry and auxiliary
	// equipment. Its existing inventory projection uses zero for those types.
	// Missing/malformed durability on weapons and armor remains invalid.
	nonDurableType := typ == "amulet" || typ == "wrist" || typ == "ring" || typ == "support" || typ == "magic stone"
	if !ok || rarity != 5 || !valid || (!hasDur && (declaresDurability || !nonDurableType)) || dur < 0 || dur > 65535 {
		return Equipment{}, ErrTarget
	}
	jobs := []string{}
	if ts, found := doc.Section("usable job"); found {
		for _, t := range ts {
			if t.Kind == pvf.TokenString {
				jobs = append(jobs, t.Value)
			}
		}
	}
	checks := []Check{}
	for _, s := range doc.Sections {
		if normalized(s.Name) != "character item check" {
			continue
		}
		ts := sectionTokens(doc, s)
		// 共用装备在同一段内连续声明多组“职业、转职、部位”，各组为可选条件。
		// 每组三项必须完整有效；不能只读首组或因后续格式错误放行部分规则。
		if len(ts) == 0 || len(ts)%3 != 0 {
			return Equipment{}, fmt.Errorf("%w: 装备职业限制格式无效", ErrRules)
		}
		for offset := 0; offset < len(ts); offset += 3 {
			group := ts[offset : offset+3]
			if group[0].Kind != pvf.TokenInt || group[1].Kind != pvf.TokenInt || group[0].Int < 0 || group[0].Int >= 256 || group[1].Int < 0 || group[1].Int >= 16 {
				return Equipment{}, fmt.Errorf("%w: 装备职业限制格式无效", ErrRules)
			}
			checks = append(checks, Check{Job: byte(group[0].Int), Grow: byte(group[1].Int), EquipmentType: normalized(group[2].Value)})
		}
	}
	e := Equipment{ItemID: id, Path: ref.Path, Type: typ, TypeID: typeID, Rarity: rarity, Durability: uint16(dur), Jobs: jobs, CharacterChecks: checks}
	c.equipment[id] = e
	return e, nil
}

var equipmentTypes = map[string]byte{"weapon": 12, "coat": 14, "shoulder": 15, "pants": 16, "shoes": 17, "waist": 18, "amulet": 19, "wrist": 20, "ring": 21, "support": 22, "magic stone": 23}

func (m Material) Allows(job, grow byte, e Equipment, option byte) bool {
	if !JobMatches(e.Jobs, job) {
		return false
	}
	if len(e.CharacterChecks) > 0 {
		matched := false
		for _, c := range e.CharacterChecks {
			if c.Job == job && c.Grow == grow && c.EquipmentType == e.Type {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	for _, c := range m.Checks {
		if c.Job == job && c.Grow == grow && c.EquipmentType == e.Type {
			if _, ok := c.Skills[option]; ok {
				return true
			}
		}
	}
	return false
}
