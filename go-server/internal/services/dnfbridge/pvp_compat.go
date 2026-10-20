package dnfbridge

import (
	"context"
	"fmt"
	"longheng.io/server/internal/modules/dnf/party"
	"net"
)

func currentPVPWeaknessValue(stats map[string]int64) byte {
	v := stats["stamina"]
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return byte(v)
}
func currentPVPObservedIPv4(p *gameSession) [4]byte {
	var out [4]byte
	if p != nil && p.conn != nil {
		if a, ok := p.conn.RemoteAddr().(*net.TCPAddr); ok {
			copy(out[:], a.IP.To4())
		}
	}
	return out
}
func (s *Service) sendPVPItemLocksIfAny(p *gameSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	body, ok := s.buildCurrentItemLockSnapshotForSession(ctx, p)
	if !ok || len(body) <= 2 {
		return nil
	}
	return s.sendCurrentSelectItemLockSnapshot(p, "pvp_after_self_mode1", body)
}
func (s *Service) sendPVPActorVisibility(target, actor *gameSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	_, _, c, ok := s.selectedCharacterForEnter(ctx, actor)
	if !ok {
		return fmt.Errorf("对战外观角色不存在")
	}
	v := int64(currentActorDefaultUserStateBits)
	if x, exists := c.Stats["user_state_bits"]; exists {
		v = x
	}
	if v < 0 || v > 255 {
		return fmt.Errorf("对战外观字段越界")
	}
	return s.sendGameUpperRawClass(target, currentActorUserStateMsgID, buildCurrentActorUserStateBody(actor.selectedCharacterID, byte(v)), 0)
}
func (s *Service) buildPVPDirectedEndpoint(room *currentPVPRoom, target, actor *currentPVPMember, ep party.PeerEndpoint) []byte {
	return s.pvpEndpointBody(room, target, actor, ep)
}

func (s *Service) sendPVPClass0Packet(p *gameSession, op uint16, body []byte, source string) error {
	return s.sendGameUpperRawClass(p, op, body, 0)
}
