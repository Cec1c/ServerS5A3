package dnfbridge

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

const currentTagTournamentDailyNotification uint16 = 1395
const currentTagTournamentStatusNotification uint16 = 1398
const currentTagTournamentEnterOpcode uint16 = 1458

// 57d8fbfc:14612E0读取普通行、单个竞技之王、困难行，再读取共同弱点。
// 14566C0/1456710在模式2取困难vector，普通第4号索引取竞技之王。
// 1292730把属性值加21作为边框帧；配套tagtournament.img的21..25分别是
// 火、冰、暗、光、无属性。不能按领域常量声明顺序直接强制转换。
func currentTagTournamentElement(value tagtournament.ElementWeakness) (byte, error) {
	switch value {
	case tagtournament.Fire:
		return 0, nil
	case tagtournament.Ice:
		return 1, nil
	case tagtournament.Dark:
		return 2, nil
	case tagtournament.Light:
		return 3, nil
	case "":
		return 4, nil
	default:
		return 0, fmt.Errorf("帝国竞技场弱点属性无对应原生边框")
	}
}

func buildCurrentTagTournamentLobbyWire(view tagtournament.LobbySchedule) ([]byte, []byte, error) {
	if view.Normal.Mode != tagtournament.Normal || view.Hard.Mode != tagtournament.Hard || view.Normal.ServiceDay != view.Hard.ServiceDay || view.Normal.ConfigHash == "" || view.Normal.ConfigHash != view.Hard.ConfigHash || view.Normal.SpecialFrom != 4 || len(view.Normal.Encounters) != 5 || len(view.Hard.Encounters) != 7 || view.Hard.SpecialFrom != 7 {
		return nil, nil, fmt.Errorf("帝国竞技场今日名单与现用界面4加1及7回合契约不符")
	}
	if view.Paid && view.LockedMode != tagtournament.Normal && view.LockedMode != tagtournament.Hard || !view.Paid && (view.LockedMode != 0 || view.RewardedRounds != 0) || view.RewardedRounds < 0 || view.RewardedRounds > 7 || view.LockedMode == tagtournament.Normal && view.RewardedRounds > 5 {
		return nil, nil, fmt.Errorf("帝国竞技场支付模式或领奖遮罩无效")
	}
	var w packetWriter
	seen := map[uint32]bool{}
	groups := [][]tagtournament.DailyEncounter{view.Normal.Encounters[:4], view.Normal.Encounters[4:], view.Hard.Encounters}
	for group, rows := range groups {
		w.writeByte(byte(len(rows)))
		for _, row := range rows {
			if row.MonsterID == 0 || row.MapID == 0 || seen[row.MonsterID] || row.Optional != (group == 1) || group == 1 && row.Weakness != "" || group != 1 && row.Weakness == "" {
				return nil, nil, fmt.Errorf("帝国竞技场今日怪物重复、缺失或弱点与关卡不符")
			}
			seen[row.MonsterID] = true
			attr, err := currentTagTournamentElement(row.Weakness)
			if err != nil {
				return nil, nil, err
			}
			w.writeUint32(row.MonsterID)
			w.writeByte(attr)
		}
	}
	// 共同弱点的0/1映射由配套UI的困难说明图确认，见本轮取证。
	switch view.Hard.CommonWeakness {
	case tagtournament.Physical:
		w.writeByte(1)
	case tagtournament.Magical:
		w.writeByte(0)
	default:
		return nil, nil, fmt.Errorf("帝国竞技场困难共同弱点无效")
	}
	// 1461E40: paid -> +EC，非零mode -> +ED锁定及+F0，领奖前缀 -> +110。
	// 未支付时mode=0解除锁定，让客户端按本地按钮选择预览；不能发1锁死普通。
	return w.bytes(), []byte{boolByte(view.Paid), byte(view.LockedMode), byte(view.RewardedRounds)}, nil
}

func (s *Service) sendCurrentTagTournamentDailyLobby(p *gameSession, now time.Time) error {
	if !s.isCurrentTagTournamentChannel(p) {
		return nil
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		s.logGameEvent(p, "帝国竞技场每日资料未就绪", "error", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	view, err := owner.Lobby(ctx, p.accountID)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场账号每日资料未投影", "error", err)
		return nil
	}
	schedule, err := s.tagTournamentCatalog.LobbySchedule(now, view.Day)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场每日模式与领奖记录不符", "error", err)
		return nil
	}
	daily, status, err := buildCurrentTagTournamentLobbyWire(schedule)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场每日界面资料校验失败", "error", err)
		return nil
	}
	if err = s.sendGameUpperRawClass(p, currentTagTournamentDailyNotification, daily, 0); err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(p, currentTagTournamentStatusNotification, status, 0); err != nil {
		return err
	}
	p.tagTournamentLobbyDay = premium.ServiceDayID(now)
	return nil
}

// 1464D10购买确认只写模式u8。全部只读准备通过、账号事务提交成功后，
// 先发真实1389/1390，再回1458成功让原生继续op15/op16。
func (s *Service) handleCurrentTagTournamentEnter(p *gameSession, body []byte) error {
	b, err := s.prepareCurrentTagTournamentEntry(p, body, false)
	if err != nil {
		return s.sendGameUpperFailure(p, currentTagTournamentEnterOpcode, currentSelectDungeonInvalidSelectionStateReason)
	}
	if err = s.sendCurrentTagTournamentEntry(p, b); err != nil {
		return err
	}
	return s.sendGameUpperSuccess(p, currentTagTournamentEnterOpcode, nil)
}

// 1458购买与当日已付费的op16重入共用完整准备和创建资料，分别由各自协议应答。
func (s *Service) sendCurrentTagTournamentEntry(p *gameSession, b *currentTagTournamentBattle) error {
	if b.prepared.creationSent {
		return nil
	}
	current := strconv.Itoa(int(p.selectedCharacterID))
	for _, ch := range b.characters {
		if ch.CharacterID == current {
			if err := s.sendCarryGoldWallet(p, ch.Stats["gold"]); err != nil {
				return err
			}
		}
	}
	if err := s.sendCurrentTagTournamentCreation(p, b); err != nil {
		return err
	}
	if err := s.sendCurrentTagTournamentDailyLobby(p, b.now()); err != nil {
		return err
	}
	s.logGameEvent(p, "帝国竞技场双角色创建已发送，等待实际选图", "mode", b.plan.Mode, "service_day", b.plan.ServiceDay, "round_count", len(b.plan.Encounters))
	return nil
}

func (s *Service) prepareCurrentTagTournamentEntry(p *gameSession, body []byte, paidOnly bool) (*currentTagTournamentBattle, error) {
	stage := "请求和会话"
	started, stageStarted := time.Now(), time.Now()
	nextStage := func(next string) {
		s.logGameEvent(p, "帝国竞技场入场阶段耗时", "stage", stage, "elapsed_ms", time.Since(stageStarted).Milliseconds(), "total_ms", time.Since(started).Milliseconds())
		stage, stageStarted = next, time.Now()
	}
	defer func() { nextStage("完成") }()
	refuse := func(err error) (*currentTagTournamentBattle, error) {
		s.logGameEvent(p, "帝国竞技场挑战请求未入场，本次未扣费", "阶段", stage, "error", err)
		return nil, err
	}
	if len(body) != 1 || body[0] != 1 && body[0] != 2 || !currentTagTournamentLobbyInteractionReady(p) {
		return refuse(fmt.Errorf("帝国竞技场挑战正文或休息室状态无效"))
	}
	// 真实挑战请求已到达休息室；若完成回城尚无35/1467，在创建下一局前
	// 先恢复原保存分组，避免把旧队伍恢复拖到新战斗中。
	if err := s.sendCurrentTagTournamentConfirmedReturnRoster(p); err != nil {
		return refuse(err)
	}
	if p.tagTournamentBattle != nil {
		phase := p.tagTournamentBattle.run.Snapshot().Phase
		if (phase == tagtournament.Finished || phase == tagtournament.Defeated || phase == tagtournament.Abandoned) && !p.tagTournamentBattle.settlementPending && !p.tagTournamentBattle.settlementProjectionPending && !p.tagTournamentBattle.roundProjectionPending && !p.tagTournamentBattle.mailAlarmPending {
			p.tagTournamentBattle = nil
		}
	}
	if p.tagTournamentBattle != nil {
		b, err := s.currentTagTournamentBattleFor(p)
		if err != nil {
			return refuse(err)
		}
		if b.prepared == nil || b.plan.Mode != tagtournament.Mode(body[0]) || b.run.Snapshot().Phase != tagtournament.Lobby {
			return refuse(fmt.Errorf("帝国竞技场已有未结束挑战"))
		}
		return b, nil
	}
	if state := s.runtimePartyStateForSession(p); state.PartyID > 0 {
		return refuse(fmt.Errorf("帝国竞技场仅允许单人双角色参加，请先退出普通队伍"))
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		return refuse(fmt.Errorf("帝国竞技场挑战会话已失效"))
	}
	rows, _, err := s.currentTagTournamentSentRoster(p)
	if err == nil {
		_, err = s.currentTagTournamentLobbyCharacters(p, rows)
	}
	if err != nil {
		return refuse(err)
	}
	owner, err := s.currentTagTournamentOwner()
	if err != nil {
		return refuse(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	current := strconv.Itoa(int(identity.character))
	repositories, _ := s.repositoryGroup()
	ch, found, err := repositories.Character.Load(ctx, current)
	if err != nil {
		return refuse(err)
	}
	if !found || ch.AccountID != p.accountID || ch.Stats["town_id"] != 48 {
		return refuse(fmt.Errorf("帝国竞技场当前角色不在休息室"))
	}
	nextStage("账号付费模式和已注册队伍")
	preview, err := owner.PreviewEntry(ctx, p.accountID, current, tagtournament.Mode(body[0]))
	if err != nil {
		return refuse(err)
	}
	view, err := owner.Lobby(ctx, p.accountID)
	if err != nil || !view.Registered {
		if err == nil {
			err = fmt.Errorf("帝国竞技场注册队伍已变化")
		}
		return refuse(err)
	}
	if paidOnly && (!view.Day.Paid || view.Day.Mode != tagtournament.Mode(body[0]) || preview.Fee != 0) {
		return refuse(fmt.Errorf("帝国竞技场重入需要当日已支付的同模式资格"))
	}
	nextStage("逐角色属性、装备、技能和宠物")
	characters, err := s.prepareCurrentTagTournamentCharacters(ctx, p, view.Characters)
	if err != nil {
		return refuse(err)
	}
	nextStage("备用角色完整创建正文")
	wire, err := s.prepareCurrentTagTournamentCharacterWire(p, characters)
	if err != nil {
		return refuse(err)
	}
	nextStage("双角色独立选项")
	options, err := s.prepareCurrentTagTournamentCharacterOptions(ctx, p, characters)
	if err != nil {
		return refuse(err)
	}
	nextStage("双角色技能指令和Buff换装")
	skillState, err := s.prepareCurrentTagTournamentSkillState(ctx, p, characters)
	if err != nil {
		return refuse(err)
	}
	nextStage("今日地图、机制对象和入场发包预检")
	table, resolver, err := s.dungeonWorldMap()
	if err != nil {
		return refuse(err)
	}
	layout, err := s.tagTournamentCatalog.BuildLayout(table, resolver, time.Now(), preview.Plan)
	if err != nil {
		return refuse(err)
	}
	prepared, err := s.prepareCurrentTagTournamentPlay(p, layout, characters, wire, options)
	if err != nil {
		return refuse(err)
	}
	prepared.skillState = skillState
	// 全部地图的冷读取可能耗时，扣费前再核对两名队员的设置和拥有物。
	nextStage("地图准备后的角色选项和技能资料复核")
	latestOptions, err := s.prepareCurrentTagTournamentCharacterOptions(ctx, p, characters)
	if err != nil {
		return refuse(err)
	}
	latestSkills, err := s.prepareCurrentTagTournamentSkillState(ctx, p, characters)
	if err != nil {
		return refuse(err)
	}
	if !bytes.Equal(latestOptions, options) || !bytes.Equal(latestSkills.commands, skillState.commands) || !bytes.Equal(latestSkills.darkKnight, skillState.darkKnight) || !reflect.DeepEqual(latestSkills.buffSources, skillState.buffSources) {
		return refuse(fmt.Errorf("帝国竞技场地图准备期间角色设置已变化，请重试"))
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return refuse(fmt.Errorf("帝国竞技场准备期间会话已经变化"))
	}
	// 冷PVF/全地图预检不占用短写事务的时限。
	nextStage("日计划快照和金币事务")
	writeCtx, writeCancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer writeCancel()
	var admission tagtournament.Admission
	if paidOnly {
		admission, err = owner.ResumePrepared(writeCtx, p.accountID, current, view.Characters, preview.Plan)
	} else {
		admission, err = owner.AdmitPrepared(writeCtx, p.accountID, current, view.Characters, preview.Plan)
	}
	if err != nil {
		return refuse(err)
	}
	b, err := s.newCurrentTagTournamentBattle(p, owner, admission, characters)
	if err != nil {
		s.logGameEvent(p, "帝国竞技场每日费用已提交，挑战绑定失败，付费资格保留", "error", err)
		return nil, err
	}
	b.prepared = prepared
	p.tagTournamentBattle = b
	for _, ch := range admission.Characters() {
		if ch.CharacterID == current {
			prepared.runtime.Character = ch
		}
	}
	s.logGameEvent(p, "帝国竞技场挑战资料已准备", "mode", body[0], "service_day", b.plan.ServiceDay, "gold_fee", admission.Fee(), "paid_reentry", paidOnly)
	return b, nil
}
