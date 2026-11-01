package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/jobmap"
	"longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const LevelChallengePVFPath = "event/createeventcharacter.evt"
const LevelChallengeGiftPVFPath = "event/eventgift.evt"
const LevelChallengeMetadataKey = "event_level_challenge_288_v1"
const LevelChallengeRoleStat = "level_challenge_288_role"

type LevelChallengeCatalog struct {
	AutomaticNewCharacters                             bool // Private PVF opt-in; ordinary newly created roles join atomically.
	Limit                                              int
	Job                                                string
	Window                                             ActivityWindow
	Main, Bonus                                        []LegacyLevelReward
	MainSender, MainMessage, BonusSender, BonusMessage string
}

func LoadLevelChallengeCatalog(ctx context.Context, source pvf.Source) (*LevelChallengeCatalog, error) {
	text, err := source.ReadText(LevelChallengePVFPath)
	if err != nil {
		return nil, err
	}
	doc, err := pvf.Parse(LevelChallengePVFPath, text)
	if err != nil {
		return nil, err
	}
	v, err := attendanceIntegerSection(doc, "max create count", 1)
	if err != nil {
		return nil, err
	}
	if v[0] < 1 || v[0] > 255 {
		return nil, fmt.Errorf("升级大挑战创建上限无效")
	}
	c := &LevelChallengeCatalog{Limit: int(v[0])}
	if fields, present := doc.Section("private automatic new character"); present {
		if len(fields) != 1 || fields[0].Kind != pvf.TokenInt || fields[0].Int < 0 || fields[0].Int > 1 {
			return nil, fmt.Errorf("升级大挑战自动参加配置无效")
		}
		c.AutomaticNewCharacters = fields[0].Int == 1
	}
	readString := func(d *pvf.Document, key string) (string, error) {
		t, ok := d.Section(key)
		if !ok || len(t) != 1 || t[0].Kind != pvf.TokenString || strings.TrimSpace(t[0].Value) == "" {
			return "", fmt.Errorf("升级大挑战配置[%s]无效", key)
		}
		return t[0].Value, nil
	}
	c.Job, err = readString(doc, "character job")
	if err != nil {
		return nil, err
	}
	validJob := strings.EqualFold(c.Job, "[all]")
	for job := 0; job <= jobmap.MaxID; job++ {
		validJob = validJob || jobmap.MatchesQuestTag(c.Job, job)
	}
	if !validJob {
		return nil, fmt.Errorf("升级大挑战职业配置无效: %s", c.Job)
	}
	text, err = source.ReadText(LevelChallengeGiftPVFPath)
	if err != nil {
		return nil, err
	}
	// EventGift含多个同名字段，只读取level up reward内部，不能取前一个礼包的event index。
	lower := strings.ToLower(text)
	start, end := strings.Index(lower, "[level up reward]"), strings.Index(lower, "[/level up reward]")
	if start < 0 || end <= start || strings.Count(lower, "[level up reward]") != 1 {
		return nil, fmt.Errorf("升级大挑战奖励段缺失或重复")
	}
	doc, err = pvf.Parse(LevelChallengeGiftPVFPath, text[start:end])
	if err != nil {
		return nil, err
	}
	v, err = attendanceIntegerSection(doc, "event index", 1)
	if err != nil {
		return nil, err
	}
	if v[0] != 288 {
		return nil, fmt.Errorf("升级大挑战奖励活动号不是288")
	}
	for _, field := range []struct {
		key string
		dst *string
	}{
		{"main reward mail sender", &c.MainSender}, {"main reward mail message", &c.MainMessage},
		{"bonus reward mail sender", &c.BonusSender}, {"bonus reward mail message", &c.BonusMessage},
	} {
		*field.dst, err = readString(doc, field.key)
		if err != nil {
			return nil, err
		}
	}
	var all []AttendancePVFItemReward
	for _, group := range []struct {
		key   string
		dst   *[]LegacyLevelReward
		count int
	}{{"main reward", &c.Main, 10}, {"bonus reward", &c.Bonus, 21}} {
		t, ok := doc.Section(group.key)
		// 当前原生XUI固定十个阶段及二十一个礼包控件，修改奖励内容不能破坏布局契约。
		if !ok || len(t) != group.count*3 {
			return nil, fmt.Errorf("升级大挑战[%s]奖励数量无效", group.key)
		}
		var previous int64
		for n := 0; n < len(t); n += 3 {
			level, id, count := t[n], t[n+1], t[n+2]
			if level.Kind != pvf.TokenInt || id.Kind != pvf.TokenInt || count.Kind != pvf.TokenInt || level.Int <= previous || level.Int > 90 || id.Int <= 0 || id.Int > 0xffffffff || count.Int < 1 || count.Int > 0x7fffffff {
				return nil, fmt.Errorf("升级大挑战[%s]第%d项无效", group.key, n/3)
			}
			r := AttendancePVFItemReward{ItemID: id.Int, Count: count.Int}
			*group.dst = append(*group.dst, LegacyLevelReward{Level: uint32(level.Int), Item: r})
			all = append(all, r)
			previous = level.Int
		}
	}
	var mainCount int64
	for _, row := range c.Main {
		mainCount += row.Item.Count
	}
	if mainCount > 0x7fffffff {
		return nil, fmt.Errorf("升级大挑战满级主奖励显示数溢出")
	}
	paths, err := resolveAttendanceRewardDefinitions(ctx, source, all)
	if err != nil {
		return nil, err
	}
	for _, rows := range [][]LegacyLevelReward{c.Main, c.Bonus} {
		for i := range rows {
			rows[i].Item.DefinitionPath = paths[rows[i].Item.ItemID]
		}
	}
	return c, nil
}

type LevelChallengeProgress struct {
	Main  map[uint32]bool `json:"main"`
	Bonus map[uint32]bool `json:"bonus"`
}
type LevelChallengeState map[string]LevelChallengeProgress

func ReadLevelChallengeState(account repo.AccountRecord) (LevelChallengeState, error) {
	state := LevelChallengeState{}
	if raw := account.Metadata[LevelChallengeMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return nil, err
		}
		if state == nil || len(state) > 65535 {
			return nil, fmt.Errorf("升级大挑战账号记录无效")
		}
		for id, p := range state {
			n, err := strconv.ParseUint(id, 10, 32)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("升级大挑战角色编号无效")
			}
			for _, rows := range []map[uint32]bool{p.Main, p.Bonus} {
				for level, claimed := range rows {
					if level < 1 || level > 90 || !claimed {
						return nil, fmt.Errorf("升级大挑战发奖回执无效")
					}
				}
			}
		}
	}
	return state, nil
}

// 在创建角色的既有事务内执行，账号名额与角色/初始资产一起提交或回滚。
// 已删除角色保留名额，防止反复建删刷取奖励。
func (c *LevelChallengeCatalog) Register(account *repo.AccountRecord, ch *repo.CharacterRecord) error {
	if c == nil || account == nil || ch == nil || account.AccountID != ch.AccountID || ch.Level != 1 || ch.Stats["pvp_private_character"] != 0 || !c.Window.ActiveAt(ch.CreatedAt) {
		return fmt.Errorf("升级大挑战创建资格无效")
	}
	job, err := strconv.Atoi(ch.Job)
	if err != nil || !jobmap.Valid(job) || (!strings.EqualFold(c.Job, "[all]") && !jobmap.MatchesQuestTag(c.Job, job)) {
		return fmt.Errorf("升级大挑战职业不符合配置")
	}
	id, err := strconv.ParseUint(ch.CharacterID, 10, 32)
	if err != nil || id == 0 {
		return fmt.Errorf("升级大挑战角色编号无效")
	}
	state, err := ReadLevelChallengeState(*account)
	if err != nil {
		return err
	}
	if _, exists := state[ch.CharacterID]; exists || (!c.AutomaticNewCharacters && len(state) >= c.Limit) {
		return fmt.Errorf("升级大挑战创建名额已用完")
	}
	state[ch.CharacterID] = LevelChallengeProgress{}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	account.Metadata[LevelChallengeMetadataKey] = string(raw)
	if ch.Stats == nil {
		ch.Stats = map[string]int64{}
	}
	ch.Stats[LevelChallengeRoleStat] = 1
	return nil
}

type LevelChallengeCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	Validate               func() bool
	Deliver                func(repo.MailboxRepository, AttendancePVFItemReward, string, bool) (string, error)
}
type LevelChallengeResult struct {
	Registered bool
	MailIDs    []string
}

func SettleLevelChallenge(ctx context.Context, repos repo.Group, c *LevelChallengeCatalog, cmd LevelChallengeCommand) (LevelChallengeResult, error) {
	if c == nil || cmd.AccountID == "" || cmd.CharacterID == "" || cmd.Now.IsZero() {
		return LevelChallengeResult{}, ErrOwnerUnavailable
	}
	var result LevelChallengeResult
	err := withinPortedEventAssets(ctx, repos, cmd.AccountID, cmd.CharacterID, func(accounts repo.AccountRepository, characters repo.CharacterRepository, _ repo.InventoryRepository, mailboxes repo.MailboxRepository) error {
		ch, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != cmd.AccountID || ch.Stats["delete_flag"] != 0 {
			return fmt.Errorf("升级大挑战角色归属无效")
		}
		account, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrOwnerUnavailable
		}
		state, err := ReadLevelChallengeState(account)
		if err != nil {
			return err
		}
		progress, registered := state[cmd.CharacterID]
		result.Registered = registered && ch.Stats[LevelChallengeRoleStat] == 1
		if !result.Registered || !c.Window.ActiveAt(cmd.Now) {
			return nil
		}
		if progress.Main == nil {
			progress.Main = map[uint32]bool{}
		}
		if progress.Bonus == nil {
			progress.Bonus = map[uint32]bool{}
		}
		for _, group := range []struct {
			rows     []LegacyLevelReward
			receipts map[uint32]bool
			bonus    bool
			name     string
		}{
			{c.Main, progress.Main, false, "main"}, {c.Bonus, progress.Bonus, true, "bonus"},
		} {
			for _, row := range group.rows {
				if int(row.Level) > ch.Level || group.receipts[row.Level] {
					continue
				}
				if cmd.Deliver == nil {
					return ErrOwnerUnavailable
				}
				key := fmt.Sprintf("%s:%s:%s:%d", LevelChallengeMetadataKey, ch.CharacterID, group.name, row.Level)
				id, err := cmd.Deliver(mailboxes, row.Item, key, group.bonus)
				if err != nil {
					return err
				}
				if id == "" {
					return fmt.Errorf("升级大挑战邮件回执为空")
				}
				group.receipts[row.Level] = true
				result.MailIDs = append(result.MailIDs, id)
			}
		}
		if len(result.MailIDs) > 0 {
			state[ch.CharacterID] = progress
			raw, err := json.Marshal(state)
			if err != nil {
				return err
			}
			return repo.SaveAccountMetadataEntry(ctx, accounts, account, LevelChallengeMetadataKey, string(raw), cmd.Now)
		}
		return nil
	}, cmd.Validate)
	if err != nil {
		return LevelChallengeResult{}, err
	}
	return result, nil
}
