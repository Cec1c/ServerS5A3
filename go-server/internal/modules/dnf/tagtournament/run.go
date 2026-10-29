package tagtournament

import (
	"fmt"
	"sync"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
)

type Phase string

const (
	Lobby         Phase = "休息室"
	Fighting      Phase = "战斗"
	BetweenRounds Phase = "回合结束"
	SpecialChoice Phase = "选择特殊关卡"
	Finished      Phase = "完成"
	Defeated      Phase = "全员死亡"
	Abandoned     Phase = "放弃"
)

type Vitals struct{ HP, MaxHP, MP, MaxMP int64 }

// Run仅由已提交的Admission创建。其进度由桥接层确认的地图/怪物死亡推进，
// 现用客户端计算伤害，专用死亡通知须先经过桥接层的会话、地图和当前队员校验；
// 切人或角色死亡不能代替地图Boss死亡与通关证明。
type Run struct {
	mu                sync.Mutex
	admission         Admission
	catalog           *Catalog
	phase             Phase
	active            int
	round, cleared    int
	vitals            [2]Vitals
	lastTime, nextTag time.Time
	startedAt         time.Time
	itemCooldown      [6]time.Time
	closing           bool
}

type RunSnapshot struct {
	Phase                  Phase
	Active, Round, Cleared int
	Vitals                 [2]Vitals
	NextTag                time.Time
}

func NewRun(a Admission, vitals [2]Vitals) (*Run, error) {
	if a.catalog == nil || a.token == "" || a.created.IsZero() || a.plan.ServiceDay != premium.ServiceDayID(a.created) {
		return nil, fmt.Errorf("帝国竞技场缺少已提交的入场凭据")
	}
	active := -1
	for i, ch := range a.characters {
		v := vitals[i]
		if v.MaxHP <= 0 || v.HP <= 0 || v.HP > v.MaxHP || v.MaxMP < 0 || v.MP < 0 || v.MP > v.MaxMP {
			return nil, fmt.Errorf("帝国竞技场角色生命/魔法快照无效")
		}
		if ch.CharacterID == a.recipient {
			active = i
		}
	}
	if active < 0 {
		return nil, fmt.Errorf("帝国竞技场当前角色不在已注册队伍")
	}
	return &Run{admission: a, catalog: a.catalog, phase: Lobby, active: active, round: -1, vitals: vitals, lastTime: a.created}, nil
}

func (r *Run) Snapshot() RunSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RunSnapshot{r.phase, r.active, r.round, r.cleared, r.vitals, r.nextTag}
}

func (r *Run) checkTime(now time.Time) error {
	if now.IsZero() || now.Before(r.lastTime) || premium.ServiceDayID(now) != r.admission.plan.ServiceDay {
		return fmt.Errorf("帝国竞技场战斗时钟回退或已跨06点服务日")
	}
	return nil
}

func (r *Run) usable() bool {
	return r.phase != Finished && r.phase != Defeated && r.phase != Abandoned
}

// BeginNext在地图加载前调用。普通第四关后必须明确选择特殊关，不能自动进王。
// restoreSkillCooldowns仅特殊关入口为true，描述原生确认执行的恢复动作。
func (r *Run) BeginNext(now time.Time, acceptSpecial bool) (encounter DailyEncounter, restoreSkillCooldowns bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginNextLocked(now, acceptSpecial)
}

// PreviewNext在发地图前验证同一时点的回合转换，不改变生命、冷却或进度。
// 返回的恢复标记描述原生特殊关确认动作；它不代表还需发送服务端恢复包。
func (r *Run) PreviewNext(now time.Time, acceptSpecial bool) (DailyEncounter, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := &Run{admission: r.admission, catalog: r.catalog, phase: r.phase, active: r.active, round: r.round, cleared: r.cleared, vitals: r.vitals, lastTime: r.lastTime, nextTag: r.nextTag, startedAt: r.startedAt, itemCooldown: r.itemCooldown}
	return copy.beginNextLocked(now, acceptSpecial)
}

func (r *Run) beginNextLocked(now time.Time, acceptSpecial bool) (encounter DailyEncounter, restoreSkillCooldowns bool, err error) {
	if err = r.checkTime(now); err != nil {
		return
	}
	if r.phase != Lobby && r.phase != BetweenRounds && r.phase != SpecialChoice {
		err = fmt.Errorf("帝国竞技场当前不能进入下一回合")
		return
	}
	if r.phase == SpecialChoice && !acceptSpecial {
		r.phase, r.lastTime = Finished, now
		return
	}
	if r.phase != SpecialChoice && acceptSpecial {
		err = fmt.Errorf("帝国竞技场尚未到特殊关选择阶段")
		return
	}
	next := r.round + 1
	if next >= len(r.admission.plan.Encounters) {
		err = fmt.Errorf("帝国竞技场回合超出日计划")
		return
	}
	encounter = r.admission.plan.Encounters[next]
	if encounter.Optional {
		for i := range r.vitals {
			if r.vitals[i].HP > 0 {
				r.vitals[i].HP, r.vitals[i].MP = r.vitals[i].MaxHP, r.vitals[i].MaxMP
			}
		}
		restoreSkillCooldowns = true
	}
	r.round, r.phase, r.lastTime = next, Fighting, now
	if next == 0 && r.startedAt.IsZero() {
		r.startedAt = now
	}
	return
}

// ConfirmBossDefeated必须在当前地图权威怪物集合确认本回合已完成后调用。
// 只接受预定地图与Boss，重复死亡与乱序地图均不能多推进一关。
func (r *Run) ConfirmBossDefeated(now time.Time, mapID, monsterID uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkTime(now); err != nil {
		return err
	}
	if r.phase != Fighting {
		return fmt.Errorf("帝国竞技场当前没有待结算战斗")
	}
	want := r.admission.plan.Encounters[r.round]
	if want.MapID != mapID || want.MonsterID != monsterID {
		return fmt.Errorf("帝国竞技场死亡对象与当前回合不符")
	}
	r.cleared, r.lastTime = r.round+1, now
	switch {
	case r.cleared == len(r.admission.plan.Encounters):
		r.phase = Finished
	case r.admission.plan.Mode == Normal && r.cleared == r.admission.plan.SpecialFrom:
		r.phase = SpecialChoice
	default:
		r.phase = BetweenRounds
	}
	return nil
}

func (r *Run) Tag(now time.Time, target int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkTime(now); err != nil {
		return err
	}
	if !r.usable() || target < 0 || target >= 2 || target == r.active || r.vitals[target].HP <= 0 || now.Before(r.nextTag) {
		return fmt.Errorf("帝国竞技场当前不能切换目标角色")
	}
	r.active, r.lastTime = target, now
	r.nextTag = now.Add(time.Duration(r.catalog.config.CharacterTagCooldownRaw) * time.Second)
	return nil
}

// ApplyVitals只接收服务端伤害/治疗处理结果；不能直接传入客户端声明的HP/MP。
// 死亡后不能用同步或恢复道具复活；确认死亡后的接替使用HandoffAfterDeath。
func (r *Run) ApplyVitals(now time.Time, role int, hp, mp int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkTime(now); err != nil {
		return err
	}
	if r.phase != Fighting || role != r.active || role < 0 || role >= 2 {
		return fmt.Errorf("帝国竞技场不能更新非当前战斗角色")
	}
	v := &r.vitals[role]
	if hp < 0 || hp > v.MaxHP || mp < 0 || mp > v.MaxMP || v.HP == 0 && hp != 0 {
		return fmt.Errorf("帝国竞技场生命/魔法变化无效")
	}
	v.HP, v.MP, r.lastTime = hp, mp, now
	if r.vitals[0].HP == 0 && r.vitals[1].HP == 0 {
		r.phase = Defeated
	}
	return nil
}

// HandoffAfterDeath只允许已确认死亡的出战者交给存活队员，不复活、不推进回合。
// 原生死亡角色不会发普通Tag，接替不能被生前的手动切人冷却阻塞。
// 重复调用没有副作用，普通Tag的冷却与目标校验保持不变。
func (r *Run) HandoffAfterDeath(now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkTime(now); err != nil {
		return false, err
	}
	if r.phase != Fighting || r.vitals[r.active].HP != 0 || r.vitals[1-r.active].HP <= 0 {
		return false, nil
	}
	r.active, r.lastTime = 1-r.active, now
	r.nextTag = now.Add(time.Duration(r.catalog.config.CharacterTagCooldownRaw) * time.Second)
	return true, nil
}

// UseConsumable返回真实PVF效果供桥接层投影；库存不扣除，也不能用于死亡队员。
// 效果单位与持续时间原样保留，不将速度300直接解释为300%。
func (r *Run) UseConsumable(now time.Time, slot int) (Consumable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkTime(now); err != nil {
		return Consumable{}, err
	}
	if r.phase != Fighting || slot < 0 || slot >= 6 || r.vitals[r.active].HP == 0 || now.Before(r.itemCooldown[slot]) {
		return Consumable{}, fmt.Errorf("帝国竞技场专用道具当前不可使用")
	}
	rule := r.catalog.Consumables()[slot]
	if rule.ID == 0 || rule.Cooldown <= 0 {
		return Consumable{}, fmt.Errorf("帝国竞技场专用道具配置缺失")
	}
	v := &r.vitals[r.active]
	recover := func(value, max int64) int64 {
		// 避免max*percent溢出；按整数向下取整，与属性整数域一致。
		amount := max/100*rule.RecoveryPercent + (max%100)*rule.RecoveryPercent/100
		if amount > max-value {
			return max
		}
		return value + amount
	}
	switch rule.RecoverySection {
	case "hp recovery":
		v.HP = recover(v.HP, v.MaxHP)
	case "mp recovery":
		v.MP = recover(v.MP, v.MaxMP)
	}
	r.itemCooldown[slot], r.lastTime = now.Add(rule.Cooldown), now
	return rule, nil
}

func (r *Run) Abandon(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkTime(now); err != nil {
		return err
	}
	if !r.usable() {
		return fmt.Errorf("帝国竞技场挑战已经结束")
	}
	r.phase, r.lastTime = Abandoned, now
	return nil
}

// BattleLocked覆盖整场比赛，包括回合间隔和竞技之王选择；回合结束不等于回到
// 休息室，不能借间隔更换装备/技能或使用普通消耗品。疲劳与耐久不由本玩法扣除。
func (r *Run) BattleLocked() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase == Fighting || r.phase == BetweenRounds || r.phase == SpecialChoice
}
