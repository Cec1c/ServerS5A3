package battlepass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"longheng.io/server/internal/modules/dnf/cerashop"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"math/bits"
	"strconv"
	"time"
)

var ErrClaim = errors.New("战令条件不足或已领取")
var ErrPeriod = errors.New("战令赛季或刷新周期已变化")
var ErrUnavailable = errors.New("战令暂不可用")
var ErrFunds = errors.New("点券不足")
var ErrPremium = errors.New("本账号本赛季已开通高级战令")
var ErrQuote = errors.New("扣款方式已变化，请重新确认")

const MetadataKey = "battlepass_v2"
const PremiumPrice uint32 = 100

type Policy struct {
	ID         uint32
	Start, End time.Time
}

func (p Policy) Validate() error {
	if p.ID == 0 || p.Start.IsZero() || !p.End.After(p.Start) || p.End.Sub(p.Start) > 100*24*time.Hour {
		return ErrUnavailable
	}
	return nil
}
func (p Policy) Active(t time.Time) bool {
	return p.Validate() == nil && !t.Before(p.Start) && t.Before(p.End)
}
func DayNumber(t time.Time) uint32 {
	d, _ := time.Parse("2006-01-02", repo.ActivityDay(t))
	return uint32(d.Unix() / 86400)
}
func WeekNumber(t time.Time) uint32 {
	d, _ := time.Parse("2006-01-02", repo.ActivityDay(t))
	n := (int(d.Weekday()) + 6) % 7
	return uint32(d.AddDate(0, 0, -n).Unix() / 86400)
}

// TaskRefresh只供显式本机测试刷新；不改原行为计数及赛季累计。
// 基线在对应服务日/周有效，正常跨期后自动失效。
type TaskRefresh struct {
	Day, Week     uint32
	Daily, Weekly map[uint32]int64
}

type State struct {
	TaskRefresh                                                      *TaskRefresh `json:",omitempty"`
	Season                                                           uint32       `json:"season"`
	Day, Week                                                        uint32
	Level, XP                                                        uint32
	Premium                                                          bool
	PremiumPurchasedAt                                               int64  `json:",omitempty"`
	PremiumPricePaid                                                 uint32 `json:",omitempty"`
	PremiumFromCera, PremiumFromTokenCera, PremiumFromHappyTokenCera uint32 `json:",omitempty"`
	DailyClaimed, WeeklyClaimed                                      uint8
	SeasonClaimed                                                    uint32
	RewardClaimed                                                    [2][10]byte
	DailyCompleted, WeeklyCompleted, LoginDays                       uint32
	LastLoginDay                                                     uint32
	WeekLogins                                                       uint8
	LoginSeen                                                        [13]byte
	StartUnix, EndUnix                                               int64
	ConfigFingerprint                                                string
}
type MissionView struct {
	Mission
	Progress  uint32
	Flags     uint16
	GrantedXP uint32
}
type Snapshot struct {
	State                                   State
	Missions                                [26]MissionView
	Policy                                  Policy
	MailIDs                                 []string
	CeraBalance                             int64
	TokenCeraBalance, HappyTokenCeraBalance int64
}
type Counter struct {
	Day    string
	Kind   uint32
	Param  int64
	Amount int64
	Period uint8
}
type Command struct {
	AccountID, CharacterID    string
	Now                       time.Time
	Action, Category, Slot    uint8
	Season, Day, Week, Target uint32
	Counters                  []Counter
	Validate                  func() bool
	Deliver                   func(repo.MailboxRepository, []Reward, string) (string, error)
	Fingerprint               string
	QuoteHappy, QuoteToken    uint32
}

func Supported(kind uint32) bool {
	switch kind {
	case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 38, 39, 40, 41, 42, 43, 44, 45:
		return true
	}
	return false
}
func selected(c *Catalog, p Policy, now time.Time) (out [26]Mission) {
	day, week := DayNumber(now), WeekNumber(now)
	startDay, startWeek := DayNumber(p.Start), WeekNumber(p.Start)
	for i, id := range c.Daily[int(day-startDay)%len(c.Daily)] {
		out[i] = c.Missions[id]
	}
	for i, id := range c.Weekly[int((week-startWeek)/7)%len(c.Weekly)] {
		out[4+i] = c.Missions[id]
	}
	for i, id := range c.Season {
		out[8+i] = c.Missions[id]
	}
	return
}
func stateView(c *Catalog, p Policy, s State, cmd Command) Snapshot {
	out := Snapshot{State: s, Policy: p}
	defs := selected(c, p, cmd.Now)
	today := repo.ActivityDay(cmd.Now)
	start := repo.ActivityDay(p.Start)
	monday := time.Unix(int64(s.Week)*86400, 0).UTC().Format("2006-01-02")
	for i, m := range defs {
		category, slot := 0, i
		if i >= 8 {
			category, slot = 2, i-8
		} else if i >= 4 {
			category, slot = 1, i-4
		}
		v := MissionView{Mission: m, GrantedXP: m.XP}
		if !Supported(m.Kind) {
			v.Flags |= 8
		}
		if category == 0 && slot == 3 {
			if !s.Premium {
				v.Flags |= 4
			} else {
				v.GrantedXP *= c.PremiumRatio
			}
		}
		total := int64(0)
		if m.Kind == 30 {
			if category == 0 {
				total = 1
			} else if category == 1 {
				total = int64(bits.OnesCount8(s.WeekLogins))
			} else {
				total = int64(s.LoginDays)
			}
		}
		// 每周登录数独立服务日位图放入虚拟计数；不会因同账号多角色重复。
		for _, x := range cmd.Counters {
			if m.Kind == 30 {
				continue
			}
			if x.Period != 0 && x.Period != uint8(category+1) {
				continue
			}
			if x.Day < start || x.Day > today || x.Kind != m.Kind || (m.Param != -1 && x.Param != m.Param) || x.Amount < 0 {
				continue
			}
			if x.Period == 0 && (category == 0 && x.Day != today || category == 1 && x.Day < monday) {
				continue
			}
			total += min(x.Amount, math.MaxInt64-total)
		}
		if reset := s.TaskRefresh; reset != nil && category < 2 {
			baseline := int64(0)
			if category == 0 && reset.Day == DayNumber(cmd.Now) {
				baseline = reset.Daily[m.ID]
			} else if category == 1 && reset.Week == WeekNumber(cmd.Now) {
				baseline = reset.Weekly[m.ID]
			}
			total -= min(total, max(baseline, 0))
		}
		if m.Kind == 36 {
			if m.Param == 0 {
				total = int64(s.DailyCompleted)
			} else if m.Param == 1 {
				total = int64(s.WeeklyCompleted)
			}
		}
		v.Progress = uint32(min(int64(m.Target), max(total, 0)))
		if v.Progress >= m.Target && v.Flags&8 == 0 {
			v.Flags |= 1
		}
		claimed := category == 0 && s.DailyClaimed&(1<<slot) != 0 || category == 1 && s.WeeklyClaimed&(1<<slot) != 0 || category == 2 && s.SeasonClaimed&(1<<slot) != 0
		if claimed {
			v.Flags |= 2
		}
		out.Missions[i] = v
	}
	return out
}
func Claimed(s State, track, level int) bool {
	return level >= 1 && level <= 80 && track >= 0 && track < 2 && s.RewardClaimed[track][(level-1)/8]&(1<<((level-1)%8)) != 0
}
func LoadState(a repo.AccountRecord, p Policy, fingerprint string) (State, error) {
	s := State{}
	if raw := a.Metadata[MetadataKey]; raw != "" {
		if e := json.Unmarshal([]byte(raw), &s); e != nil {
			return s, e
		}
	}
	if s.Season != p.ID {
		return State{Season: p.ID, Level: 1, StartUnix: p.Start.Unix(), EndUnix: p.End.Unix(), ConfigFingerprint: fingerprint}, nil
	}
	if s.StartUnix != p.Start.Unix() || s.EndUnix != p.End.Unix() || s.ConfigFingerprint != fingerprint {
		return s, fmt.Errorf("赛季配置已变更，须先显式迁移或启用新赛季")
	}
	if s.Level < 1 || s.Level > 80 || s.DailyClaimed > 15 || s.WeeklyClaimed > 15 || s.SeasonClaimed>>18 != 0 || s.Week > s.Day {
		return s, fmt.Errorf("战令持久状态无效")
	}
	return s, nil
}

// 账号领取回执与全部奖励邮件同事务；不依赖客户端缓存或请求序号进行去重。
func Execute(ctx context.Context, g repo.Group, c *Catalog, p Policy, cmd Command) (Snapshot, error) {
	var out Snapshot
	if c == nil || !p.Active(cmd.Now) || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Action > 4 {
		return out, ErrUnavailable
	}
	u, ok := g.MailboxAssets.(repo.AccountMailboxCharacterUnitOfWork)
	if !ok {
		return out, ErrUnavailable
	}
	e := u.WithinAccountMailboxCharacter(ctx, cmd.AccountID, cmd.CharacterID, func(accounts repo.AccountRepository, ch repo.CharacterRecord, box repo.MailboxRepository) error {
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrUnavailable
		}
		if ch.AccountID != cmd.AccountID || ch.CharacterID != cmd.CharacterID {
			return ErrUnavailable
		}
		a, found, e := accounts.Load(ctx, cmd.AccountID)
		if e != nil {
			return e
		}
		if !found {
			return ErrUnavailable
		}
		s, e := LoadState(a, p, cmd.Fingerprint)
		if e != nil {
			return e
		}
		if e := normalizeState(c, p, &s, cmd); e != nil {
			return e
		}
		day, week := DayNumber(cmd.Now), WeekNumber(cmd.Now)
		// 登录按服务日位图归并，登录、换角和打开窗口同日只算一次。
		before := stateView(c, p, s, cmd)
		if cmd.Action != 0 && (cmd.Season != p.ID || cmd.Day != day || cmd.Week != week) {
			return ErrPeriod
		}
		switch cmd.Action {
		case 4:
			// 价格由服务端固定；同账号串行事务保护扣款与赛季权益，重发不重复扣款。
			if cmd.Category != 0 || cmd.Slot != 0 || cmd.Target != PremiumPrice || cmd.QuoteHappy > PremiumPrice || cmd.QuoteToken > PremiumPrice || cmd.QuoteHappy+cmd.QuoteToken > PremiumPrice {
				return ErrClaim
			}
			if s.Premium {
				return ErrPremium
			}
			payment, err := cerashop.PlanWalletPayment(cerashop.WalletBalance(a), int64(PremiumPrice), cerashop.PayPolicyWaterfall)
			if errors.Is(err, cerashop.ErrWalletInsufficient) {
				return ErrFunds
			}
			if err != nil {
				return err
			}
			// 确认后钱包变动不能静默改为多扣点券，实际扣款分配必须与确认一致。
			if payment.FromHappyTokenCera != int64(cmd.QuoteHappy) || payment.FromTokenCera != int64(cmd.QuoteToken) {
				return ErrQuote
			}
			a = repo.CloneAccount(a)
			cerashop.ApplyWalletPayment(&a, payment)
			for _, key := range []string{"account_cera", "account_token_cera", "account_happy_token_cera"} {
				if e = repo.SaveAccountMetadataEntry(ctx, accounts, a, key, a.Metadata[key], cmd.Now); e != nil {
					return e
				}
			}
			s.Premium = true
			s.PremiumPurchasedAt, s.PremiumPricePaid = cmd.Now.Unix(), PremiumPrice
			s.PremiumFromCera, s.PremiumFromTokenCera, s.PremiumFromHappyTokenCera = uint32(payment.FromCera), uint32(payment.FromTokenCera), uint32(payment.FromHappyTokenCera)
		case 1:
			if cmd.Category > 2 || cmd.Category < 2 && cmd.Slot >= 4 || cmd.Category == 2 && cmd.Slot >= 18 {
				return ErrClaim
			}
			idx := int(cmd.Slot)
			if cmd.Category == 1 {
				idx += 4
			} else if cmd.Category == 2 {
				idx += 8
			}
			v := before.Missions[idx]
			if v.ID != cmd.Target || v.Flags&15 != 1 || s.Level == 80 {
				return ErrClaim
			}
			if cmd.Category == 0 {
				s.DailyClaimed |= 1 << cmd.Slot
				s.DailyCompleted++
			} else if cmd.Category == 1 {
				s.WeeklyClaimed |= 1 << cmd.Slot
				s.WeeklyCompleted++
			} else {
				s.SeasonClaimed |= 1 << cmd.Slot
			}
			xp := uint64(s.XP) + uint64(v.GrantedXP)
			for s.Level < 80 && xp >= uint64(c.Experience[s.Level]) {
				xp -= uint64(c.Experience[s.Level])
				s.Level++
			}
			if s.Level == 80 {
				xp = 0
			}
			if xp > math.MaxUint32 {
				return ErrClaim
			}
			s.XP = uint32(xp)
		case 2, 3:
			if cmd.Deliver == nil {
				return ErrUnavailable
			}
			if cmd.Action == 2 && (cmd.Category > 1 || cmd.Target < 1 || cmd.Target > s.Level) {
				return ErrClaim
			}
			for track := 0; track < 2; track++ {
				for level := 1; level <= int(s.Level); level++ {
					if cmd.Action == 2 && (track != int(cmd.Category) || level != int(cmd.Target)) {
						continue
					}
					rewards := c.Rewards[track][level]
					if len(rewards) == 0 || Claimed(s, track, level) || track == 1 && !s.Premium {
						continue
					}
					key := fmt.Sprintf("%s:%d:%d:%d", MetadataKey, p.ID, track, level)
					id, err := cmd.Deliver(box, rewards, key)
					if err != nil {
						return err
					}
					if id == "" {
						return fmt.Errorf("战令奖励邮件未生成")
					}
					out.MailIDs = append(out.MailIDs, id)
					s.RewardClaimed[track][(level-1)/8] |= 1 << ((level - 1) % 8)
				}
			}
			if cmd.Action == 2 && len(out.MailIDs) == 0 {
				return ErrClaim
			}
		}
		out.State = s
		view := stateView(c, p, s, cmd)
		out.Missions = view.Missions
		out.Policy = p
		out.CeraBalance = cerashop.Balance(a)
		out.TokenCeraBalance, out.HappyTokenCeraBalance = cerashop.TokenCeraBalance(a), cerashop.HappyTokenCeraBalance(a)
		raw, e := json.Marshal(s)
		if e != nil {
			return e
		}
		if e = repo.SaveAccountMetadataEntry(ctx, accounts, a, MetadataKey, string(raw), cmd.Now); e != nil {
			return e
		}
		if cmd.Validate != nil && !cmd.Validate() {
			return ErrUnavailable
		}
		return nil
	})
	if e != nil {
		return Snapshot{}, e
	}
	return out, nil
}

// 从已持久化角色服务日桶读取；副本疲劳/深渊另由已去重的账号活动账本提供。
func Counters(characters []repo.CharacterRecord, start, end string) []Counter {
	sums := map[string]int64{}
	for _, ch := range characters {
		for k, v := range ch.Stats {
			day, _, _, ok := repo.ReadBattlePassCounterKey(k)
			if !ok || day < start || day > end || v <= 0 {
				continue
			}
			old := sums[k]
			sums[k] = old + min(v, math.MaxInt64-old)
		}
	}
	result := make([]Counter, 0, len(sums))
	for k, v := range sums {
		day, kind, param, _ := repo.ReadBattlePassCounterKey(k)
		result = append(result, Counter{day, kind, param, v, 0})
	}
	return result
}
func CounterKey(kind uint32, param int64) string {
	return strconv.FormatUint(uint64(kind), 10) + ":" + strconv.FormatInt(param, 10)
}

func normalizeState(c *Catalog, p Policy, s *State, cmd Command) error {
	day, week := DayNumber(cmd.Now), WeekNumber(cmd.Now)
	if s.Day > day || s.Week > week {
		return ErrPeriod
	}
	if s.Day != day {
		s.Day = day
		s.DailyClaimed = 0
	}
	if s.Week != week {
		s.Week = week
		s.WeeklyClaimed = 0
		s.WeekLogins = 0
	}
	s.LastLoginDay = day
	startDay := DayNumber(p.Start)
	markLogin := func(d uint32) {
		if d >= startDay && d <= day && d-startDay < 100 {
			s.LoginSeen[(d-startDay)/8] |= 1 << ((d - startDay) % 8)
		}
	}
	markLogin(day)
	for _, counter := range cmd.Counters {
		if counter.Kind == 30 && counter.Amount > 0 {
			at, e := time.Parse("2006-01-02", counter.Day)
			if e != nil {
				return e
			}
			markLogin(uint32(at.Unix() / 86400))
		}
	}
	s.LoginDays = 0
	s.WeekLogins = 0
	for i := uint32(0); i < 100; i++ {
		if s.LoginSeen[i/8]&(1<<(i%8)) != 0 {
			s.LoginDays++
			d := startDay + i
			if d >= week && d <= day {
				s.WeekLogins |= 1 << (d - week)
			}
		}
	}
	if s.Level < 80 && s.XP >= c.Experience[s.Level] || s.Level == 80 && s.XP != 0 {
		return fmt.Errorf("战令经验状态无效")
	}
	return nil
}

// Inspect 仅投影当前服务日状态，入口轮询不保存账号、进度或领取记录。
func Inspect(c *Catalog, p Policy, a repo.AccountRecord, cmd Command) (Snapshot, error) {
	if c == nil || !p.Active(cmd.Now) || cmd.AccountID == "" || a.AccountID != cmd.AccountID {
		return Snapshot{}, ErrUnavailable
	}
	s, e := LoadState(a, p, cmd.Fingerprint)
	if e != nil {
		return Snapshot{}, e
	}
	if e = normalizeState(c, p, &s, cmd); e != nil {
		return Snapshot{}, e
	}
	return stateView(c, p, s, cmd), nil
}

// ClaimNotifications与实际领取共用任务/等级/空奖励/高级权益限制。
// 位1/2/3是每日/每周/赛季，位4是等级奖励；入口勾和标签共用此值。
func ClaimNotifications(c *Catalog, v Snapshot) byte {
	if c == nil {
		return 0
	}
	var mask byte
	if v.State.Level < 80 {
		for i, m := range v.Missions {
			if m.Flags&15 == 1 {
				category := 0
				if i >= 8 {
					category = 2
				} else if i >= 4 {
					category = 1
				}
				mask |= 1 << uint(category+1)
			}
		}
	}
	for track := 0; track < 2; track++ {
		if track == 1 && !v.State.Premium {
			continue
		}
		for level := 1; level <= int(v.State.Level) && level <= 80; level++ {
			if len(c.Rewards[track][level]) > 0 && !Claimed(v.State, track, level) {
				mask |= 16
				break
			}
		}
	}
	return mask
}
func CanClaim(c *Catalog, v Snapshot) bool { return ClaimNotifications(c, v) != 0 }
