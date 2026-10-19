package dnfbridge

import (
	"fmt"
	"time"
)

// 调用方持有当前会话 raidProjection.mu；发送失败后要求重连，禁止重放半帧。
func (s *Service) sendCurrentFiendWarProjectionLocked(p *gameSession, state runtimeRaidState, announce bool) error {
	b := state.Battle
	if b == nil || b.FiendWar == nil || !s.currentRaidAuthenticated(p) || b.MemberAccounts[p.selectedCharacterID] != s.accountIDForSession(p) || b.ChannelID != p.channel.ID || b.ChannelType != p.channel.Type {
		return fmt.Errorf("超时空战况投影归属不符")
	}
	if p.raidProjection.failed {
		return fmt.Errorf("超时空投影发送中断，等待重连")
	}
	latest, err := s.latestCurrentRaidProjectionState(p, state)
	if err != nil {
		return err
	}
	state = latest
	b = state.Battle
	id := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: 0, CharID: p.selectedCharacterID, Generation: p.characterGeneration}
	fresh := !p.raidProjection.initialized || p.raidProjection.identity != id
	if fresh {
		p.fiendWarProjection = currentFiendWarProjection{}
	}
	view := currentFiendWarProjection{RaidKey: state.RaidKey, Battle: b.FiendWar, Members: append([]runtimeRaidMemberState(nil), state.Members...), Deadline: b.Deadline}
	packets, err := buildCurrentFiendWarBoard(view, p.fiendWarProjection, p.selectedCharacterID, time.Now())
	if err != nil {
		return err
	}
	if fresh || announce {
		initial := []currentFiendWarWire{{currentFiendWarInit, nil}}
		if !b.TimedOut {
			initial = append([]currentFiendWarWire{{587, []byte{2, 0}}, {currentFiendWarStateNotice, []byte{2, 0}}}, initial...)
		}
		packets = append(initial, packets...)
	}
	// 失败提示由终局结果独占：601先提交类型3，随后1484状态6读取它。
	// 状态4是结算完成UI入口，不能在每次战况刷新时当作失败通知。
	for _, q := range packets {
		if err := s.sendGameUpperRawClass(p, q.opcode, q.body, 0); err != nil {
			p.raidProjection.failed = true
			return err
		}
	}
	view.Battle = b.FiendWar.Clone()
	p.fiendWarProjection = view
	p.raidProjection.identity, p.raidProjection.initialized, p.raidProjection.version = id, true, b.ProgressVersion
	p.raidProjection.startTransitionSent = true
	return nil
}
