package onlineevent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const MarblePVFPath = "event/attendancemarble.evt"
const MarbleBoardSize = 28
const MarbleDayKey = "attendance_marble_calendar_day"

// etc/variousbufflist.etc, index 29: unique_rarity_item_drop_rate (3, 120).
const MarbleUniqueBonusPercent int64 = 120

func MarbleEquipmentRarityThresholds(thresholds [7]int64) [7]int64 {
	weight := max(int64(0), min(thresholds[3], int64(1000000))-thresholds[2])
	extra := weight * MarbleUniqueBonusPercent / 100
	// Take the added unique probability from lower rarities; higher tiers retain
	// their PVF probabilities and the cumulative denominator remains unchanged.
	for i := 0; i < 3; i++ {
		thresholds[i] = max(int64(0), thresholds[i]-extra)
	}
	return thresholds
}

var ErrMarbleState = errors.New("attendance marble state invalid")
var ErrMarbleSpent = errors.New("attendance marble earned turns exhausted")
var ErrMarbleGold = errors.New("attendance marble gold insufficient")

type MarbleSlot struct {
	Index, Type, Goto, Buff int
	ItemID, ItemCount, Gold int64
}
type MarbleStageReward struct {
	Round         int
	ItemID, Count int64
}
type MarbleCatalog struct {
	Slots        [MarbleBoardSize]MarbleSlot
	StageRewards []MarbleStageReward
	ClearRewards [3][2]int64
	PostalDays   int
}

func ParseMarbleCatalog(doc *dnfpvf.Document) (*MarbleCatalog, error) {
	if doc == nil {
		return nil, ErrMarbleState
	}
	c := &MarbleCatalog{}
	clears, err := attendanceIntegerSection(doc, "reward clear", 6)
	if err != nil {
		return nil, err
	}
	for i := range c.ClearRewards {
		if clears[i*2] != int64((i+1)*4) || clears[i*2+1] != 2 {
			return nil, fmt.Errorf("marble clear rewards differ from native four records")
		}
		c.ClearRewards[i] = [2]int64{clears[i*2], clears[i*2+1]}
	}
	postal, err := attendanceIntegerSection(doc, "reward postal day", 1)
	if err != nil || postal[0] < 1 || postal[0] > 365 {
		return nil, fmt.Errorf("marble postal duration invalid")
	}
	c.PostalDays = int(postal[0])
	stages, err := attendanceIntegerSection(doc, "accumulate race info", 15)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 3; i++ {
		v := stages[i*5:]
		if v[0] != int64((i+1)*4) || v[1] < 1 || v[1] > 0xffffffff || v[2] < 1 || v[2] > 0xffffffff {
			return nil, fmt.Errorf("marble lap reward invalid")
		}
		c.StageRewards = append(c.StageRewards, MarbleStageReward{Round: int(v[0]), ItemID: v[1], Count: v[2]})
	}
	seen := [MarbleBoardSize]bool{}
	for i := 0; i < len(doc.Tokens); i++ {
		t := doc.Tokens[i]
		if t.Kind != dnfpvf.TokenSection || !strings.EqualFold(t.Value, "slot") {
			continue
		}
		fields := map[string][]int64{}
		name := ""
		i++
		for ; i < len(doc.Tokens); i++ {
			t = doc.Tokens[i]
			if t.Kind == dnfpvf.TokenSection {
				name = strings.ToLower(strings.TrimSpace(t.Value))
				if name == "/slot" {
					break
				}
			} else if t.Kind == dnfpvf.TokenInt {
				fields[name] = append(fields[name], t.Int)
			}
		}
		if i == len(doc.Tokens) || len(fields["index"]) != 1 || len(fields["type"]) != 1 {
			return nil, ErrMarbleState
		}
		index, kind := int(fields["index"][0]), int(fields["type"][0])
		if index < 0 || index >= MarbleBoardSize || seen[index] {
			return nil, ErrMarbleState
		}
		slot := MarbleSlot{Index: index, Type: kind, Goto: -1}
		switch kind {
		case 1, 5:
			v := fields["item"]
			if len(v) != 2 || v[0] < 1 || v[0] > 0xffffffff || v[1] < 1 || v[1] > 0xffffffff {
				return nil, ErrMarbleState
			}
			slot.ItemID, slot.ItemCount = v[0], v[1]
			if kind == 5 {
				v = fields["gold"]
				if len(v) != 1 || v[0] < 1 {
					return nil, ErrMarbleState
				}
				slot.Gold = v[0]
			}
		case 3:
			v := fields["goto"]
			if len(v) != 1 || v[0] < 0 || v[0] >= MarbleBoardSize {
				return nil, ErrMarbleState
			}
			slot.Goto = int(v[0])
		case 7:
			v := fields["buff index"]
			if len(v) != 1 || v[0] < 29 || v[0] > 32 {
				return nil, ErrMarbleState
			}
			slot.Buff = int(v[0])
		default:
			return nil, fmt.Errorf("marble unsupported slot type %d", kind)
		}
		seen[index], c.Slots[index] = true, slot
	}
	for _, present := range seen {
		if !present {
			return nil, ErrMarbleState
		}
	}
	for _, slot := range c.Slots {
		if slot.Type == 3 && c.Slots[slot.Goto].Type == 3 {
			return nil, ErrMarbleState
		}
	}
	return c, nil
}

func MarbleDay(now time.Time) int64 {
	now = now.In(ChinaCalendar).Add(-6 * time.Hour)
	return int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
}

type MarbleState struct {
	Position, Round, DiceUsed, Double, DoubleChain int64
	Reward4, Reward8, Reward12, CalendarDay        int64
	Clears, Turns, Serial                          int64
	MailIDs                                        []string
	GoldChanged                                    bool
	GoldAfter                                      int64
	BuffIndex                                      int
	PendingBuffs                                   [4]int64
}

func (s MarbleState) Remaining() int64 { return s.Turns - s.DiceUsed }

func ReadMarbleState(character dnfrepo.CharacterRecord, c *MarbleCatalog, now time.Time) (MarbleState, error) {
	if c == nil {
		return MarbleState{}, ErrMarbleState
	}
	v := character.Stats
	for i := 29; i <= 32; i++ {
		if count := v[fmt.Sprintf("attendance_marble_buff_%d", i)]; count < 0 || count > 10000 {
			return MarbleState{}, ErrMarbleState
		}
	}
	s := MarbleState{Position: v["attendance_marble_position"], Round: v["attendance_marble_round"], DiceUsed: v["attendance_marble_dice_used"], Double: v["attendance_marble_double"], DoubleChain: v["attendance_marble_double_chain"],
		Reward4: v["attendance_marble_reward_4"], Reward8: v["attendance_marble_reward_8"], Reward12: v["attendance_marble_reward_12"], CalendarDay: v[MarbleDayKey], Clears: v["attendance_marble_clears"], Serial: v["attendance_marble_serial"]}
	if s.CalendarDay != MarbleDay(now) {
		s.CalendarDay, s.Clears, s.DiceUsed, s.Double, s.DoubleChain = MarbleDay(now), 0, 0, 0, 0
	}
	s.Turns = 2
	for _, r := range c.ClearRewards {
		if s.Clears >= r[0] {
			s.Turns += r[1]
		}
	}
	if s.Position < 0 || s.Position >= MarbleBoardSize || s.Round < 0 || s.Round > 0xffffffff || s.Clears < 0 || s.Clears > 12 || s.DiceUsed < 0 || s.DiceUsed > 8 || s.Double < 0 || s.Double > 1 || s.DoubleChain < 0 || s.DoubleChain > 5 || s.Serial < 0 || s.Reward4 < 0 || s.Reward4 > 1 || s.Reward8 < 0 || s.Reward8 > 1 || s.Reward12 < 0 || s.Reward12 > 1 {
		return MarbleState{}, ErrMarbleState
	}
	// Earlier builds allowed three unearned turns; preserve them as spent, never negative on the wire.
	if s.Turns < s.DiceUsed {
		s.Turns = s.DiceUsed
	}
	return s, nil
}

func writeMarbleState(character *dnfrepo.CharacterRecord, s MarbleState) {
	if character.Stats == nil {
		character.Stats = map[string]int64{}
	}
	v := character.Stats
	for k, value := range map[string]int64{
		"position": s.Position, "round": s.Round, "dice_used": s.DiceUsed, "double": s.Double, "double_chain": s.DoubleChain,
		"reward_4": s.Reward4, "reward_8": s.Reward8, "reward_12": s.Reward12, "calendar_day": s.CalendarDay, "clears": s.Clears, "serial": s.Serial,
	} {
		v["attendance_marble_"+k] = value
	}
}

func RecordMarbleClear(character *dnfrepo.CharacterRecord, c *MarbleCatalog, now time.Time) error {
	if c == nil {
		return nil
	}
	s, err := ReadMarbleState(*character, c, now)
	if err != nil {
		return err
	}
	if s.Clears < 12 {
		s.Clears++
	}
	writeMarbleState(character, s)
	return nil
}

type MarbleCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	Dice                   *[2]byte
	CloseDouble            bool
	Deliver                func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

func SettleMarble(ctx context.Context, repos dnfrepo.Group, c *MarbleCatalog, cmd MarbleCommand) (MarbleState, error) {
	if c == nil || repos.MailboxAssets == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Now.IsZero() {
		return MarbleState{}, ErrOwnerUnavailable
	}
	var result MarbleState
	err := repos.MailboxAssets.WithinMailboxAssets(ctx, cmd.CharacterID, cmd.CharacterID, func(characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, mailboxes dnfrepo.MailboxRepository) error {
		character, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != cmd.CharacterID || character.AccountID != cmd.AccountID {
			return ErrMarbleState
		}
		s, err := ReadMarbleState(character, c, cmd.Now)
		if err != nil {
			return err
		}
		writeMarbleState(&character, s)
		if cmd.CloseDouble {
			s.Double, s.DoubleChain = 0, 0
		}
		if cmd.Dice != nil {
			if s.Serial == math.MaxInt64 || s.Round == math.MaxUint32 {
				return ErrMarbleState
			}
			dice := *cmd.Dice
			if dice[0] < 1 || dice[0] > 6 || dice[1] < 1 || dice[1] > 6 || cmd.Deliver == nil {
				return ErrMarbleState
			}
			if s.Double != 0 {
				s.DoubleChain++
			} else {
				if s.Remaining() <= 0 {
					return ErrMarbleSpent
				}
				s.DiceUsed++
				s.DoubleChain = 0
			}
			s.Serial++
			move := s.Position + int64(dice[0]) + int64(dice[1])
			s.Round += move / MarbleBoardSize
			s.Position = move % MarbleBoardSize
			slot := c.Slots[s.Position]
			if slot.Type == 3 {
				s.Position = int64(slot.Goto)
				slot = c.Slots[s.Position]
			}
			deliver := func(id, count int64, key string) error {
				mail, err := cmd.Deliver(mailboxes, AttendancePVFItemReward{ItemID: id, Count: count}, "attendance_marble:"+key)
				if err != nil {
					return err
				}
				if mail == "" {
					return ErrMarbleState
				}
				s.MailIDs = append(s.MailIDs, mail)
				return nil
			}
			if slot.Type == 1 || slot.Type == 5 {
				if slot.Type == 5 {
					gold := character.Stats["gold"]
					if gold < slot.Gold {
						return ErrMarbleGold
					}
					character.Stats["gold"] = gold - slot.Gold
					s.GoldChanged, s.GoldAfter = true, gold-slot.Gold
				}
				if err := deliver(slot.ItemID, slot.ItemCount, fmt.Sprintf("turn:%d", s.Serial)); err != nil {
					return err
				}
			}
			if slot.Type == 7 {
				key := fmt.Sprintf("attendance_marble_buff_%d", slot.Buff)
				character.Stats[key] = min(character.Stats[key]+1, int64(10000))
				s.BuffIndex = slot.Buff
			}
			for _, stage := range c.StageRewards {
				var claimed *int64
				switch stage.Round {
				case 4:
					claimed = &s.Reward4
				case 8:
					claimed = &s.Reward8
				case 12:
					claimed = &s.Reward12
				}
				if claimed == nil {
					return ErrMarbleState
				}
				if s.Round >= int64(stage.Round) && *claimed == 0 {
					if err := deliver(stage.ItemID, stage.Count, fmt.Sprintf("lap:%d", stage.Round)); err != nil {
						return err
					}
					*claimed = 1
				}
			}
			s.Double = 0
			if dice[0] == dice[1] && s.DoubleChain < 5 {
				s.Double = 1
			}
		}
		writeMarbleState(&character, s)
		if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		for i := 29; i <= 32; i++ {
			s.PendingBuffs[i-29] = character.Stats[fmt.Sprintf("attendance_marble_buff_%d", i)]
		}
		result = s
		return nil
	})
	return result, err
}

// VariousBuff 29..32 use dungeon_one_match (1, 0): reserve the pending
// effects for one accepted run; its runtime retains them across room changes.
func ConsumeMarbleDungeonBuffs(ctx context.Context, repos dnfrepo.Group, accountID, characterID string) (byte, error) {
	if repos.CharacterAssets == nil || accountID == "" || characterID == "" {
		return 0, ErrOwnerUnavailable
	}
	var mask byte
	err := repos.CharacterAssets.WithinCharacterAssets(ctx, characterID, func(chars dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		ch, found, err := chars.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || ch.CharacterID != characterID || ch.AccountID != accountID {
			return ErrMarbleState
		}
		for i := 29; i <= 32; i++ {
			key := fmt.Sprintf("attendance_marble_buff_%d", i)
			if ch.Stats[key] > 0 {
				mask |= 1 << (i - 29)
				ch.Stats[key]--
			}
		}
		if mask == 0 {
			return nil
		}
		return dnfrepo.SaveCharacterFields(ctx, chars, ch, dnfrepo.CharacterFieldStats)
	})
	if err != nil {
		return 0, err
	}
	return mask, nil
}
