package onlineevent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 回归按实际离线时长计算，默认离线七天；资格有效期可在策略中调整。
const ReturneeOfflinePeriod = 7 * 24 * time.Hour
const ReturneeDuration = 30 * 24 * time.Hour

// 欢迎经验默认沿用参考端20%，可在活动策略调整；它不是PVF里的奖励比例。
const ReturneeWelcomeExperiencePercent int64 = 20
const ReturneeMetadataKey = "event_seria_returnee_v1"

// 仅由首次创建账号档案且没有历史角色的认证登录标记，首次进场景时原子消费。
const NewbieEligibleMetadataKey = "seria_newbie_eligibility_v1"
const NewbieDuration = 30 * 24 * time.Hour
const ReturneeQuestCountKey = "seria_returnee_quest_count"
const ReturneeDisjointCountKey = "seria_returnee_disjoint_count"
const ReturneeGiftCountKey = "seria_returnee_gift_count"
const ReturneeSkillCountKey = "seria_returnee_skill_count"

type ReturneeMission struct {
	ID             uint32
	Condition      string
	Required       uint32
	Reward         AttendancePVFItemReward
	ConditionValue int64
}

type ReturneeCatalog struct {
	Newbie              *ReturneeCatalog
	Missions            [10]ReturneeMission
	Attendance          [7]ReturneeMission
	ReturnDays          []uint32
	MailTitle, MailBody string
	// 当前PVF可以读取，但这些条件尚未接入权威计数；不能因此启用领取。
	PendingConditions []string
}

func LoadReturneeCatalog(ctx context.Context, source dnfpvf.Source) (*ReturneeCatalog, error) {
	c, err := loadWelcomeCatalogGroup(ctx, source, 1)
	if err != nil {
		return nil, err
	}
	c.Newbie, err = loadWelcomeCatalogGroup(ctx, source, 0)
	return c, err
}

func loadWelcomeCatalogGroup(ctx context.Context, source dnfpvf.Source, wantedGroup int64) (*ReturneeCatalog, error) {
	text, err := source.ReadText("etc/seriawelcome.etc")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("etc/seriawelcome.etc", text)
	if err != nil {
		return nil, err
	}
	c := &ReturneeCatalog{}
	definitions := map[uint32]ReturneeMission{}
	var m ReturneeMission
	inList, inGroups, inMission, inCondition := false, false, false, false
	group := int64(-1)
	var ids []uint32
	var attendanceIDs []uint32
	for _, section := range doc.Sections {
		v := doc.Tokens[section.Start:section.End]
		integer := func(i int) int64 {
			if i < len(v) && v[i].Kind == dnfpvf.TokenInt {
				return v[i].Int
			}
			return -1
		}
		switch section.Name {
		case "return day":
			for _, t := range v {
				if t.Kind != dnfpvf.TokenInt || t.Int < 0 || t.Int > 36500 {
					return nil, fmt.Errorf("回归离线分组配置无效")
				}
				c.ReturnDays = append(c.ReturnDays, uint32(t.Int))
			}
		case "attendance reward mission":
			if inGroups && group == wantedGroup {
				for _, t := range v {
					if t.Kind != dnfpvf.TokenInt || t.Int < 1 {
						return nil, fmt.Errorf("回归签到配置无效")
					}
					attendanceIDs = append(attendanceIDs, uint32(t.Int))
				}
			}
		case "mission list":
			if inGroups && group == wantedGroup {
				for _, t := range v {
					if t.Kind != dnfpvf.TokenInt || t.Int < 1 {
						return nil, fmt.Errorf("invalid returnee mission group")
					}
					ids = append(ids, uint32(t.Int))
				}
			} else if !inGroups {
				inList = true
			}
		case "/mission list":
			if !inGroups {
				inList = false
			}
		case "mission group":
			inGroups = true
		case "/mission group":
			inGroups = false
		case "group":
			group = integer(0)
		case "mission":
			if inList {
				m = ReturneeMission{}
				inMission = true
			}
		case "/mission":
			if inMission {
				definitions[m.ID] = m
				inMission = false
			}
			inCondition = false
		case "index":
			if inMission {
				m.ID = uint32(integer(0))
			}
		case "condition":
			if inMission {
				inCondition = true
			}
		case "/condition":
			inCondition = false
		case "condition value":
			if inMission && inCondition && len(v) == 1 && v[0].Kind == dnfpvf.TokenInt {
				m.ConditionValue = v[0].Int
			}
		case "reward item":
			if inMission && len(v) == 2 {
				m.Reward = AttendancePVFItemReward{ItemID: integer(0), Count: integer(1)}
			}
		case "reward mail":
			if len(v) == 2 {
				c.MailTitle, c.MailBody = v[0].Value, v[1].Value
			}
		default:
			if inCondition && section.Name != "condition value" && len(v) == 1 && integer(0) > 0 {
				m.Condition, m.Required = section.Name, uint32(integer(0))
			}
		}
	}
	if wantedGroup == 0 && len(ids) == 0 {
		return nil, nil // 历史回归专用配置可不含新手组。
	}
	if len(ids) != len(c.Missions) || c.MailTitle == "" {
		return nil, fmt.Errorf("returnee native layout requires ten missions and mail text")
	}
	for i, id := range ids {
		m, ok := definitions[id]
		if !ok || m.Required == 0 || m.Reward.ItemID <= 0 || m.Reward.Count <= 0 {
			return nil, fmt.Errorf("invalid returnee mission %d", id)
		}
		switch m.Condition {
		case "clear quest", "join guild", "learn skill", "proper dungeon clear", "disjoint item", "level up count", "present npc", "mission clear count", "move village", "use cube skill", "dimension dungeon clear", "level up", "grow job":
		default:
			return nil, fmt.Errorf("unsupported returnee condition %q", m.Condition)
		}
		if m.Condition == "level up" && (m.ConditionValue <= 0 || m.ConditionValue > 255 || m.Required != 1) || m.Condition == "grow job" && m.Required != 1 {
			return nil, fmt.Errorf("新手等级/转职条件无效: %d", id)
		}
		c.Missions[i] = m
	}
	seen := map[uint32]bool{}
	for _, m := range c.Missions {
		if seen[m.ID] {
			return nil, fmt.Errorf("回归任务编号重复")
		}
		seen[m.ID] = true
	}
	if len(attendanceIDs) != 0 && len(attendanceIDs) != len(c.Attendance) {
		return nil, fmt.Errorf("回归原生签到需要七项配置")
	}
	for i, id := range attendanceIDs {
		m, ok := definitions[id]
		if !ok || seen[id] || m.Condition != "attendance count" || m.Required != 1 || m.Reward.ItemID <= 0 || m.Reward.Count <= 0 {
			return nil, fmt.Errorf("回归签到任务%d配置无效", id)
		}
		seen[id] = true
		c.Attendance[i] = m
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

type ReturneeObservation struct {
	Grown                                           bool
	Level, Quests, Clears, Disjoint, Gifts, Learned int64
	CubeSkills, Dimensions                          int64
	Villages                                        map[int64]int64
	Guild                                           bool
}

type ReturneeState struct {
	Newbie            bool                           `json:"newbie,omitempty"`
	LastSeen          time.Time                      `json:"last_seen"`
	Started           time.Time                      `json:"started,omitempty"`
	ExpiresAt         time.Time                      `json:"expires_at,omitempty"`
	Guide             uint64                         `json:"guide,omitempty"`
	GuideExtra        [64]byte                       `json:"guide_extra,omitempty"`
	Progress          [10]uint32                     `json:"progress"`
	Claimed           uint16                         `json:"claimed"`
	UserType          uint16                         `json:"user_type,omitempty"`
	AttendanceDays    []string                       `json:"attendance_days,omitempty"`
	AttendanceClaimed uint8                          `json:"attendance_claimed,omitempty"`
	Observed          map[string]ReturneeObservation `json:"observed,omitempty"`
}

func (s ReturneeState) ActiveAt(now time.Time) bool {
	// Mission claims do not shorten the owner's 30-day welcome benefit.
	if s.Started.IsZero() {
		return false
	}
	expiresAt := s.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = s.Started.Add(ReturneeDuration)
	}
	if now.IsZero() {
		now = time.Now()
	}
	return !now.Before(s.Started) && now.Before(expiresAt)
}

func (s ReturneeState) Active() bool { return s.ActiveAt(time.Now()) }

func LoadReturneeState(a dnfrepo.AccountRecord) (ReturneeState, error) {
	var s ReturneeState
	if raw := a.Metadata[ReturneeMetadataKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return s, fmt.Errorf("invalid returnee state: %w", err)
		}
	}
	if s.Claimed > 0x3ff {
		return s, fmt.Errorf("invalid returnee claim mask")
	}
	if s.AttendanceClaimed > 0x7f || len(s.AttendanceDays) > 7 || s.Guide>>46 != 0 || s.UserType > 5 {
		return s, fmt.Errorf("回归状态位或签到记录无效")
	}
	if s.Newbie && s.UserType != 0 {
		return s, fmt.Errorf("新手状态与原生用户类型不一致")
	}
	seenDays := map[string]bool{}
	for _, day := range s.AttendanceDays {
		if _, err := time.Parse("2006-01-02", day); err != nil || seenDays[day] {
			return s, fmt.Errorf("回归签到日期无效")
		}
		seenDays[day] = true
	}
	if !s.Started.IsZero() {
		if s.ExpiresAt.IsZero() {
			s.ExpiresAt = s.Started.Add(ReturneeDuration)
		} else if s.ExpiresAt.Before(s.Started) {
			return s, fmt.Errorf("invalid returnee expiration")
		}
	}
	return s, nil
}

// Observe uses durable gameplay counters; client progress reports
// cannot complete a mission. Each account/character snapshot is consumed once.
func (s *ReturneeState) Observe(c *ReturneeCatalog, characterID string, o ReturneeObservation, now time.Time, login bool) {
	s.ObserveWithPolicy(c, characterID, o, now, login, DefaultPortedActivityPolicy())
}

func (s *ReturneeState) ObserveWithPolicy(c *ReturneeCatalog, characterID string, o ReturneeObservation, now time.Time, login bool, policy PortedActivityPolicy) {
	if c == nil || len(c.PendingConditions) != 0 {
		return
	}
	if login && !s.ActiveAt(now) && !s.LastSeen.IsZero() && now.Sub(s.LastSeen) >= time.Duration(policy.ReturneeOfflineDays)*24*time.Hour {
		kind := uint16(5)
		for i, days := range c.ReturnDays {
			if i < 5 && now.Sub(s.LastSeen) >= time.Duration(days)*24*time.Hour {
				kind = uint16(i + 1)
				break
			}
		}
		*s = ReturneeState{Started: now, ExpiresAt: now.Add(time.Duration(policy.ReturneeDurationDays) * 24 * time.Hour), LastSeen: s.LastSeen, Guide: s.Guide, GuideExtra: s.GuideExtra, UserType: kind}
	}
	if now.After(s.LastSeen) {
		s.LastSeen = now
	}
	if !s.ActiveAt(now) {
		return
	}
	if c.Attendance[0].ID != 0 && len(s.AttendanceDays) < len(c.Attendance) {
		day := now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Add(-6 * time.Hour).Format("2006-01-02")
		known := false
		for _, d := range s.AttendanceDays {
			known = known || d == day
		}
		if !known {
			s.AttendanceDays = append(s.AttendanceDays, day)
		}
	}
	if s.Observed == nil {
		s.Observed = map[string]ReturneeObservation{}
	}
	old, known := s.Observed[characterID]
	if !known {
		old = o
	}
	for i, m := range c.Missions {
		var n int64
		switch m.Condition {
		case "level up":
			if o.Level >= m.ConditionValue {
				n = int64(m.Required)
			}
		case "grow job":
			if o.Grown {
				n = int64(m.Required)
			}
		case "clear quest":
			n = o.Quests - old.Quests
		case "proper dungeon clear":
			n = o.Clears - old.Clears
		case "disjoint item":
			n = o.Disjoint - old.Disjoint
		case "present npc":
			n = o.Gifts - old.Gifts
		case "level up count":
			n = o.Level - old.Level
		case "learn skill":
			n = o.Learned - old.Learned
		case "use cube skill":
			n = o.CubeSkills - old.CubeSkills
		case "dimension dungeon clear":
			n = o.Dimensions - old.Dimensions
		case "move village":
			for town, at := range o.Villages {
				if (m.ConditionValue == -1 || m.ConditionValue == town) && at >= s.Started.UnixNano() && at <= now.UnixNano() {
					n = int64(m.Required)
					break
				}
			}
		case "join guild":
			if o.Guild {
				n = int64(m.Required)
			}
		}
		s.Progress[i] = uint32(min(int64(m.Required), int64(s.Progress[i])+max(n, 0)))
	}
	var completed uint32
	for i, m := range c.Missions {
		if m.Condition != "mission clear count" && s.Progress[i] >= m.Required {
			completed++
		}
	}
	for i, m := range c.Missions {
		if m.Condition == "mission clear count" {
			s.Progress[i] = min(completed, m.Required)
		}
	}
	s.Observed[characterID] = o
}

type ReturneeCommand struct {
	AccountID, CharacterID string
	Now                    time.Time
	Login                  bool
	Claim                  uint32
	Guide                  *uint32
	GuideExtra             *uint32
	Policy                 *PortedActivityPolicy
	Validate               func() bool
	Observation            ReturneeObservation
	Deliver                func(dnfrepo.MailboxRepository, AttendancePVFItemReward, string) (string, error)
}

func SettleReturnee(ctx context.Context, repos dnfrepo.Group, c *ReturneeCatalog, cmd ReturneeCommand) (ReturneeState, []string, error) {
	var state ReturneeState
	var mails []string
	if c != nil && len(c.PendingConditions) != 0 {
		return state, nil, fmt.Errorf("%w: 回归任务条件待接入%v", ErrOwnerUnavailable, c.PendingConditions)
	}
	if c == nil || repos.MailboxAssets == nil || cmd.Now.IsZero() || cmd.AccountID == "" || cmd.CharacterID == "" {
		return state, nil, ErrOwnerUnavailable
	}
	err := withinPortedEventAssets(ctx, repos, cmd.AccountID, cmd.CharacterID, func(accounts dnfrepo.AccountRepository, chars dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, mailbox dnfrepo.MailboxRepository) error {
		ch, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != cmd.AccountID {
			return fmt.Errorf("returnee character ownership mismatch")
		}
		a, found, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("returnee account missing")
		}
		state, err = LoadReturneeState(a)
		if err != nil {
			return err
		}
		c := c // 事务回调的目录选择保持局部，重试不能沿用上一轮的子目录。
		root := c
		if cmd.Login && state.Started.IsZero() && a.Metadata[NewbieEligibleMetadataKey] == "pending" {
			if root.Newbie == nil {
				return fmt.Errorf("%w: 新手组配置缺失", ErrOwnerUnavailable)
			}
			state = ReturneeState{Newbie: true, Started: cmd.Now, ExpiresAt: cmd.Now.Add(NewbieDuration), LastSeen: state.LastSeen, Guide: state.Guide, GuideExtra: state.GuideExtra}
			a = dnfrepo.CloneAccount(a)
			a.Metadata[NewbieEligibleMetadataKey] = "started:" + cmd.Now.UTC().Format(time.RFC3339Nano)
			if err := dnfrepo.SaveAccountMetadataEntry(ctx, accounts, a, NewbieEligibleMetadataKey, a.Metadata[NewbieEligibleMetadataKey], cmd.Now); err != nil {
				return err
			}
		}
		// 一个原生页面同一时期只绑定一组；过期新手再次满足离线规则时仍可转入回归组。
		if state.Newbie && state.ActiveAt(cmd.Now) {
			c = root.Newbie
			if c == nil {
				return fmt.Errorf("%w: 有效新手缺少配置", ErrOwnerUnavailable)
			}
		}
		o := cmd.Observation
		o.Grown = ch.Stats["grow_type"] > 0 && ch.Stats["grow_type"]&15 != 0
		o.Level, o.Quests, o.Disjoint, o.Gifts = int64(ch.Level), ch.Stats[ReturneeQuestCountKey], ch.Stats[ReturneeDisjointCountKey], ch.Stats[ReturneeGiftCountKey]
		o.Clears = ch.Stats["adventure_recommended_dungeon_clears"]
		o.Learned = ch.Stats[ReturneeSkillCountKey]
		o.CubeSkills, o.Dimensions = ch.Stats[dnfrepo.ReturneeCubeSkillCountKey], ch.Stats[dnfrepo.ReturneeDimensionCountKey]
		o.Villages = map[int64]int64{}
		for key, at := range ch.Stats {
			if strings.HasPrefix(key, dnfrepo.ReturneeVillagePrefix) {
				town, err := strconv.ParseInt(strings.TrimPrefix(key, dnfrepo.ReturneeVillagePrefix), 10, 64)
				if err == nil && town > 0 {
					o.Villages[town] = at
				}
			}
		}
		policy := DefaultPortedActivityPolicy()
		if cmd.Policy != nil {
			policy = *cmd.Policy
		}
		if err := policy.Validate(); err != nil {
			return err
		}
		state.ObserveWithPolicy(c, cmd.CharacterID, o, cmd.Now, cmd.Login, policy)
		if cmd.Guide != nil {
			if *cmd.Guide >= 46 || !state.ActiveAt(cmd.Now) {
				return fmt.Errorf("回归引导编号或资格无效")
			}
			state.Guide |= uint64(1) << *cmd.Guide
		}
		if cmd.GuideExtra != nil {
			if *cmd.GuideExtra >= 512 || !state.ActiveAt(cmd.Now) {
				return fmt.Errorf("回归扩展引导编号或资格无效")
			}
			state.GuideExtra[*cmd.GuideExtra/8] |= byte(1 << (*cmd.GuideExtra % 8))
		}
		if cmd.Claim != 0 {
			if !state.ActiveAt(cmd.Now) {
				return fmt.Errorf("returnee period expired or inactive")
			}
			index := -1
			attendance := false
			var reward AttendancePVFItemReward
			for i, m := range c.Missions {
				if m.ID == cmd.Claim {
					index = i
					reward = m.Reward
				}
			}
			for i, m := range c.Attendance {
				if m.ID != 0 && m.ID == cmd.Claim {
					index, attendance, reward = i, true, m.Reward
				}
			}
			if index < 0 || state.Started.IsZero() ||
				(!attendance && state.Progress[index] < c.Missions[index].Required) ||
				(attendance && len(state.AttendanceDays) <= index) {
				return fmt.Errorf("returnee mission not complete")
			}
			claimed := !attendance && state.Claimed&(1<<index) != 0 || attendance && state.AttendanceClaimed&(1<<index) != 0
			if !claimed {
				if cmd.Deliver == nil {
					return ErrOwnerUnavailable
				}
				key := fmt.Sprintf("%s:%d:%d", ReturneeMetadataKey, state.Started.UnixNano(), cmd.Claim)
				id, err := cmd.Deliver(mailbox, reward, key)
				if err != nil {
					return err
				}
				if id == "" {
					return fmt.Errorf("returnee reward mail missing")
				}
				mails = append(mails, id)
				if attendance {
					state.AttendanceClaimed |= 1 << index
				} else {
					state.Claimed |= 1 << index
				}
			}
		}
		wantFlag := int64(0)
		if state.ActiveAt(cmd.Now) {
			wantFlag = 1
		}
		wantUntil := int64(0)
		if wantFlag != 0 {
			wantUntil = state.ExpiresAt.Unix()
		}
		returnFlag, returnUntil, newbieUntil := wantFlag, wantUntil, int64(0)
		if state.Newbie {
			returnFlag, returnUntil, newbieUntil = 0, 0, wantUntil
		}
		if ch.Stats["is_return_user"] != returnFlag || ch.Stats["seria_returnee_active_until"] != returnUntil || ch.Stats["seria_newbie_active_until"] != newbieUntil {
			ch = dnfrepo.CloneCharacter(ch)
			if ch.Stats == nil {
				ch.Stats = map[string]int64{}
			}
			ch.Stats["is_return_user"] = returnFlag
			ch.Stats["seria_returnee_active_until"] = returnUntil
			ch.Stats["seria_newbie_active_until"] = newbieUntil
			ch.UpdatedAt = cmd.Now
			if err := dnfrepo.SaveCharacterFields(ctx, chars, ch, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return dnfrepo.SaveAccountMetadataEntry(ctx, accounts, a, ReturneeMetadataKey, string(raw), cmd.Now)
	}, cmd.Validate)
	if err != nil {
		return ReturneeState{}, nil, err
	}
	return state, mails, nil
}
