package tagtournament

import (
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// DayState 由Owner在账号事务中保存，不可从客户端请求构造或按角色保存。
type DayState struct {
	AccountID    string
	ServiceDay   int64
	Mode         Mode
	Paid         bool
	ConfigHash   string
	NormalClaims map[uint32]bool
	HardCleared  int
	ActiveRun    string
	ActiveRoster [2]string
}

type EntryQuote struct {
	Fee  uint32
	Plan DailyPlan
	Next DayState
}

// QuoteEntry 只报价、不扣费。调用方必须在同一账号事务内校验两名权威角色、
// 扣当前角色金币并提交Next，提交成功后才允许创建战斗与投影双角色。
func (c *Catalog) QuoteEntry(now time.Time, mode Mode, accountID string, roster [2]repo.CharacterRecord, gold uint64, state DayState) (EntryQuote, error) {
	if c == nil || now.IsZero() || accountID == "" || accountID != strings.TrimSpace(accountID) {
		return EntryQuote{}, fmt.Errorf("帝国竞技场缺少权威入场上下文")
	}
	if c.config.DailyRewardCount != 1 || c.config.DailyExtraRewardCount != 0 {
		return EntryQuote{}, fmt.Errorf("帝国竞技场每日奖励次数须为一次且无额外次数")
	}
	dungeon, ok := c.Dungeon(mode)
	if !ok {
		return EntryQuote{}, fmt.Errorf("帝国竞技场模式无效")
	}
	for _, character := range roster {
		if character.AccountID != accountID || character.CharacterID == "" || character.CharacterID != strings.TrimSpace(character.CharacterID) || character.Level < int(dungeon.MinimumLevel) {
			return EntryQuote{}, fmt.Errorf("帝国竞技场需同账号两名达到PVF等级要求的角色")
		}
	}
	if roster[0].CharacterID == roster[1].CharacterID {
		return EntryQuote{}, fmt.Errorf("帝国竞技场不能重复注册同一角色")
	}
	day := premium.ServiceDayID(now)
	if state.ServiceDay > day {
		return EntryQuote{}, fmt.Errorf("帝国竞技场日账本时间超前")
	}
	if state.ServiceDay < day {
		state = DayState{ServiceDay: day}
	}
	if state.Paid && (state.AccountID != accountID || state.Mode != Normal && state.Mode != Hard || state.ConfigHash == "") {
		return EntryQuote{}, fmt.Errorf("帝国竞技场已付费日账本不完整")
	}
	if !state.Paid && (state.Mode != 0 || state.ConfigHash != "" || len(state.NormalClaims) != 0 || state.HardCleared != 0 || state.ActiveRun != "" || state.ActiveRoster != [2]string{}) {
		return EntryQuote{}, fmt.Errorf("帝国竞技场日账本缺少支付记录")
	}
	if state.AccountID != "" && state.AccountID != accountID {
		return EntryQuote{}, fmt.Errorf("帝国竞技场日账本账号不匹配")
	}
	if state.Paid && state.Mode != mode {
		return EntryQuote{}, fmt.Errorf("帝国竞技场今日已付费，06点前不能变更模式")
	}
	if state.Paid && state.ConfigHash != c.config.Fingerprint {
		return EntryQuote{}, fmt.Errorf("帝国竞技场配置已变更，需核对当日日计划后恢复")
	}
	plan, err := c.DailyPlan(now, mode)
	if err != nil {
		return EntryQuote{}, err
	}
	quote := EntryQuote{Plan: plan, Next: cloneDay(state)}
	if !state.Paid {
		quote.Fee = c.config.EntryGold
		if gold < uint64(quote.Fee) {
			return EntryQuote{}, fmt.Errorf("帝国竞技场首次入场金币不足")
		}
		quote.Next.Paid, quote.Next.Mode, quote.Next.ConfigHash = true, mode, c.config.Fingerprint
		quote.Next.AccountID = accountID
	}
	return quote, nil
}

func cloneDay(state DayState) DayState {
	claims := make(map[uint32]bool, len(state.NormalClaims))
	for id, claimed := range state.NormalClaims {
		claims[id] = claimed
	}
	state.NormalClaims = claims
	return state
}

// QuoteNormalReward 只能由服务端确认的怪物死亡结算调用，不能以客户端怪物编号
// 直接发奖。返回新日账本及邮件附件计划；持久化时必须与邮件共用账号事务。
func (c *Catalog) QuoteNormalReward(now time.Time, monsterID uint32, rolls map[byte]byte, state DayState) ([]RewardRow, DayState, error) {
	if c == nil || now.IsZero() || !state.Paid || state.Mode != Normal || state.ServiceDay != premium.ServiceDayID(now) || state.ConfigHash != c.config.Fingerprint {
		return nil, DayState{}, fmt.Errorf("帝国竞技场奖励缺少当日普通入场记录")
	}
	plan, err := c.DailyPlan(now, Normal)
	if err != nil {
		return nil, DayState{}, err
	}
	special, found := false, false
	for _, encounter := range plan.Encounters {
		if encounter.MonsterID == monsterID {
			found, special = true, encounter.Optional
		}
	}
	if !found {
		return nil, DayState{}, fmt.Errorf("帝国竞技场怪物不在当日名单")
	}
	if state.NormalClaims[monsterID] {
		return nil, cloneDay(state), nil
	}
	table := c.config.NormalRewards
	if special {
		table = c.config.SpecialRewards
	}
	groups := map[byte][]RewardRow{}
	var order []byte
	for _, row := range table {
		if _, exists := groups[row.Index]; !exists {
			order = append(order, row.Index)
		}
		groups[row.Index] = append(groups[row.Index], row)
	}
	if len(rolls) != len(groups) {
		return nil, DayState{}, fmt.Errorf("帝国竞技场奖励随机分组不完整")
	}
	var rewards []RewardRow
	for _, group := range order {
		roll, exists := rolls[group]
		if !exists || roll >= 100 {
			return nil, DayState{}, fmt.Errorf("帝国竞技场奖励随机值无效")
		}
		limit := 0
		for _, row := range groups[group] {
			limit += int(row.Weight)
			if int(roll) < limit {
				rewards = append(rewards, row)
				break
			}
		}
	}
	next := cloneDay(state)
	next.NormalClaims[monsterID] = true
	return rewards, next, nil
}

// HardRewardTotal 返回已通关回合对应的总额，不累计七列表。
// 重复挑战由Owner采用同日最高通关进度补差政策，不依赖用时或排行。
func (c *Catalog) HardRewardTotal(clearedRounds int) ([]RewardRow, error) {
	if c == nil {
		return nil, fmt.Errorf("帝国竞技场资源目录未加载")
	}
	return c.config.HardRewardAtStage(clearedRounds - 1)
}
