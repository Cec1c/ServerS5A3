package tagtournament

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

const rosterKey = "tag_tournament_roster_v1"
const dayKey = "tag_tournament_day_v1"

// Owner复用已有账号、多角色、邮箱原子事务能力，不创建寄售记录或修改库存。
// 入场及结算不能退回多次独立Save模拟，缺少事务能力时直接拒绝。
type Owner struct {
	catalog      *Catalog
	tx           repo.AccountTradeMailboxUnitOfWork
	repositories repo.Group
	now          func() time.Time
	roll         func() (byte, error)
}

func NewOwner(c *Catalog, repositories repo.Group) (*Owner, error) {
	return NewOwnerWithClock(c, repositories, time.Now)
}

// 桥接回合与持久化结算使用同一服务时钟，便于在06点边界统一判定。
func NewOwnerWithClock(c *Catalog, repositories repo.Group, now func() time.Time) (*Owner, error) {
	tx, ok := repositories.MailboxAssets.(repo.AccountTradeMailboxUnitOfWork)
	if c == nil || c.config.Fingerprint == "" || !ok || tx == nil || repositories.Account == nil || repositories.Character == nil || now == nil {
		return nil, fmt.Errorf("帝国竞技场缺少已验证资源或账号双角色邮箱事务")
	}
	return &Owner{catalog: c, tx: tx, repositories: repositories, now: now, roll: func() (byte, error) {
		n, e := rand.Int(rand.Reader, big.NewInt(100))
		if e != nil {
			return 0, e
		}
		return byte(n.Int64()), nil
	}}, nil
}

// LobbySnapshot是账号已有状态的只读投影，不注册队伍、不刷新挑战凭据、不扣费。
// 角色在实际注册及入场事务中仍会重新校验，客户端不能用此快照授权结算。
type LobbySnapshot struct {
	Registered bool
	Characters [2]repo.CharacterRecord
	Day        DayState
}

func (o *Owner) Lobby(ctx context.Context, accountID string) (LobbySnapshot, error) {
	if o == nil || accountID == "" || accountID != strings.TrimSpace(accountID) {
		return LobbySnapshot{}, fmt.Errorf("帝国竞技场休息室缺少账号身份")
	}
	account, found, err := o.repositories.Account.Load(ctx, accountID)
	if err != nil {
		return LobbySnapshot{}, err
	}
	if !found || account.AccountID != accountID {
		return LobbySnapshot{}, fmt.Errorf("帝国竞技场休息室账号不存在")
	}
	day, err := o.loadDay(account, o.now())
	if err != nil {
		return LobbySnapshot{}, err
	}
	view := LobbySnapshot{Day: cloneDay(day)}
	raw, exists := account.Metadata[rosterKey]
	if !exists {
		return view, nil
	}
	ids, err := decodeRosterMetadata(raw)
	if err != nil {
		return LobbySnapshot{}, err
	}
	roster, err := loadOwned(ctx, o.repositories.Character, accountID, ids)
	if err != nil {
		return LobbySnapshot{}, err
	}
	for _, ch := range roster {
		if ch.Level < int(o.catalog.dungeons[0].MinimumLevel) {
			return LobbySnapshot{}, fmt.Errorf("帝国竞技场已注册队员等级不足")
		}
	}
	view.Registered, view.Characters = true, roster
	return view, nil
}

// Admission的凭据、名单及角色快照不可由客户端构造。提交成功后才交给桥接层。
// 尚未接通协议的桥接层不得调用Admit扣费后再尝试猜测双角色正文。
type Admission struct {
	catalog                     *Catalog
	accountID, recipient, token string
	characters                  [2]repo.CharacterRecord
	plan                        DailyPlan
	fee                         uint32
	created                     time.Time
}

func (a Admission) Fee() uint32 { return a.fee }
func (a Admission) Characters() [2]repo.CharacterRecord {
	return [2]repo.CharacterRecord{repo.CloneCharacter(a.characters[0]), repo.CloneCharacter(a.characters[1])}
}
func (a Admission) Plan() DailyPlan {
	p := a.plan
	p.Encounters = append([]DailyEncounter(nil), p.Encounters...)
	return p
}

func validRoster(ids [2]string, current string) bool {
	return ids[0] != "" && ids[1] != "" && ids[0] != ids[1] && ids[0] == strings.TrimSpace(ids[0]) && ids[1] == strings.TrimSpace(ids[1]) && (current == ids[0] || current == ids[1])
}

func loadOwned(ctx context.Context, chars repo.CharacterRepository, account string, ids [2]string) ([2]repo.CharacterRecord, error) {
	var result [2]repo.CharacterRecord
	for i, id := range ids {
		v, found, err := chars.Load(ctx, id)
		if err != nil {
			return result, err
		}
		if !found || v.CharacterID != id || v.AccountID != account || v.Slot < 0 || v.Stats["delete_flag"] != 0 || v.Stats["delete_time"] != 0 {
			return result, fmt.Errorf("帝国竞技场队员不存在、已删除或不属当前账号")
		}
		result[i] = repo.CloneCharacter(v)
	}
	return result, nil
}

// decodeMetadata拒绝空结构、截断、未知字段及尾随对象，损坏账本不会变成免费入场。
func decodeMetadata(raw string, dst any) error {
	if len(raw) == 0 || len(raw) > 16384 {
		return fmt.Errorf("帝国竞技场账号账本长度无效")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("帝国竞技场账号账本解析失败: %w", err)
	}
	var tail any
	if err := d.Decode(&tail); err != io.EOF {
		return fmt.Errorf("帝国竞技场账号账本存在尾随内容")
	}
	return nil
}

func decodeRosterMetadata(raw string) ([2]string, error) {
	var rows []string
	if err := decodeMetadata(raw, &rows); err != nil {
		return [2]string{}, err
	}
	if len(rows) != 2 {
		return [2]string{}, fmt.Errorf("帝国竞技场注册名单必须恰好两名队员")
	}
	ids := [2]string{rows[0], rows[1]}
	if !validRoster(ids, ids[0]) {
		return [2]string{}, fmt.Errorf("帝国竞技场注册名单无效")
	}
	return ids, nil
}

func (o *Owner) loadDay(account repo.AccountRecord, now time.Time) (DayState, error) {
	var state DayState
	if raw, exists := account.Metadata[dayKey]; exists {
		if err := decodeMetadata(raw, &state); err != nil {
			return DayState{}, err
		}
		if state.AccountID != account.AccountID || state.ServiceDay <= 0 || !state.Paid || state.Mode != Normal && state.Mode != Hard || state.ConfigHash == "" || state.HardCleared < 0 || state.HardCleared > len(o.catalog.config.HardPool.Rows) || len(state.NormalClaims) > o.catalog.config.NormalPool.Take+o.catalog.config.SpecialPool.Take {
			return DayState{}, fmt.Errorf("帝国竞技场账号日账本不完整")
		}
		if state.Mode == Normal && state.HardCleared != 0 || state.Mode == Hard && len(state.NormalClaims) != 0 {
			return DayState{}, fmt.Errorf("帝国竞技场奖励账本模式不符")
		}
		if state.Mode == Normal && state.ConfigHash == o.catalog.config.Fingerprint && state.ServiceDay == premium.ServiceDayID(now) {
			plan, err := o.catalog.DailyPlan(now, Normal)
			if err != nil {
				return DayState{}, err
			}
			allowed := make(map[uint32]bool, len(plan.Encounters))
			for _, encounter := range plan.Encounters {
				allowed[encounter.MonsterID] = true
			}
			for monster, claimed := range state.NormalClaims {
				if !claimed || !allowed[monster] {
					return DayState{}, fmt.Errorf("帝国竞技场奖励账本含非当日怪物")
				}
			}
		}
		if state.ActiveRun != "" {
			b, err := hex.DecodeString(state.ActiveRun)
			if err != nil || len(b) != 16 || !validRoster(state.ActiveRoster, state.ActiveRoster[0]) {
				return DayState{}, fmt.Errorf("帝国竞技场挑战凭据损坏")
			}
		} else if state.ActiveRoster != [2]string{} {
			return DayState{}, fmt.Errorf("帝国竞技场挑战名单缺少凭据")
		}
	}
	if state.ServiceDay > premium.ServiceDayID(now) {
		return DayState{}, fmt.Errorf("帝国竞技场日账本时间超前")
	}
	return state, nil
}

func saveMetadata(ctx context.Context, accounts repo.AccountRepository, account repo.AccountRecord, key string, value any, now time.Time) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return repo.SaveAccountMetadataEntry(ctx, accounts, account, key, string(b), now)
}

// RegisterRoster只注册同账号、达到PVF最低等级的两名角色。桥接层须先确认处于休息室。
// 重新注册会作废旧挑战凭据，保留当天支付模式和已发奖励。
func (o *Owner) RegisterRoster(ctx context.Context, accountID, current string, ids [2]string) error {
	return o.registerRoster(ctx, accountID, current, ids, nil)
}

// RegisterRosterFromSnapshot绑定客户端已收到的角色表。角色删除、改名或SQL槽位
// 调整后，旧序号不能在检查与提交之间注册到另一份角色身份。
func (o *Owner) RegisterRosterFromSnapshot(ctx context.Context, accountID, current string, expected [2]repo.CharacterRecord) error {
	ids := [2]string{expected[0].CharacterID, expected[1].CharacterID}
	return o.registerRoster(ctx, accountID, current, ids, &expected)
}

func (o *Owner) registerRoster(ctx context.Context, accountID, current string, ids [2]string, expected *[2]repo.CharacterRecord) error {
	if o == nil || !validRoster(ids, current) {
		return fmt.Errorf("帝国竞技场需注册两名不同队员")
	}
	return o.tx.WithinAccountTradeMailbox(ctx, accountID, ids[:], current, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.MailboxRepository) error {
		now := o.now()
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("帝国竞技场账号不存在")
		}
		roster, err := loadOwned(ctx, chars, accountID, ids)
		if err != nil {
			return err
		}
		if expected != nil {
			for i, ch := range roster {
				old := expected[i]
				if old.AccountID != accountID || old.CharacterID != ch.CharacterID || old.Slot != ch.Slot || old.Name != ch.Name {
					return fmt.Errorf("帝国竞技场角色列表已变更，请重新获取后设置队员")
				}
			}
		}
		min := o.catalog.dungeons[0].MinimumLevel
		for _, ch := range roster {
			if ch.Level < int(min) {
				return fmt.Errorf("帝国竞技场队员等级不足")
			}
		}
		state, err := o.loadDay(account, now)
		if err != nil {
			return err
		}
		if state.ActiveRun != "" {
			state.ActiveRun = ""
			state.ActiveRoster = [2]string{}
			if err = saveMetadata(ctx, accounts, account, dayKey, state, now); err != nil {
				return err
			}
		}
		// 重新读取事务内账号，兼容只实现整行Save的仓储，不覆盖刚保存的日账本。
		account, _, err = accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		return saveMetadata(ctx, accounts, account, rosterKey, ids, now)
	})
}

// Admit在同一事务内锁定两名队员、验证注册名单、扣当前角色金币、提交日账本。
// 重入只换挑战凭据；06点后重新收费；旧会话不能在新挑战后继续结算。
func (o *Owner) Admit(ctx context.Context, accountID, current string, ids [2]string, mode Mode) (Admission, error) {
	return o.admit(ctx, accountID, current, ids, mode, nil, nil, false)
}

// AdmitFromSnapshot在扣费事务中重新比较已用于构造双角色的角色快照。
// 改名、转职、属性或金币变化都必须重新准备正文，不能扣费后投影旧资料。
func (o *Owner) AdmitFromSnapshot(ctx context.Context, accountID, current string, expected [2]repo.CharacterRecord, mode Mode) (Admission, error) {
	expected = [2]repo.CharacterRecord{repo.CloneCharacter(expected[0]), repo.CloneCharacter(expected[1])}
	return o.admit(ctx, accountID, current, [2]string{expected[0].CharacterID, expected[1].CharacterID}, mode, &expected, nil, false)
}

// AdmitPrepared在同一扣费事务中比较已冻结的日计划，准备期间跨06点即拒绝。
func (o *Owner) AdmitPrepared(ctx context.Context, accountID, current string, expected [2]repo.CharacterRecord, plan DailyPlan) (Admission, error) {
	expected = [2]repo.CharacterRecord{repo.CloneCharacter(expected[0]), repo.CloneCharacter(expected[1])}
	return o.admit(ctx, accountID, current, [2]string{expected[0].CharacterID, expected[1].CharacterID}, plan.Mode, &expected, &plan, false)
}

// ResumePrepared只恢复当天已经支付的同模式挑战。06点、名单及角色快照
// 在账号事务中再次核对，不能把重入请求转成一次新的收费购买。
func (o *Owner) ResumePrepared(ctx context.Context, accountID, current string, expected [2]repo.CharacterRecord, plan DailyPlan) (Admission, error) {
	expected = [2]repo.CharacterRecord{repo.CloneCharacter(expected[0]), repo.CloneCharacter(expected[1])}
	return o.admit(ctx, accountID, current, [2]string{expected[0].CharacterID, expected[1].CharacterID}, plan.Mode, &expected, &plan, true)
}

func (o *Owner) admit(ctx context.Context, accountID, current string, ids [2]string, mode Mode, expected *[2]repo.CharacterRecord, preparedPlan *DailyPlan, paidOnly bool) (Admission, error) {
	if o == nil || !validRoster(ids, current) {
		return Admission{}, fmt.Errorf("帝国竞技场入场名单无效")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Admission{}, err
	}
	var admission Admission
	err := o.tx.WithinAccountTradeMailbox(ctx, accountID, ids[:], current, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.MailboxRepository) error {
		now := o.now()
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("帝国竞技场账号不存在")
		}
		registered, err := decodeRosterMetadata(account.Metadata[rosterKey])
		if err != nil {
			return err
		}
		if registered != ids {
			return fmt.Errorf("帝国竞技场请求名单与账号注册不符")
		}
		roster, err := loadOwned(ctx, chars, accountID, ids)
		if err != nil {
			return err
		}
		if expected != nil && !reflect.DeepEqual(roster, *expected) {
			return fmt.Errorf("帝国竞技场双角色准备后资料已变化，请重新入场")
		}
		payer := 0
		if roster[1].CharacterID == current {
			payer = 1
		}
		gold := roster[payer].Stats["gold"]
		if gold < 0 {
			return fmt.Errorf("帝国竞技场角色金币账本无效")
		}
		state, err := o.loadDay(account, now)
		if err != nil {
			return err
		}
		if paidOnly && (!state.Paid || state.Mode != mode) {
			return fmt.Errorf("帝国竞技场重入资格未支付、已过期或模式不符")
		}
		quote, err := o.catalog.QuoteEntry(now, mode, accountID, roster, uint64(gold), state)
		if err != nil {
			return err
		}
		if preparedPlan != nil && !reflect.DeepEqual(quote.Plan, *preparedPlan) {
			return fmt.Errorf("帝国竞技场准备期间每日计划已变化，请重新挑战")
		}
		if paidOnly && quote.Fee != 0 {
			return fmt.Errorf("帝国竞技场重入不能产生新的入场费用")
		}
		quote.Next.ActiveRun, quote.Next.ActiveRoster = hex.EncodeToString(token[:]), ids
		if quote.Fee > 0 {
			roster[payer].Stats["gold"] -= int64(quote.Fee)
			roster[payer].UpdatedAt = now
			if err = repo.SaveCharacterFields(ctx, chars, roster[payer], repo.CharacterFieldStats); err != nil {
				return err
			}
		}
		if err = saveMetadata(ctx, accounts, account, dayKey, quote.Next, now); err != nil {
			return err
		}
		admission = Admission{o.catalog, accountID, current, quote.Next.ActiveRun, roster, quote.Plan, quote.Fee, now}
		return nil
	})
	if err != nil {
		return Admission{}, err
	}
	return admission, nil
}

type Settlement struct {
	MailIDs []string
	Rewards []RewardRow
}

// Close只终止挑战，不能增加击杀进度。跨06点时仍按原凭据、原日账本结算
// 已确认的回合，邮件使用实际提交时间；新日/新挑战已替换凭据则拒绝旧挑战。
func (o *Owner) Close(ctx context.Context, r *Run) (Settlement, error) {
	if o == nil || r == nil {
		return Settlement{}, fmt.Errorf("帝国竞技场缺少待结束挑战")
	}
	r.mu.Lock()
	now := o.now()
	if r.catalog != o.catalog || now.IsZero() || now.Before(r.lastTime) {
		r.mu.Unlock()
		return Settlement{}, fmt.Errorf("帝国竞技场结束挑战归属或时钟无效")
	}
	if r.usable() {
		r.phase = Abandoned
	}
	r.closing, r.lastTime = true, now
	cleared := r.cleared
	r.mu.Unlock()
	if cleared == 0 {
		return Settlement{}, nil
	}
	return o.Settle(ctx, r)
}

// ValidateRun在切人、回合等操作前核对账号当前凭据。另一连接重新入场或
// 重新注册队伍后，旧连接不能继续操纵已作废挑战；本方法不写入账本。
func (o *Owner) ValidateRun(ctx context.Context, r *Run) error {
	if o == nil || r == nil {
		return fmt.Errorf("帝国竞技场缺少待核对挑战")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.catalog != o.catalog {
		return fmt.Errorf("帝国竞技场挑战资源归属不符")
	}
	now := o.now()
	if err := r.checkTime(now); err != nil {
		return err
	}
	a := r.admission
	account, found, err := o.repositories.Account.Load(ctx, a.accountID)
	if err != nil {
		return err
	}
	if !found || account.AccountID != a.accountID {
		return fmt.Errorf("帝国竞技场挑战账号已失效")
	}
	day, err := o.loadDay(account, now)
	if err != nil {
		return err
	}
	ids := [2]string{a.characters[0].CharacterID, a.characters[1].CharacterID}
	if day.ServiceDay != a.plan.ServiceDay || day.Mode != a.plan.Mode || day.ConfigHash != a.plan.ConfigHash || day.ActiveRun != a.token || day.ActiveRoster != ids {
		return fmt.Errorf("帝国竞技场挑战凭据已被新的入场或组队设置替换")
	}
	return nil
}

// Settle只接受本Owner已提交的Run及其权威通关进度，不能传入客户端奖励/怪物/回合。
// 普通按怪物账号每日一次；困难按当日最高通关总额补差，不重复累计整列表。
// 邮件、奖励回执与账号日账本由同一个仓储事务提交，失败可原样重试。
func (o *Owner) Settle(ctx context.Context, r *Run) (Settlement, error) {
	if o == nil || r == nil {
		return Settlement{}, fmt.Errorf("帝国竞技场结算缺少挑战")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.catalog != o.catalog || r.cleared == 0 {
		return Settlement{}, fmt.Errorf("帝国竞技场没有已确认的通关进度")
	}
	if aMode := r.admission.plan.Mode; aMode == Hard && r.phase != BetweenRounds && r.phase != Finished && r.phase != Defeated && r.phase != Abandoned {
		return Settlement{}, fmt.Errorf("帝国竞技场困难奖励需有已确认的阶段结算边界")
	}
	a := r.admission
	ids := [2]string{a.characters[0].CharacterID, a.characters[1].CharacterID}
	var result Settlement
	err := o.tx.WithinAccountTradeMailbox(ctx, a.accountID, ids[:], a.recipient, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, mailboxes repo.MailboxRepository) error {
		now := o.now()
		if err := r.checkTime(now); err != nil && (!r.closing || now.Before(r.lastTime) || premium.ServiceDayID(now) < a.plan.ServiceDay) {
			return err
		}
		account, found, err := accounts.Load(ctx, a.accountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("帝国竞技场结算账号不存在")
		}
		roster, err := loadOwned(ctx, chars, a.accountID, ids)
		if err != nil {
			return err
		}
		state, err := o.loadDay(account, now)
		if err != nil {
			return err
		}
		if state.ServiceDay != a.plan.ServiceDay || state.Mode != a.plan.Mode || state.ConfigHash != a.plan.ConfigHash || state.ActiveRun != a.token || state.ActiveRoster != ids {
			return fmt.Errorf("帝国竞技场结算凭据已过期或被新挑战替换")
		}
		deliver := func(label, title, body string, rewards []RewardRow) error {
			if len(rewards) == 0 {
				return nil
			}
			name := roster[0].Name
			if roster[1].CharacterID == a.recipient {
				name = roster[1].Name
			}
			attachments := make([]repo.MailAttachment, 0, len(rewards))
			for _, row := range rewards {
				if row.Count > 0 {
					attachments = append(attachments, repo.MailAttachment{ItemID: int64(row.ItemID), Count: int64(row.Count), Bind: true})
				}
			}
			key := fmt.Sprintf("tag/%s/%d/%s/%d/%s", a.accountID, state.ServiceDay, state.ConfigHash, state.Mode, label)
			mailID, err := repo.AppendSystemMail(ctx, mailboxes, repo.SystemMailDelivery{RecipientCharacterID: a.recipient, RecipientName: name, Title: title, Body: body, Source: "tag_tournament", CompletionKey: key, Attachments: attachments, CreatedAt: now})
			if err != nil {
				return err
			}
			result.MailIDs = append(result.MailIDs, mailID)
			result.Rewards = append(result.Rewards, rewards...)
			return nil
		}
		if state.Mode == Normal {
			for round, encounter := range a.plan.Encounters[:r.cleared] {
				if state.NormalClaims[encounter.MonsterID] {
					continue
				}
				table := o.catalog.config.NormalRewards
				if encounter.Optional {
					table = o.catalog.config.SpecialRewards
				}
				rolls := map[byte]byte{}
				for _, row := range table {
					if _, exists := rolls[row.Index]; !exists {
						roll, err := o.roll()
						if err != nil {
							return err
						}
						rolls[row.Index] = roll
					}
				}
				rewards, next, err := o.catalog.QuoteNormalReward(a.created, encounter.MonsterID, rolls, state)
				if err != nil {
					return err
				}
				title := fmt.Sprintf("帝国竞技场第%d回合奖励", round+1)
				body := fmt.Sprintf("您已通关帝国竞技场第%d回合，获得本回合奖励。\n请查收附件。", round+1)
				if encounter.Optional {
					title = "帝国竞技场竞技之王奖励"
					body = "您已击败帝国竞技场竞技之王，获得竞技之王通关奖励。\n请查收附件。"
				}
				if err = deliver(fmt.Sprintf("monster-%d", encounter.MonsterID), title, body, rewards); err != nil {
					return err
				}
				state = next
			}
		} else {
			if r.cleared <= state.HardCleared {
				return nil
			}
			rewards, err := o.catalog.HardRewardTotal(r.cleared)
			if err != nil {
				return err
			}
			prior := map[uint32]uint16{}
			if state.HardCleared > 0 {
				old, err := o.catalog.HardRewardTotal(state.HardCleared)
				if err != nil {
					return err
				}
				for _, v := range old {
					prior[v.ItemID] = v.Count
				}
			}
			var delta []RewardRow
			for _, v := range rewards {
				if v.Count < prior[v.ItemID] {
					return fmt.Errorf("帝国竞技场困难累计奖励回退")
				}
				v.Count -= prior[v.ItemID]
				if v.Count > 0 {
					delta = append(delta, v)
				}
			}
			body := fmt.Sprintf("您已通关帝国竞技场困难模式第%d回合，获得本回合奖励。\n请查收附件。", r.cleared)
			if err = deliver(fmt.Sprintf("hard-%d", r.cleared), fmt.Sprintf("帝国竞技场第%d回合奖励", r.cleared), body, delta); err != nil {
				return err
			}
			state.HardCleared = r.cleared
		}
		if len(result.MailIDs) == 0 {
			return nil
		}
		return saveMetadata(ctx, accounts, account, dayKey, state, now)
	})
	if err != nil {
		return Settlement{}, err
	}
	return result, nil
}
