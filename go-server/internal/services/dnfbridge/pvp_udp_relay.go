package dnfbridge

import (
	"fmt"
	"longheng.io/server/internal/modules/dnf/party"
	"time"
)

// pvpRelayPortQuarantine 是 PVP 中继端口被释放后的静默期：这段时间内不再把它分给新腿。
//
// 现场（2026-10-31 90CN，10 局实测）：决斗结束约 20 秒内再匹配，客户端会黑屏约
// **12 秒**后主动发 op52（座位置 0xfe）退座、随后回广场；隔 2 分钟以上再匹配则正常。
// 判据链：
//
//	服务端下发逐字节一致（op41/op42/op45 的 mapIndex 一致、座位表/RP/HP 都对）
//	中继两局都在正常转发（成功局 1680 个数据报、失败局 331 个）
//	失败局客户端**从未**发 type=480（PVP 延迟上报）与 type=38（使用技能）⇒ 没进过战斗场景
//	成功与失败**只**和"距上一局多久"相关（≤72 秒全败、≥167 秒全成）
//
// 唯一跨局完全不变的量是**通告给客户端的对端端点**：端口池"取最小空闲"，所以连续两局
// 都拿到 30001/30002，客户端看到的 (对端 IP, 对端端口) 一字不差 ⇒ 它的 UDP 会话四元组
// 跨局不变，新一局的数据会被当成上一局的残留。换端口就等于换一条全新会话。
//
// ★ 只是"优先避开"，不是硬性禁用：端口池被抽干时 newLegLocked 会忽略静默期再扫一遍。
const pvpRelayPortQuarantine = 180 * time.Second

func (r *currentPartyUDPRelay) closePVPRoom(id uint16) {
	if r == nil {
		return
	}
	r.mu.Lock()
	var legs []*currentPartyUDPRelayLeg
	for k, v := range r.legs {
		if k.pvp && k.partyID == id {
			delete(r.legs, k)
			delete(r.usedPorts, int(v.port))
			r.notePVPPortReleasedLocked(int(v.port))
			legs = append(legs, v)
		}
	}
	r.mu.Unlock()
	for _, v := range legs {
		_ = v.conn.Close()
	}
	r.logLegClosure(legs, "决斗房间退出")
}
func (s *Service) syncPVPRelay(room *currentPVPRoom, peers []*currentPVPMember) error {
	r := s.partyUDPRelay
	if r == nil || !r.Enabled() || len(peers) < 2 {
		return nil
	}
	bindings := make([]currentPartyUDPRelayBinding, 0, len(peers))
	for _, p := range peers {
		address := currentPVPObservedIPv4(p.session)
		if address == [4]byte{} {
			return fmt.Errorf("决斗中继缺少真实TCP地址")
		}
		bindings = append(bindings, currentPartyUDPRelayBinding{characterID: p.characterID, generation: p.session.characterGeneration, address: address, advertisedPort: currentPartyPeerPort(p.session)})
	}
	if !r.syncGroup(true, room.id, bindings) {
		return fmt.Errorf("决斗中继端口不足或成员失效")
	}
	return nil
}
func (s *Service) pvpEndpointBody(room *currentPVPRoom, target, actor *currentPVPMember, ep party.PeerEndpoint) []byte {
	r := s.partyUDPRelay
	if r != nil && r.Enabled() {
		r.mu.Lock()
		leg := r.legs[currentPartyUDPRelayLegKey{pvp: true, partyID: room.id, ownerID: target.characterID, peerID: actor.characterID}]
		if leg != nil {
			ep.IPv4 = r.advertise
			ep.Port = leg.port
		}
		r.mu.Unlock()
	}
	return s.buildPartyPeerEndpointInfoBody([]party.PeerEndpoint{ep})
}

func (s *Service) refreshPVPRelayAfterLeave(room *currentPVPRoom) {
	if room == nil || s.partyUDPRelay == nil {
		return
	}
	peers := []*currentPVPMember{}
	for _, p := range room.members {
		if p != nil {
			peers = append(peers, p)
		}
	}
	if len(peers) < 2 {
		s.partyUDPRelay.closePVPRoom(room.id)
		return
	}
	if err := s.syncPVPRelay(room, peers); err != nil {
		s.logGameEvent(nil, "决斗中继离房同步失败", "room_id", room.id, "error", err)
	}
}
