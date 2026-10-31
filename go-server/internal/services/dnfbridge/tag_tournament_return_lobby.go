package dnfbridge

import (
	"context"
	"fmt"

	"longheng.io/server/internal/modules/dnf/tagtournament"
)

// 真实15可以确认已写出的帝国回城；1388必须等城镇角色/HUD重建完成，
// 不能为了提前恢复队伍而紧跟24发出。普通场景及无效归属仍走原流程。
func (s *Service) confirmCurrentTagTournamentEnterSelectReturn(p *gameSession) error {
	if !s.isCurrentTagTournamentChannel(p) || !p.backToVillageEnterSelectPending || !p.accountFromToken || !p.loginPacketSeen {
		return nil
	}
	b := p.tagTournamentReturnRoster
	if b == nil || b.prepared == nil || b.prepared.runtime == nil ||
		!b.prepared.runtime.townReturnOp24Sent || !b.prepared.runtime.townReturnPending ||
		p.dungeon.runtime != nil && b.prepared.runtime != p.dungeon.runtime ||
		!s.boundGameSessionCharacterCurrent(b.identity) || b.identity.character != p.selectedCharacterID {
		return nil
	}
	if err := s.confirmCurrentTagTournamentRosterTownReturn(p); err != nil {
		return err
	}
	if currentTagTournamentLobbyInteractionReady(p) {
		if err := s.sendCurrentTagTournamentConfirmedReturnRoster(p); err != nil {
			return err
		}
		p.backToVillageEnterSelectPending = false
		s.logGameEvent(p, "帝国竞技场回城首条选图请求已确认", "char_id", p.selectedCharacterID)
	}
	return nil
}

// 这里只登记24对应的恢复归属，不写1388。现用1460F90会调用145A4C0销毁
// 已有双角色holder及备用actor；24的socket写成功并不代表客户端已销毁旧图。
// 调用方持dungeon.mu；保存本场凭据跨过运行态释放，等待真实城镇请求。
func (s *Service) queueCurrentTagTournamentReturnRoster(p *gameSession, runtime *runtimeDungeonState) error {
	if !s.isCurrentTagTournamentChannel(p) || p.tagTournamentBattle == nil {
		return nil
	}
	b := p.tagTournamentBattle
	if b.returnRosterSent || p.tagTournamentReturnRoster == b {
		return nil
	}
	if runtime == nil || b.prepared == nil || !b.prepared.entered || b.prepared.runtime != runtime ||
		p.dungeon.runtime != runtime || !s.boundGameSessionCharacterCurrent(b.identity) ||
		b.identity.character != p.selectedCharacterID || !runtime.townReturnPending ||
		!runtime.townReturnOp24Sent || runtime.townReturnTransition.TownID != 48 {
		return fmt.Errorf("帝国竞技场回城队伍恢复缺少当前角色的完整24")
	}
	phase := b.run.Snapshot().Phase
	if phase != tagtournament.Abandoned && phase != tagtournament.Defeated && phase != tagtournament.Finished {
		return fmt.Errorf("帝国竞技场战斗未结束，不能恢复休息室分组")
	}
	rows, server, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	view, err := b.owner.Lobby(ctx, p.accountID)
	if err != nil {
		return err
	}
	if !view.Registered {
		return fmt.Errorf("帝国竞技场回城队伍已失效")
	}
	body, err := buildCurrentTagTournamentSavedRosterBody(view.Characters, rows, server)
	if err != nil {
		return err
	}
	p.tagTournamentReturnRoster = b
	s.logGameEvent(p, "帝国竞技场回城队伍恢复等待真实城镇确认", "server", server, "first_selector", body[1], "second_selector", body[3])
	return nil
}

// 仅由15、35等真实城镇请求在原角色/HUD已经恢复之后调用；不使用定时器
// 猜测加载结束。失败保留恢复凭据，重试不再发24或重建战斗角色。
func (s *Service) sendCurrentTagTournamentConfirmedReturnRoster(p *gameSession) error {
	if p == nil || p.tagTournamentReturnRoster == nil {
		return nil
	}
	b := p.tagTournamentReturnRoster
	if !s.isCurrentTagTournamentChannel(p) || !s.boundGameSessionCharacterCurrent(b.identity) || b.identity.character != p.selectedCharacterID {
		p.tagTournamentReturnRoster = nil
		return nil
	}
	if b.prepared == nil || b.prepared.runtime == nil || b.run == nil {
		return nil
	}
	runtime := b.prepared.runtime
	phase := b.run.Snapshot().Phase
	if !runtime.townReturnPending || !runtime.townReturnOp24Sent || runtime.townReturnTransition.TownID != 48 ||
		phase != tagtournament.Abandoned && phase != tagtournament.Defeated && phase != tagtournament.Finished {
		return nil
	}
	if !currentTagTournamentLobbyInteractionReady(p) || p.tagTournamentBattle != nil {
		return nil
	}
	rows, server, err := s.currentTagTournamentSentRoster(p)
	if err != nil {
		return err
	}
	return s.sendCurrentTagTournamentSavedRoster(p, rows, server)
}

// 现用客户端中途42/24回休息室后可能不发送35、36或15，下一条城镇意图
// 就是1467。只确认已经由本连接请求、实际写出24且领域已关闭的帝国回城；
// 战斗中打开队伍框不能授权离场。复用待决回城和可重试HUD链，不制造通关。
func (s *Service) confirmCurrentTagTournamentRosterTownReturn(p *gameSession) error {
	if !s.isCurrentTagTournamentChannel(p) || p.selectedCharacterID == 0 {
		return nil
	}
	p.dungeon.mu.Lock()
	runtime := p.dungeon.runtime
	if runtime == nil {
		pending := p.confirmedDungeonReturnStatePending
		p.dungeon.mu.Unlock()
		if pending {
			return s.ensureCurrentConfirmedDungeonReturnPlayerState(p, "帝国队伍请求续办已确认回城")
		}
		return nil
	}
	b := p.tagTournamentBattle
	if runtime.Session == nil || b == nil || b.prepared == nil || !b.prepared.entered ||
		b.prepared.runtime != runtime || !dungeonRuntimeOwnsCharacter(runtime, p.selectedCharacterID) ||
		!s.boundGameSessionCharacterCurrent(b.identity) || !runtime.townReturnPending || !runtime.townReturnOp24Sent ||
		(runtime.townReturnRequestMsgID != 42 && runtime.townReturnRequestMsgID != 132) ||
		runtime.townReturnTransition.TownID != 48 {
		p.dungeon.mu.Unlock()
		return nil
	}
	phase := b.run.Snapshot().Phase
	if phase != tagtournament.Abandoned && phase != tagtournament.Defeated && phase != tagtournament.Finished {
		p.dungeon.mu.Unlock()
		return nil
	}
	transition := cloneCurrentDungeonTownTransition(runtime.townReturnTransition)
	origin := runtime.townReturnOrigin
	characterID := p.selectedCharacterID
	p.dungeon.mu.Unlock()

	// 1467只确认已写出的休息室落点；位置复用进图前冻结的35，缺失时使用
	// 原24已验证的城镇投影。先留下位置，保证HUD写失败、运行态释放后的重试。
	position := currentTownPositionSnapshot{
		CharacterID: characterID, TownID: transition.TownID, AreaID: transition.AreaID,
		PositionX: uint16(transition.PositionX), PositionY: uint16(transition.PositionY),
		MovementCode: transition.Direction, PositionValid: true,
	}
	if origin.CharacterID == characterID && origin.PositionValid && origin.TownID == transition.TownID && origin.AreaID == transition.AreaID {
		position = origin
	}
	p.townMu.Lock()
	resetCurrentTownEnterSelectSceneLocked(p)
	p.townPositionSnapshot = position
	p.townMu.Unlock()
	return s.commitPendingDungeonReturnForSceneRequest(p, "帝国1467确认已发送的休息室回城")
}
