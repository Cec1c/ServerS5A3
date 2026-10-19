package dnfbridge

import (
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
)

const currentAuthenticatedChannelHandoffLifetime = 30 * time.Second

type currentAuthenticatedChannelHandoffKey struct {
	account string
	flow    [32]byte
}

type currentAuthenticatedChannelHandoff struct {
	source    channelcatalog.Channel
	ordinary  channelcatalog.Channel
	expiresAt time.Time
}

func currentChannelHandoffKey(p *gameSession) (currentAuthenticatedChannelHandoffKey, bool) {
	if p == nil || !p.loginPacketSeen || !p.accountFromToken || strings.TrimSpace(p.accountID) == "" || p.loginChannelFlowKey == [32]byte{} {
		return currentAuthenticatedChannelHandoffKey{}, false
	}
	return currentAuthenticatedChannelHandoffKey{account: p.accountID, flow: p.loginChannelFlowKey}, true
}

// EXIT是主动换线的明确意图，只有已认证且已选角色的连接可以登记。
// 交接按已验证令牌的摘要隔离，单次消费；新启动器令牌不能继承旧进程的退出。
// 在ACK写出前登记以覆盖本机快速重连；写出失败只撤销自己登记的同一条记录。
func (s *Service) rememberCurrentAuthenticatedChannelHandoff(p *gameSession) func() {
	noop := func() {}
	key, valid := currentChannelHandoffKey(p)
	if s == nil || s.options.currentClientVersion != "official-24.9" || !valid || p.selectedCharacterID == 0 {
		return noop
	}
	now := s.gameplayNow()
	entry := &currentAuthenticatedChannelHandoff{source: p.channel, expiresAt: now.Add(currentAuthenticatedChannelHandoffLifetime)}
	if ordinary, ok := s.currentOrdinaryChannelFromCatalog(p.channel); ok {
		entry.ordinary = ordinary
	} else if p.channelExitHandoff != nil {
		entry.ordinary, _ = s.currentOrdinaryChannelFromCatalog(p.channelExitHandoff.ordinary)
	}
	s.channelHandoffMu.Lock()
	if s.channelHandoffs == nil {
		s.channelHandoffs = make(map[currentAuthenticatedChannelHandoffKey]*currentAuthenticatedChannelHandoff)
	}
	for old, ticket := range s.channelHandoffs {
		if !now.Before(ticket.expiresAt) {
			delete(s.channelHandoffs, old)
		}
	}
	if _, replacing := s.channelHandoffs[key]; !replacing && len(s.channelHandoffs) >= 4096 {
		s.channelHandoffMu.Unlock()
		return noop
	}
	s.channelHandoffs[key] = entry
	s.channelHandoffMu.Unlock()
	return func() {
		s.channelHandoffMu.Lock()
		if s.channelHandoffs[key] == entry {
			delete(s.channelHandoffs, key)
		}
		s.channelHandoffMu.Unlock()
	}
}

func (s *Service) takeCurrentAuthenticatedChannelHandoff(p *gameSession) *currentAuthenticatedChannelHandoff {
	key, valid := currentChannelHandoffKey(p)
	if !valid {
		return nil
	}
	s.channelHandoffMu.Lock()
	defer s.channelHandoffMu.Unlock()
	entry := s.channelHandoffs[key]
	delete(s.channelHandoffs, key)
	if entry == nil || !s.gameplayNow().Before(entry.expiresAt) {
		return nil
	}
	return entry
}

// 交接在目标解析或应答写出失败时放回，供同账号、同启动令牌的快速重连重试。
// 若期间已登记新的 EXIT，以新交接为准，不能用旧记录覆盖。
func (s *Service) restoreCurrentAuthenticatedChannelHandoff(p *gameSession, entry *currentAuthenticatedChannelHandoff) {
	key, valid := currentChannelHandoffKey(p)
	if s == nil || !valid || entry == nil || !s.gameplayNow().Before(entry.expiresAt) {
		return
	}
	s.channelHandoffMu.Lock()
	defer s.channelHandoffMu.Unlock()
	if s.channelHandoffs == nil {
		s.channelHandoffs = make(map[currentAuthenticatedChannelHandoffKey]*currentAuthenticatedChannelHandoff)
	}
	if _, replaced := s.channelHandoffs[key]; !replaced {
		s.channelHandoffs[key] = entry
	}
}

func (s *Service) restoreCurrentGameChannelHandoff(p *gameSession) {
	if s == nil || p == nil {
		return
	}
	s.restoreCurrentAuthenticatedChannelHandoff(p, p.channelExitHandoff)
}

func (s *Service) currentOrdinaryChannelFromCatalog(candidate channelcatalog.Channel) (channelcatalog.Channel, bool) {
	catalog := s.currentCatalog()
	if catalog == nil {
		return channelcatalog.Channel{}, false
	}
	actual, found := catalog.Channel(candidate.ID)
	return actual, found && actual.ServerID == candidate.ServerID && actual.Type == candidate.Type &&
		actual.ID > 0 && actual.ID <= currentOwnerChannelByteLimit && actual.Port > 0 &&
		channelcatalog.IsResidentChannel(actual) && !currentColdSpecialChannel(actual)
}

func (s *Service) currentColdOrdinaryChannelForSession(p *gameSession) (channelcatalog.Channel, error) {
	if p != nil {
		if ticket := p.channelExitHandoff; ticket != nil {
			if ordinary, ok := s.currentOrdinaryChannelFromCatalog(ticket.ordinary); ok {
				return ordinary, nil
			}
		}
		if ordinary, ok := s.currentOrdinaryChannelFromCatalog(p.residentChannel); ok {
			return ordinary, nil
		}
		if catalog := s.currentCatalog(); catalog != nil {
			if candidate, found := catalog.ResidentFor(p.channel); found {
				if ordinary, ok := s.currentOrdinaryChannelFromCatalog(candidate); ok {
					return ordinary, nil
				}
			}
		}
	}
	return s.currentColdOrdinaryChannel()
}

// 当前客户端1FAFC36/1FAFC3D会忽略已接受LOGIN之后的成功应答。
// 必须在首次成功之前更正CHANNELINFO，再对新的LOGIN只发送一次目标类型。
// 这里不提前发送角色列表，保留客户端随后GET_USERINFO或UDP登记的真实生命周期。
func (s *Service) redirectCurrentSpecialBeforeFirstLoginSuccess(p *gameSession) (bool, error) {
	if s == nil || p == nil || s.options.currentClientVersion != "official-24.9" || p.selectedCharacterID != 0 || p.characterGeneration > 1 {
		return false, nil
	}
	if _, valid := currentChannelHandoffKey(p); !valid {
		return false, nil
	}
	p.endpointHandshakeMu.Lock()
	if p.gameEndpointSuccessSent || !p.currentChannelResidentNoticeSent || p.coldChannelLanding != nil {
		p.endpointHandshakeMu.Unlock()
		return false, nil
	}
	if ticket := s.takeCurrentAuthenticatedChannelHandoff(p); ticket != nil {
		p.channelExitHandoff = ticket
	}
	handoff := p.channelExitHandoff
	if currentDirectFiendWarChannel(p.channel) {
		p.endpointHandshakeMu.Unlock()
		return false, nil
	}
	sameEmpireReturn := p.channelExitHandoff != nil && p.channel.Type == currentTagTournamentChannelType &&
		p.channelExitHandoff.source.ServerID == p.channel.ServerID && p.channelExitHandoff.source.ID == p.channel.ID && p.channelExitHandoff.source.Type == p.channel.Type
	if isPVPChannel(p.channel) || !currentColdSpecialChannel(p.channel) || p.channelExitHandoff != nil && !sameEmpireReturn ||
		p.channel.Type != currentTagTournamentChannelType && s.currentColdRaidSeatAvailable(p, 0, s.gameplayNow()) {
		p.endpointHandshakeMu.Unlock()
		return false, nil
	}
	target, err := s.currentColdOrdinaryChannelForSession(p)
	if err != nil {
		s.restoreCurrentAuthenticatedChannelHandoff(p, handoff)
		p.endpointHandshakeMu.Unlock()
		return true, err
	}
	original := p.channel
	p.coldChannelLanding = &currentColdChannelLanding{original: original, account: s.accountIDForSession(p), waiting: true}
	p.channel, p.residentChannel = target, target
	p.channelReconnect = false
	p.currentChannelResidentNoticeSent = false
	p.connectionTownActorOwnerChannel, p.townActorOwnerChannel = byte(target.ID), byte(target.ID)
	p.endpointHandshakeMu.Unlock()
	if err := s.sendCurrentChannelResidentNotice(p, s.gameplayNow()); err != nil {
		s.restoreCurrentAuthenticatedChannelHandoff(p, handoff)
		if p.conn != nil {
			_ = p.conn.Close()
		}
		return true, err
	}
	reason := "重新登录没有主动换线交接"
	if sameEmpireReturn {
		reason = "帝国退出后同频道回接转普通来源"
	}
	s.logGameEvent(p, "首次LOGIN成功前更正特殊频道", "来源频道", original.ID, "目标频道", target.ID, "原因", reason)
	return true, nil
}
