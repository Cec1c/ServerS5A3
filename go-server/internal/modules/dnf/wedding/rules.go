// Package wedding owns marriage relationships and the PVF marriage rules.
package wedding

import (
	"fmt"
	"sort"
	"strings"

	"longheng.io/server/internal/modules/dnf/pvf"
)

const (
	SystemPath = "etc/weddingsystem.etc"
	RoomPath   = "decoration/coupleroomiteminfo.etc"
)

// Bonuses keeps ability fields separate from weapon damage and town speed.
// Speed values retain the native PVF units; the bridge owns wire conversion.
type Bonuses struct {
	Strength, Intelligence, Vitality, Spirit                      int64
	HP, MP, PhysicalAttack, MagicalAttack, IndependentAttack      int64
	ElementalAttack, PhysicalCritical, MagicalCritical            int64
	MoveSpeed, AttackSpeed, CastSpeed, HitRecovery, TownMoveSpeed int64
}

func (b Bonuses) Add(v Bonuses) Bonuses {
	b.Strength += v.Strength
	b.Intelligence += v.Intelligence
	b.Vitality += v.Vitality
	b.Spirit += v.Spirit
	b.HP += v.HP
	b.MP += v.MP
	b.PhysicalAttack += v.PhysicalAttack
	b.MagicalAttack += v.MagicalAttack
	b.IndependentAttack += v.IndependentAttack
	b.ElementalAttack += v.ElementalAttack
	b.PhysicalCritical += v.PhysicalCritical
	b.MagicalCritical += v.MagicalCritical
	b.MoveSpeed += v.MoveSpeed
	b.AttackSpeed += v.AttackSpeed
	b.CastSpeed += v.CastSpeed
	b.HitRecovery += v.HitRecovery
	b.TownMoveSpeed += v.TownMoveSpeed
	return b
}

type Ring struct {
	Stage                 int
	Bonuses               Bonuses
	TicketID, TicketCount int64
}
type RoomTier struct {
	Points  int64
	Bonuses Bonuses
}
type Ceremony struct {
	Kind                  byte
	Gold                  int64
	Ring                  int
	Invitations           int64
	TicketID, TicketCount int64
}
type Rules struct {
	CeremonySeconds                            int64
	EngagementCancellationGold                 int64
	Furniture                                  map[int64]Furniture
	LovePointItems                             map[int64]LovePointCoupon
	CeremonyGifts                              map[byte]Gift
	ForceMove                                  [2]CeremonyPosition
	MinimumLevel, MaxLovePoint, DailyLovePoint int64
	QuestIDs                                   []int64
	Rings                                      []Ring
	RoomTiers                                  []RoomTier
	Ceremonies                                 map[byte]Ceremony
	Durations                                  []int64
}

func Load(source pvf.Source) (*Rules, error) {
	if source == nil {
		return nil, fmt.Errorf("wedding: PVF source missing")
	}
	read := func(path string) (*pvf.Document, error) {
		text, err := source.ReadText(path)
		if err != nil {
			return nil, err
		}
		return pvf.Parse(path, text)
	}
	system, err := read(SystemPath)
	if err != nil {
		return nil, fmt.Errorf("wedding rules: %w", err)
	}
	room, err := read(RoomPath)
	if err != nil {
		return nil, fmt.Errorf("wedding room rules: %w", err)
	}
	r := &Rules{Ceremonies: make(map[byte]Ceremony)}
	r.CeremonySeconds, _ = system.Int("noti end")
	if r.CeremonySeconds <= 0 || r.CeremonySeconds > 86400 {
		return nil, fmt.Errorf("wedding: invalid ceremony end interval")
	}
	r.MinimumLevel, _ = system.Int("minimum level")
	r.MaxLovePoint, _ = system.Int("max love point")
	r.DailyLovePoint, _ = system.Int("love point")
	r.QuestIDs = system.Ints("quest")
	r.Durations = system.Ints("minute selection")
	if alimony := system.Ints("alimony"); len(alimony) == 2 && alimony[0] >= 0 && alimony[1] >= 0 {
		// The current native cancellation confirmation and this PVF agree:
		// unilateral engagement cancellation charges the first alimony value.
		r.EngagementCancellationGold = alimony[0]
	}
	if r.MinimumLevel <= 0 || r.MaxLovePoint <= 0 || r.MaxLovePoint > 0xffffffff || r.DailyLovePoint < 0 || r.DailyLovePoint > r.MaxLovePoint || len(r.QuestIDs) != 2 || len(r.Durations) == 0 {
		return nil, fmt.Errorf("wedding: invalid level/point/quest/duration rules")
	}
	for _, id := range r.QuestIDs {
		if id <= 0 {
			return nil, fmt.Errorf("wedding: invalid quest")
		}
	}
	for _, duration := range r.Durations {
		if duration <= 0 {
			return nil, fmt.Errorf("wedding: invalid duration")
		}
	}
	ringRows, err := buffRows(system, "wedding ring buff", false)
	if err != nil {
		return nil, err
	}
	for i, row := range ringRows {
		if row.Points != int64(i) {
			return nil, fmt.Errorf("wedding: non-contiguous ring stages")
		}
		r.Rings = append(r.Rings, Ring{Stage: i, Bonuses: row.Bonuses})
	}
	if len(r.Rings) < 2 {
		return nil, fmt.Errorf("wedding: ring stages missing")
	}
	tickets := system.Ints("upgrade ring ticket")
	if len(tickets) != len(r.Rings)*2 {
		return nil, fmt.Errorf("wedding: ring ticket shape")
	}
	for i := range r.Rings {
		id, count := tickets[i*2], tickets[i*2+1]
		if (i == 0 && (id != 0 || count != 0)) || (i > 0 && (id <= 0 || count <= 0)) {
			return nil, fmt.Errorf("wedding: invalid ring ticket")
		}
		r.Rings[i].TicketID, r.Rings[i].TicketCount = id, count
	}
	r.RoomTiers, err = buffRows(room, "couple room buff", true)
	if err != nil {
		return nil, err
	}
	ceremonies := system.Ints("ceremony")
	if len(ceremonies) == 0 || len(ceremonies)%4 != 0 {
		return nil, fmt.Errorf("wedding: ceremony shape")
	}
	for i := 0; i < len(ceremonies); i += 4 {
		kind := ceremonies[i]
		if kind < 0 || kind > 255 || ceremonies[i+1] < 0 || ceremonies[i+2] < 0 || ceremonies[i+2] >= int64(len(r.Rings)) || ceremonies[i+3] <= 0 || ceremonies[i+3] > 65535 {
			return nil, fmt.Errorf("wedding: invalid ceremony")
		}
		if _, found := r.Ceremonies[byte(kind)]; found {
			return nil, fmt.Errorf("wedding: duplicate ceremony")
		}
		r.Ceremonies[byte(kind)] = Ceremony{Kind: byte(kind), Gold: ceremonies[i+1], Ring: int(ceremonies[i+2]), Invitations: ceremonies[i+3]}
	}
	charged := system.Ints("charged wedding ticket")
	if len(charged)%3 != 0 {
		return nil, fmt.Errorf("wedding: charged ceremony shape")
	}
	for i := 0; i < len(charged); i += 3 {
		if charged[i] < 0 || charged[i] > 255 {
			return nil, fmt.Errorf("wedding: charged ceremony kind")
		}
		c, ok := r.Ceremonies[byte(charged[i])]
		if !ok || charged[i+1] <= 0 || charged[i+2] <= 0 {
			return nil, fmt.Errorf("wedding: charged ceremony ticket")
		}
		c.TicketID, c.TicketCount = charged[i+1], charged[i+2]
		r.Ceremonies[c.Kind] = c
	}
	gifts := system.Ints("wedding gift")
	if len(gifts) != len(r.Ceremonies)*3 {
		return nil, fmt.Errorf("wedding: ceremony gift shape")
	}
	r.CeremonyGifts = make(map[byte]Gift, len(r.Ceremonies))
	for i := 0; i < len(gifts); i += 3 {
		kind := gifts[i]
		if kind < 0 || kind > 255 || gifts[i+1] <= 0 || gifts[i+1] > 0xffffffff || gifts[i+2] <= 0 || gifts[i+2] > 0xffffffff {
			return nil, fmt.Errorf("wedding: invalid ceremony gift")
		}
		if _, known := r.Ceremonies[byte(kind)]; !known {
			return nil, fmt.Errorf("wedding: unknown ceremony gift kind")
		}
		if _, duplicate := r.CeremonyGifts[byte(kind)]; duplicate {
			return nil, fmt.Errorf("wedding: duplicate ceremony gift kind")
		}
		r.CeremonyGifts[byte(kind)] = Gift{ItemID: gifts[i+1], Count: gifts[i+2]}
	}
	positions := system.Ints("force move")
	if len(positions) != 6 {
		return nil, fmt.Errorf("wedding: ceremony position shape")
	}
	for i := range r.ForceMove {
		x, y, direction := positions[i*3], positions[i*3+1], positions[i*3+2]
		if x < 0 || x > 32767 || y < 0 || y > 32767 || direction < 0 || direction > 255 {
			return nil, fmt.Errorf("wedding: invalid ceremony position")
		}
		r.ForceMove[i] = CeremonyPosition{X: int16(x), Y: int16(y), Direction: byte(direction)}
	}
	r.LovePointItems, err = loadLovePointItems(source, system, r.MaxLovePoint)
	if err != nil {
		return nil, err
	}
	r.Furniture, err = loadFurniture(source, room)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rules) Attributes(stage int, roomPoints int64, married, pvp bool) (Bonuses, error) {
	if !married || pvp {
		return Bonuses{}, nil
	}
	if r == nil || stage < 0 || stage >= len(r.Rings) || roomPoints < 0 {
		return Bonuses{}, fmt.Errorf("wedding: invalid attribute state")
	}
	result := r.Rings[stage].Bonuses
	for i := len(r.RoomTiers) - 1; i >= 0; i-- {
		if roomPoints >= r.RoomTiers[i].Points {
			result = result.Add(r.RoomTiers[i].Bonuses)
			break
		}
	}
	return result, nil
}

func buffRows(doc *pvf.Document, section string, room bool) ([]RoomTier, error) {
	tokens, ok := doc.Section(section)
	if !ok || len(tokens) == 0 {
		return nil, fmt.Errorf("wedding: missing %s", section)
	}
	rows := []RoomTier{}
	seen := map[int64]bool{}
	for i := 0; i < len(tokens); {
		if i+2 > len(tokens) || tokens[i].Kind != pvf.TokenInt || tokens[i+1].Kind != pvf.TokenInt {
			return nil, fmt.Errorf("wedding: malformed %s", section)
		}
		key, count := tokens[i].Int, tokens[i+1].Int
		i += 2
		if key < 0 || count <= 0 || count > 32 || seen[key] || count > int64((len(tokens)-i)/2) {
			return nil, fmt.Errorf("wedding: invalid %s row", section)
		}
		seen[key] = true
		row := RoomTier{Points: key}
		attributes := map[string]bool{}
		for j := int64(0); j < count; j++ {
			name := strings.ToLower(strings.TrimSpace(tokens[i].Value))
			if tokens[i].Kind != pvf.TokenString || tokens[i+1].Kind != pvf.TokenInt || tokens[i+1].Int < 0 || attributes[name] {
				return nil, fmt.Errorf("wedding: malformed %s ability", section)
			}
			attributes[name] = true
			if err := setAbility(&row.Bonuses, name, tokens[i+1].Int, room); err != nil {
				return nil, err
			}
			i += 2
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Points < rows[j].Points })
	return rows, nil
}

func setAbility(b *Bonuses, name string, value int64, room bool) error {
	if room {
		switch name {
		case "hp max":
			b.HP = value
		case "mp max":
			b.MP = value
		case "village moving speed":
			b.TownMoveSpeed = value
		case "all elemental attack":
			b.ElementalAttack = value
		case "equipment physical attack":
			b.PhysicalAttack = value
		case "equipment magical attack":
			b.MagicalAttack = value
		case "separate attack":
			b.IndependentAttack = value
		default:
			return fmt.Errorf("wedding: unsupported room ability %q", name)
		}
	} else {
		switch name {
		case "physical attack":
			b.Strength = value
		case "magical attack":
			b.Intelligence = value
		case "physical defense":
			b.Vitality = value
		case "magical defense":
			b.Spirit = value
		case "hit recovery":
			b.HitRecovery = value
		case "move speed":
			b.MoveSpeed = value
		case "attack speed":
			b.AttackSpeed = value
		case "cast speed":
			b.CastSpeed = value
		case "physical critical hit rate":
			b.PhysicalCritical = value
		case "magical critical hit rate":
			b.MagicalCritical = value
		default:
			return fmt.Errorf("wedding: unsupported ring ability %q", name)
		}
	}
	return nil
}
