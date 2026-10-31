package dnfbridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dungeoncmd"
	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// network owner仍是原登录角色，战斗归属取本场Run的Active及其冻结技能/装备。
// 不切selectedCharacterID，不把备用角色写成账号钱包、邮件或副本生命周期owner。
func (s *Service) currentTagTournamentCombatActor(p *gameSession, b *currentTagTournamentBattle) (currentTagTournamentCharacterProjection, error) {
	if b == nil || b.prepared == nil || !b.prepared.entered || p.dungeon.runtime != b.prepared.runtime || b.prepared.runtime.Character.CharacterID != strconv.Itoa(int(p.selectedCharacterID)) {
		return currentTagTournamentCharacterProjection{}, fmt.Errorf("帝国竞技场尚无当前连接拥有的战斗地图")
	}
	snapshot := b.run.Snapshot()
	if snapshot.Phase != tagtournament.Fighting || snapshot.Active < 0 || snapshot.Active >= 2 || snapshot.Vitals[snapshot.Active].HP <= 0 {
		return currentTagTournamentCharacterProjection{}, fmt.Errorf("帝国竞技场当前没有存活的战斗角色")
	}
	return b.prepared.characters[snapshot.Active], nil
}

// 与普通op39相同，伤害由客户端计算；服务器只接受当前地图已公告的对象键。
// Tag后的击杀者仍使用1389绑定的共同root对象，不把紧凑角色序号当对象键。
// 调用方持dungeon.mu；此路径不进入普通经验、任务、掉落、宠物成长或翻牌。
func (s *Service) handleCurrentTagTournamentMonsterDeathLocked(p *gameSession, runtime *runtimeDungeonState, request dungeoncmd.DieMonsterRequest) error {
	b, err := s.currentTagTournamentBattleFor(p)
	if err != nil {
		return err
	}
	if b.prepared == nil || !b.prepared.entered {
		return nil
	}
	actor := b.prepared.characters[b.run.Snapshot().Active]
	if _, dead := runtime.Room.DefeatedDeathTarget(request.RuntimeObjectKey); !dead {
		if _, err = s.currentTagTournamentCombatActor(p, b); err != nil {
			return nil
		}
	}
	if runtime != b.prepared.runtime || request.OwnerObjectKey != currentSceneActorObjectKey(p.selectedCharacterID) && request.OwnerObjectKey != ^uint16(0) && !dungeonRuntimeContainsDeathOwnerObjectKey(runtime, uint32(request.OwnerObjectKey)) {
		return nil
	}
	// 已提交死亡可以补发通知或重试邮件，但不能重复结算。
	target, _, err := runtime.Room.CommitActorDeathReport(request.RuntimeObjectKey, runtime.Session)
	if errors.Is(err, errDungeonMonsterAlreadyDefeated) {
		var ok bool
		target, ok = runtime.Room.DefeatedDeathTarget(request.RuntimeObjectKey)
		if !ok {
			return nil
		}
	} else if err != nil {
		var matched bool
		target, matched, err = runtime.Room.RetireSpecialMonsterActor(request.RuntimeObjectKey)
		if !matched || err != nil {
			return nil
		}
	}
	response, err := buildCurrentDungeonDeathNotificationBodyWithDrops(target.ObjectKey, target.ResponseKind, nil)
	if err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(p, 38, response, 0); err != nil {
		return err
	}
	runtime.Room.RecordDeathResponseBody(target.ObjectKey, response)
	// 今日Boss的真实死亡结束本回合，收回地图已公告的剩余机制怪物。
	snapshot := b.run.Snapshot()
	if snapshot.Round >= 0 && snapshot.Round < len(b.plan.Encounters) {
		want := b.plan.Encounters[snapshot.Round]
		if monster, ok := runtime.Room.MonsterByReference(target.Reference, runtimeDungeonMonsterDefeated); ok && monster.Spawn.MonsterID == int64(want.MonsterID) {
			forced, _, forceErr := runtime.Room.CommitRemainingAnnouncedHostilesAfterBoss(target.ObjectKey, runtime.Session)
			if forceErr != nil {
				return forceErr
			}
			for _, dead := range forced {
				body, err := buildCurrentDungeonDeathNotificationBodyWithDrops(dead.ObjectKey, dead.ResponseKind, nil)
				if err != nil {
					return err
				}
				if err = s.sendGameUpperRawClass(p, 38, body, 0); err != nil {
					return err
				}
				runtime.Room.RecordDeathResponseBody(dead.ObjectKey, body)
			}
		}
	}
	if err = runtime.cacheCurrentDungeonRoom(); err != nil {
		return err
	}
	s.logGameEvent(p, "帝国竞技场战斗死亡对象已确认", "active_character", actor.character.CharacterID, "object_key", request.RuntimeObjectKey, "skill_tree", actor.skillTree)
	_, err = s.syncCurrentTagTournamentRoundLocked(p, runtime)
	return err
}

// 现用2771860:27719BA..2771A2E进入Tag分支后写1466并直接return，
// 2771A31之后才是普通40。不能要求帝国先发一条原生不会发送的普通40。
// 1466是限定当前角色的死亡事件，不接受客户端HP/MP数值，也不据此通关或发奖。
func (s *Service) acceptCurrentTagTournamentDeath(p *gameSession, b *currentTagTournamentBattle, role int) error {
	if _, err := s.currentTagTournamentCombatActor(p, b); err != nil {
		return err
	}
	snapshot := b.run.Snapshot()
	if role != snapshot.Active {
		return fmt.Errorf("帝国竞技场只接受当前出战角色死亡")
	}
	if err := b.run.ApplyVitals(b.now(), role, 0, snapshot.Vitals[role].MP); err != nil {
		return err
	}
	s.logGameEvent(p, "帝国竞技场当前角色死亡已登记", "character", b.characters[role].CharacterID, "all_dead", b.run.Snapshot().Phase == tagtournament.Defeated)
	if b.run.Snapshot().Phase == tagtournament.Defeated {
		b.tagProjectionPending = false // 没有存活角色，不补发先前未完成的切人。
		b.settlementPending = snapshot.Cleared > 0
		return s.flushCurrentTagTournamentSettlement(p, b)
	}
	return nil
}

// 正常返回时必须先结算已确认进度。失败保留挑战供再次返回/时间队列重试。
func (s *Service) closeCurrentTagTournamentForReturn(p *gameSession) error {
	if p == nil || p.tagTournamentBattle == nil {
		return nil
	}
	b := p.tagTournamentBattle
	if !s.boundGameSessionCharacterCurrent(b.identity) || b.identity.character != p.selectedCharacterID {
		return fmt.Errorf("帝国竞技场返回会话已替换")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := b.owner.Close(ctx, b.run)
	if err != nil {
		return err
	}
	b.settlementPending = false
	b.mailAlarmPending = b.mailAlarmPending || len(result.MailIDs) > 0
	if b.mailAlarmPending {
		if err = s.sendMailboxAlarmForSession(p, p.selectedCharacterID, "帝国竞技场离场奖励邮件已提交"); err != nil {
			return err
		}
		b.mailAlarmPending = false
	}
	b.pendingRoundRewards, b.settlementProjectionPending, b.roundProjectionPending = nil, false, false
	rootRole := -1
	for i, ch := range b.characters {
		if ch.CharacterID == strconv.Itoa(int(p.selectedCharacterID)) {
			rootRole = i
			break
		}
	}
	if rootRole < 0 {
		return fmt.Errorf("帝国竞技场离场缺少原登录角色")
	}
	if err := s.sendCurrentTagTournamentHotkeys(p, b, rootRole); err != nil {
		return err
	}
	// 原生回城落位按原登录角色重建，先切回仍存活的本体；死亡本体由回城恢复。
	if b.characters[b.run.Snapshot().Active].CharacterID != strconv.Itoa(int(p.selectedCharacterID)) {
		for i, ch := range b.characters {
			if ch.CharacterID == strconv.Itoa(int(p.selectedCharacterID)) {
				if err := s.sendGameUpperRawClass(p, currentTagTournamentTagNotification, []byte{b.nativeRoleIDs[i]}, 0); err != nil {
					return err
				}
			}
		}
	}
	if b.prepared != nil {
		// 1394只临时加载bank，454同时恢复原登录角色的活动bank。
		// 不改会话身份或保存设置，避免备用角色键位带回普通城镇。
		row := b.prepared.characters[rootRole]
		if row.hotkeys == nil {
			return fmt.Errorf("帝国竞技场离场缺少原角色键位")
		}
		if err := s.sendGameUpperRawClass(p, s.currentAccountHotkeyNoticeID(), row.hotkeys.body, 0); err != nil {
			return err
		}
	}
	// 离场已恢复本体和键位，旧的战斗切人补发不再适用；否则会阻止回城释放。
	b.tagProjectionPending = false
	return nil
}

// 断线/选角在身份世代失效前提交邮件，不尝试向关闭的连接补发UI。
func (s *Service) retireCurrentTagTournamentBattle(p *gameSession) {
	if p == nil {
		return
	}
	p.tagTournamentReturnRoster = nil
	if p.tagTournamentBattle == nil {
		return
	}
	b := p.tagTournamentBattle
	var err error
	if b.owner == nil || b.run == nil || !s.boundGameSessionCharacterCurrent(b.identity) {
		p.tagTournamentBattle = nil
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	_, err = b.owner.Close(ctx, b.run)
	cancel()
	if err != nil {
		s.logGameEvent(p, "帝国竞技场结束连接时结算未完成", "error", err)
	}
	p.tagTournamentBattle = nil
}
