package dnfbridge

import (
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/channelcatalog"
	"longheng.io/server/internal/modules/dnf/dnfenum"
)

// 只用于首次角色列表之前的频道更正；普通换线不请求角色列表，不进入此握手。
// TCP监听来源保留在original中，场景、owner和LOGIN端点统一采用已公告的目标。
type currentColdChannelLanding struct {
	original channelcatalog.Channel
	account  string
	waiting  bool
}

func currentColdSpecialChannel(channel channelcatalog.Channel) bool {
	return isPVPChannel(channel) || channel.Type == currentTagTournamentChannelType ||
		channel.Type == dnfenum.RaidChannelType || channel.Type == dnfenum.HiddenRaidType || channel.Type == 42
}

// 超时空是可直接进入的专用团队频道。内部目录保持 type42，现用客户端登录/下载
// 投影为45；不能用“无EXIT交接”把它判成应回普通城镇的特殊频道。
func currentDirectFiendWarChannel(channel channelcatalog.Channel) bool {
	group := strings.TrimSpace(channel.Group)
	group = strings.Trim(group, "[]`")
	return channel.Type == 42 && strings.EqualFold(strings.TrimSpace(group), "fiendwar")
}

// 进行中团本只有本账号、同频道、同实例且仍在宽限期内的真实保留席位可恢复。
// 角色列表尚未选角时允许该账号的保留席位；选角前再核对具体角色。
func (s *Service) currentColdRaidSeatAvailable(p *gameSession, characterID uint16, now time.Time) bool {
	s.raidMu.Lock()
	defer s.raidMu.Unlock()
	account := s.accountIDForSession(p)
	for id, seat := range s.currentRaidDisconnects {
		if characterID != 0 && characterID != id || seat.AccountID != account || now.Before(seat.DisconnectedAt) || !now.Before(seat.ExpiresAt) {
			continue
		}
		state := s.raids[seat.RaidKey]
		if state == nil || state != seat.Raid || !state.Started || state.Battle == nil || state.Battle.InstanceID == "" || state.Battle.TimedOut || state.Battle.InstanceID != seat.InstanceID || state.Battle.ChannelID != p.channel.ID || state.Battle.ChannelType != p.channel.Type || state.Battle.MemberAccounts[id] != account || runtimeRaidMemberIndex(*state, id) < 0 {
			continue
		}
		b := state.Battle
		if b.PhaseCompleted && (b.Single || b.Phase != 0 || !currentRaidHasTwoPhaseProgram(b) || len(b.Configuration.PhaseLimitSeconds) != 2) {
			continue // 安图恩/卢克第一阶段休整仍属进行中，最终通关不恢复。
		}
		return true
	}
	return false
}

func (s *Service) currentColdOrdinaryChannel() (channelcatalog.Channel, error) {
	if c, err := s.loginChannel(); err == nil && channelcatalog.IsResidentChannel(c) && !currentColdSpecialChannel(c) {
		return c, nil
	}
	if catalog := s.currentCatalog(); catalog != nil {
		for _, c := range catalog.FilterForRequest("cain") {
			if channelcatalog.IsResidentChannel(c) && !currentColdSpecialChannel(c) && c.ID > 0 && c.ID <= currentOwnerChannelByteLimit {
				return c, nil
			}
		}
	}
	return channelcatalog.Channel{}, fmt.Errorf("重新登录没有可用的普通落点频道")
}

// GET_USERINFO是明确的新选角生命周期。客户端缓存了帝国/决斗/已失效团本频道时，
// 在任何角色绑定和场景包之前，完成一次新的CHANNELINFO -> LOGIN握手。
// 不在op24之后重放通知，不以IP或旧角色坐标猜频道，也不凭改房间号伪装普通频道。
func (s *Service) redirectCurrentColdSpecialChannel(p *gameSession, characterID uint16) (bool, error) {
	// 真实连接由startGameSessionEvents先初始化为世代1；第一次绑定角色后才大于1。
	// 决斗客户端已自行返回普通频道，保留已验路径，不追加二次握手。
	if s == nil || p == nil || s.options.currentClientVersion != "official-24.9" || !p.rosterRequested || p.characterGeneration > 1 || p.selectedCharacterID != 0 || isPVPChannel(p.channel) || currentDirectFiendWarChannel(p.channel) {
		return false, nil
	}
	if p.coldChannelLanding != nil {
		return p.coldChannelLanding.waiting, nil
	}
	if p.channelExitHandoff != nil {
		return false, nil // 本客户端刚完成明确的退出交接，不能用GET_USERINFO重判成冷启动。
	}
	if !currentColdSpecialChannel(p.channel) || !p.loginPacketSeen || !p.accountFromToken || strings.TrimSpace(p.accountID) == "" || !p.gameEndpointSuccessSent {
		return false, nil
	}
	if !isPVPChannel(p.channel) && p.channel.Type != currentTagTournamentChannelType && s.currentColdRaidSeatAvailable(p, characterID, s.gameplayNow()) {
		return false, nil
	}
	target, err := s.currentColdOrdinaryChannelForSession(p)
	if err != nil {
		return true, err
	}
	p.endpointHandshakeMu.Lock()
	original := p.channel
	p.coldChannelLanding = &currentColdChannelLanding{original: original, account: s.accountIDForSession(p), waiting: true}
	p.channel, p.residentChannel = target, target
	p.channelReconnect = false
	p.currentChannelResidentNoticeSent, p.gameEndpointSuccessSent = false, false
	p.connectionTownActorOwnerChannel = byte(target.ID)
	p.townActorOwnerChannel = byte(target.ID)
	p.endpointHandshakeMu.Unlock()
	if err := s.sendCurrentChannelResidentNotice(p, s.gameplayNow()); err != nil {
		if p.conn != nil {
			_ = p.conn.Close()
		}
		return true, err
	}
	s.logGameEvent(p, "特殊频道重登转入普通频道", "来源频道", original.ID, "目标频道", target.ID, "阶段", "等待新LOGIN应答后发送角色列表")
	return true, nil
}
