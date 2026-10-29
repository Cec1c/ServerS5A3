package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/tagtournament"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentTagTournamentTagOpcode uint16 = 1465
const currentTagTournamentDeathOpcode uint16 = 1466
const currentTagTournamentTagNotification uint16 = 1402
const currentTagTournamentRoundRewardNotification uint16 = 1397

// 每条连接独立持有一场挑战，注册位置、客户端序号与当前登录角色互不混用。
// Run持有已确认存活/死亡、回合及冷却；伤害数值仍由现用客户端计算。
// 桥接层保留实际地图、逐角色资料、原生投影和可靠重发状态。
type currentTagTournamentBattle struct {
	identity                    boundGameSessionCharacter
	owner                       *tagtournament.Owner
	run                         *tagtournament.Run
	plan                        tagtournament.DailyPlan
	characters                  [2]repo.CharacterRecord
	nativeRoleIDs               [2]byte
	now                         func() time.Time
	tagProjectionPending        bool
	settlementPending           bool
	settlementProjectionPending bool
	pendingRoundRewards         []tagtournament.RewardRow
	roundRewardGranted          bool
	mailAlarmPending            bool
	prepared                    *currentTagTournamentPrepared
	roundProjectionPending      bool
	projectedRounds             int
	startedAt                   time.Time
	completionBody              []byte
	completionSent              bool
	completionRecord            *tagtournament.CompletionRecord
	weeklyRecordSent            bool
	returnRosterSent            bool
}

// 此入口只接受Owner已经提交的凭据与已完成的逐角色投影，不负责猜测1389运行字段。
// 生产1458完成正文、选项及所有地图预检后才扣费，并将冻结资料绑定到此场挑战。
func (s *Service) newCurrentTagTournamentBattle(p *gameSession, owner *tagtournament.Owner, admission tagtournament.Admission, projections [2]currentTagTournamentCharacterProjection) (*currentTagTournamentBattle, error) {
	identity, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok || owner == nil || !s.isCurrentTagTournamentChannel(p) || !p.accountFromToken || !p.loginPacketSeen || p.tagTournamentBattle != nil {
		return nil, fmt.Errorf("帝国竞技场挑战缺少有效会话或已有未结束挑战")
	}
	rows, _, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		return nil, err
	}
	characters := admission.Characters()
	var selectors [2]byte
	var vitals [2]tagtournament.Vitals
	currentFound := false
	for i, ch := range characters {
		projection := projections[i]
		if ch.CharacterID == "" || ch.AccountID != p.accountID || projection.character.CharacterID != ch.CharacterID || projection.character.Name != ch.Name || projection.character.Job != ch.Job || projection.character.Level != ch.Level || projection.character.Slot != ch.Slot || len(projection.stats) != currentMode1StatBlobWireSize {
			return nil, fmt.Errorf("帝国竞技场挑战凭据与双角色资料不符")
		}
		found := false
		for j, row := range rows {
			if row.CharacterID == ch.CharacterID && row.Name == ch.Name && row.Slot == ch.Slot {
				selectors[i], found = byte(j), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("帝国竞技场队员不在该连接已发角色表")
		}
		hp, mp := int64(binary.LittleEndian.Uint32(projection.stats)), int64(binary.LittleEndian.Uint32(projection.stats[4:]))
		vitals[i] = tagtournament.Vitals{HP: hp, MaxHP: hp, MP: mp, MaxMP: mp}
		currentFound = currentFound || ch.CharacterID == strconv.Itoa(int(identity.character))
	}
	if !currentFound || selectors[0] == selectors[1] {
		return nil, fmt.Errorf("帝国竞技场登录角色或队员序号无效")
	}
	run, err := tagtournament.NewRun(admission, vitals)
	if err != nil {
		return nil, err
	}
	if characters[run.Snapshot().Active].CharacterID != strconv.Itoa(int(identity.character)) {
		return nil, fmt.Errorf("帝国竞技场入场付款角色与当前登录角色不符")
	}
	nativeRoleIDs, err := currentTagTournamentNativeRoleIDs(identity.character, characters)
	if err != nil {
		return nil, err
	}
	return &currentTagTournamentBattle{identity: identity, owner: owner, run: run, plan: admission.Plan(), characters: characters, nativeRoleIDs: nativeRoleIDs, now: time.Now}, nil
}

func (s *Service) currentTagTournamentBattleFor(p *gameSession) (*currentTagTournamentBattle, error) {
	if p == nil || !s.isCurrentTagTournamentChannel(p) || p.tagTournamentBattle == nil {
		return nil, fmt.Errorf("帝国竞技场尚未创建双角色挑战")
	}
	b := p.tagTournamentBattle
	if b.run == nil || b.owner == nil || b.identity.session != p || !p.accountFromToken || b.identity.character != p.selectedCharacterID || !s.boundGameSessionCharacterCurrent(b.identity) || b.now == nil || premium.ServiceDayID(b.now()) != b.plan.ServiceDay {
		return nil, fmt.Errorf("帝国竞技场挑战会话已失效或已跨06点")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if err := b.owner.ValidateRun(ctx, b.run); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *currentTagTournamentBattle) roleForNativeID(id byte) (int, bool) {
	for i, value := range b.nativeRoleIDs {
		if id == value {
			return i, true
		}
	}
	return 0, false
}

func (s *Service) sendCurrentTagTournamentTagProjection(p *gameSession, b *currentTagTournamentBattle) error {
	snapshot := b.run.Snapshot()
	if err := s.sendCurrentTagTournamentHotkeys(p, b, snapshot.Active); err != nil {
		b.tagProjectionPending = true
		return err
	}
	if err := s.sendGameUpperRawClass(p, currentTagTournamentTagNotification, []byte{b.nativeRoleIDs[snapshot.Active]}, 0); err != nil {
		b.tagProjectionPending = true
		return err
	}
	b.tagProjectionPending = false
	return nil
}

// 1456680只写目标容器位置u8（本体0、备用1）；1460110的成功ACK仅清pending，真正切人由
// class0/1402的1460EC0执行。拒绝必须回失败ACK，否则原生+50一直等待。
func (s *Service) handleCurrentTagTournamentTag(p *gameSession, body []byte) error {
	refuse := func(err error) error {
		s.logGameEvent(p, "帝国竞技场切人请求已拒绝", "error", err)
		return s.sendGameUpperFailure(p, currentTagTournamentTagOpcode, 3)
	}
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return refuse(err)
	}
	if b.prepared != nil && !b.prepared.entered {
		return refuse(fmt.Errorf("帝国竞技场正在准备进图，暂不可切人"))
	}
	if len(body) != 1 {
		return refuse(fmt.Errorf("帝国竞技场切人正文必须为单个队员序号"))
	}
	role, found := b.roleForNativeID(body[0])
	if !found {
		return refuse(fmt.Errorf("帝国竞技场切人目标不在已创建的队伍中"))
	}
	if b.tagProjectionPending {
		// 上次写失败只重投已提交的角色，不再切一次或延长冷却。
		return s.sendCurrentTagTournamentTagProjection(p, b)
	}
	if err := b.run.Tag(b.now(), role); err != nil {
		return refuse(err)
	}
	b.tagProjectionPending = true
	return s.sendCurrentTagTournamentTagProjection(p, b)
}

// 2771860经14571D0查出actor在容器中的位置，再由1455FC0写入1466，跳过普通40。
// 只有实际进图且当前出战的已创建队员可提交死亡，不接受任意生命数值。
func (s *Service) handleCurrentTagTournamentDeath(p *gameSession, body []byte) error {
	refuse := func(err error) error {
		s.logGameEvent(p, "帝国竞技场死亡通知未通过服务端确认", "error", err)
		return s.sendGameUpperFailure(p, currentTagTournamentDeathOpcode, 3)
	}
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return refuse(err)
	}
	if len(body) != 1 {
		return refuse(fmt.Errorf("帝国竞技场死亡正文必须为单个队员序号"))
	}
	role, found := b.roleForNativeID(body[0])
	snapshot := b.run.Snapshot()
	if !found || role != snapshot.Active && snapshot.Vitals[role].HP != 0 {
		return refuse(fmt.Errorf("帝国竞技场死亡队员与权威生命状态不符"))
	}
	if snapshot.Vitals[role].HP != 0 {
		if err = s.acceptCurrentTagTournamentDeath(p, b, role); err != nil {
			return refuse(err)
		}
	}
	// V195的1466成功应答会启动整局失败倒计时，并在10秒后发送42。
	// 只有全灭才发送它；单人死亡由1402接替，重复包只补未完成投影。
	if switched, handoffErr := b.run.HandoffAfterDeath(b.now()); handoffErr != nil {
		return refuse(handoffErr)
	} else if switched {
		b.tagProjectionPending = true
	}
	if b.run.Snapshot().Phase == tagtournament.Defeated {
		return s.sendGameUpperSuccess(p, currentTagTournamentDeathOpcode, nil)
	}
	if b.tagProjectionPending {
		return s.sendCurrentTagTournamentTagProjection(p, b)
	}
	return nil
}

// BeginNext只由服务端地图准备路径调用，不能用客户端怪物ID/回合号推进。
func (s *Service) beginCurrentTagTournamentRound(p *gameSession, acceptSpecial bool) (tagtournament.DailyEncounter, bool, error) {
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return tagtournament.DailyEncounter{}, false, err
	}
	if b.settlementPending || b.settlementProjectionPending {
		return tagtournament.DailyEncounter{}, false, fmt.Errorf("帝国竞技场上一回合邮件或通知尚未完成")
	}
	return b.beginRound(b.now(), acceptSpecial)
}

func (b *currentTagTournamentBattle) beginRound(now time.Time, acceptSpecial bool) (tagtournament.DailyEncounter, bool, error) {
	e, recovery, err := b.run.BeginNext(now, acceptSpecial)
	if err == nil && b.startedAt.IsZero() && b.run.Snapshot().Round == 0 {
		b.startedAt = now
	}
	return e, recovery, err
}

// 只有现用地图已清空，且权威死亡表里存在预定Boss，才把进度交给领域层。
// 不读取客户端上报的回合、怪物ID或奖励数量；重复死亡仅补偿未完成结算。
func (s *Service) confirmCurrentTagTournamentRound(p *gameSession, mapID uint32, defeated []uint32, roomCleared bool) error {
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return err
	}
	snapshot := b.run.Snapshot()
	if snapshot.Round < 0 || snapshot.Round >= len(b.plan.Encounters) {
		return fmt.Errorf("帝国竞技场尚无正在进行的回合")
	}
	want := b.plan.Encounters[snapshot.Round]
	bossConfirmed := false
	for _, monster := range defeated {
		bossConfirmed = bossConfirmed || monster == want.MonsterID
	}
	if !roomCleared || mapID != want.MapID || !bossConfirmed {
		return fmt.Errorf("帝国竞技场回合缺少对应地图与Boss的权威死亡证明")
	}
	if snapshot.Phase == tagtournament.Fighting {
		if err = b.run.ConfirmBossDefeated(b.now(), mapID, want.MonsterID); err != nil {
			return err
		}
	} else if snapshot.Cleared != snapshot.Round+1 {
		return fmt.Errorf("帝国竞技场回合状态与已确认进度不符")
	}
	snapshot = b.run.Snapshot()
	if snapshot.Phase == tagtournament.Finished && b.completionBody == nil {
		b.completionBody, err = buildCurrentTagTournamentCompletion(b.startedAt, b.now())
		if err != nil {
			return err
		}
	}
	b.roundProjectionPending = snapshot.Cleared > b.projectedRounds
	// 每个已确认阶段即时交付邮件；困难仍以当日最高阶段累计额补差，
	// 不把七列累计总额逐列叠加，也不重复发已领取阶段。
	b.settlementPending = true
	return s.flushCurrentTagTournamentSettlement(p, b)
}

// 调用方持有dungeon.mu。仅消费这条会话正在持有的DungeonSession已确认的
// RuntimeObjects/DefeatedObjects，不从op39正文中的编号直接发奖。
func (s *Service) syncCurrentTagTournamentRoundLocked(p *gameSession, runtime *runtimeDungeonState) (bool, error) {
	if p == nil || p.tagTournamentBattle == nil || !s.isCurrentTagTournamentChannel(p) {
		return false, nil
	}
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return true, err
	}
	if runtime == nil || p.dungeon.runtime != runtime || runtime.Session == nil || runtime.AccountID != p.accountID || runtime.Character.CharacterID != strconv.Itoa(int(p.selectedCharacterID)) || runtime.Dungeon.ID != int64(b.plan.DungeonID) || runtime.Session.Snapshot().Run.DungeonID != int64(b.plan.DungeonID) {
		return true, fmt.Errorf("帝国竞技场地图运行时不属于当前挑战")
	}
	scene, ok := runtime.Session.Scene()
	if !ok {
		return true, fmt.Errorf("帝国竞技场当前地图缺少权威场景")
	}
	if !scene.Cleared {
		return true, nil
	}
	if scene.Map.Map.ID <= 0 || scene.Map.Map.ID > int64(^uint32(0)) {
		return true, fmt.Errorf("帝国竞技场地图编号不能由当前协议表示")
	}
	var defeated []uint32
	for _, key := range scene.DefeatedObjects {
		ref, found := scene.RuntimeObjects[key]
		if !found || ref.Kind != worldmap.HostileMonster || ref.Index < 0 || ref.Index >= len(scene.Monsters) {
			continue
		}
		id := scene.Monsters[ref.Index].MonsterID
		if id > 0 && id <= int64(^uint32(0)) {
			defeated = append(defeated, uint32(id))
		}
	}
	return true, s.confirmCurrentTagTournamentRound(p, uint32(scene.Map.Map.ID), defeated, true)
}

// 用于战斗管线已经确认的伤害/治疗结果；1466收包路径不调用它。
func (s *Service) applyCurrentTagTournamentVitals(p *gameSession, role int, hp, mp int64) error {
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return err
	}
	if err = b.run.ApplyVitals(b.now(), role, hp, mp); err != nil {
		return err
	}
	if b.run.Snapshot().Phase == tagtournament.Defeated {
		b.settlementPending = b.run.Snapshot().Cleared > 0
		return s.flushCurrentTagTournamentSettlement(p, b)
	}
	return nil
}

func (s *Service) finishCurrentTagTournamentBattle(p *gameSession) error {
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return err
	}
	snapshot := b.run.Snapshot()
	if snapshot.Phase != tagtournament.Finished && snapshot.Phase != tagtournament.Defeated && snapshot.Phase != tagtournament.Abandoned {
		if err = b.run.Abandon(b.now()); err != nil {
			return err
		}
	}
	if snapshot.Cleared > 0 {
		b.settlementPending = true
	}
	return s.flushCurrentTagTournamentSettlement(p, b)
}

// 14618B0分配28字节，前后两段各只能容纳两个奖励；强制上限2，避免原生
// count>2写穿相邻字段。数量0只保留图标；困难中间回合展示可结算差额，
// 普通或终局展示已提交附件。显示通知不能代替邮件提交。
func buildCurrentTagTournamentRoundReward(rows []tagtournament.RewardRow) ([]byte, error) {
	if len(rows) > 2 {
		return nil, fmt.Errorf("帝国竞技场回合奖励超出现用客户端两项容量")
	}
	var w packetWriter
	w.writeByte(byte(len(rows)))
	seen := map[uint32]bool{}
	for _, row := range rows {
		if row.ItemID == 0 || seen[row.ItemID] {
			return nil, fmt.Errorf("帝国竞技场回合奖励附件无效或重复")
		}
		seen[row.ItemID] = true
		w.writeUint32(row.ItemID)
		w.writeUint16(row.Count)
	}
	return w.bytes(), nil
}

func (s *Service) flushCurrentTagTournamentSettlement(p *gameSession, b *currentTagTournamentBattle) error {
	if current, err := s.currentTagTournamentBattleFor(p); err != nil || current != b {
		return fmt.Errorf("帝国竞技场结算连接或挑战已经替换")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if b.settlementPending {
		result, err := b.owner.Settle(ctx, b.run)
		if err != nil {
			return err
		}
		b.settlementPending = false
		b.mailAlarmPending = b.mailAlarmPending || len(result.MailIDs) > 0
		// 保留已提交但尚未投影的发奖事实；重复死亡触发的幂等结算
		// 可能返回零封新信，不能因此丢掉上次写失败待补投的窗口。
		b.roundRewardGranted = b.roundRewardGranted || len(result.MailIDs) > 0
		// 一次补偿可能补齐多封普通邮件；1397只展示本回合，不能将多个
		// 回合附件拼入固定两项数组。完整内容仍由真实邮箱提供。
		if len(result.MailIDs) == 1 && len(result.Rewards) <= 2 {
			b.pendingRoundRewards = append([]tagtournament.RewardRow(nil), result.Rewards...)
		}
		b.settlementProjectionPending = true
	}
	if b.mailAlarmPending {
		if err := s.sendMailboxAlarmForSession(p, p.selectedCharacterID, "帝国竞技场奖励邮件已提交"); err != nil {
			return err
		}
		b.mailAlarmPending = false
	}
	if b.settlementProjectionPending {
		if err := s.projectCurrentTagTournamentRoundReward(p, b); err != nil {
			return err
		}
		// 中途全灭的困难邮件只结算已通过回合，不向原生发送当前房通关UI。
		b.pendingRoundRewards = nil
		b.roundRewardGranted = false
		// 结算路径必须把读取/编码失败交给补偿队列，不能采用休息室广播的
		// 容错返回值后误标通知成功。只刷新支付/领奖状态，无需重发当日怪物。
		view, err := b.owner.Lobby(ctx, p.accountID)
		if err != nil {
			return err
		}
		schedule, err := s.tagTournamentCatalog.LobbySchedule(b.now(), view.Day)
		if err != nil {
			return err
		}
		_, status, err := buildCurrentTagTournamentLobbyWire(schedule)
		if err != nil {
			return err
		}
		if err = s.sendGameUpperRawClass(p, currentTagTournamentStatusNotification, status, 0); err != nil {
			return err
		}
		b.settlementProjectionPending = false
	}
	if err := s.projectCurrentTagTournamentRoundReward(p, b); err != nil {
		return err
	}
	return s.sendCurrentTagTournamentCompletion(p, b)
}
