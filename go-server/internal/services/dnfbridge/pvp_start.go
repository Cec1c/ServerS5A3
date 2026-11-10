package dnfbridge

import (
	"fmt"
	"math/rand/v2"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func currentPVPStartMapOrder(source initialEquipmentTextSource) ([]int64, error) {
	text, err := source.ReadText("etc/pvpmapbasicdata.etc")
	if err != nil {
		return nil, err
	}
	doc, err := dnfpvf.Parse("etc/pvpmapbasicdata.etc", text)
	if err != nil {
		return nil, err
	}
	order := doc.Ints("pvp map order")
	// Native map indices address this vector directly; 50000 is the random row.
	if len(order) < 2 || len(order) > 255 || order[0] != 50000 {
		return nil, fmt.Errorf("PVP map order has invalid length %d", len(order))
	}
	seen := make(map[int64]bool, len(order))
	for _, id := range order {
		if id <= 0 || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate PVP map %d", id)
		}
		seen[id] = true
	}
	return order, nil
}

// ensureCurrentPVPMapOrderLocked 保证 `s.pvpMapOrder` 已从 PVF 加载（受 `s.pvpMu` 保护）。
func (s *Service) ensureCurrentPVPMapOrderLocked() error {
	if s.pvpMapOrder != nil {
		return nil
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err == nil {
		s.pvpMapOrder, err = currentPVPStartMapOrder(archive)
	}
	return err
}

// currentPVPStartMapIndexLocked 返回一张**真实可用**的决斗地图索引（1..len-1）。
//
// ★ 索引 0 在 PVF `etc/pvpmapbasicdata.etc` 里对应 `50000`「随机行」，客户端拿到它
// 不会预加载任何地图。所以**房间信息 op41 必须发真实索引**，否则客户端要等到 op45
// 才开始加载决斗地图；而服务端此前下发的 op23/op3/op357 都落在城镇场景里，
// 场景重建后整张 actor 表被清空 —— 现场表现就是「匹配进决斗房间看不到对方」。
//
// 2026-10-31 实测（90CN）：匹配路径 op41 的 mapIndex 是 0、op45 却是 4/3（两者不一致），
// 玩家看不到对方；手动开房由玩家选了地图，op41 与 op45 一致，一切正常。
func (s *Service) currentPVPStartMapIndexLocked() (int, error) {
	if err := s.ensureCurrentPVPMapOrderLocked(); err != nil {
		return 0, err
	}
	if len(s.pvpMapOrder) < 2 {
		return 0, fmt.Errorf("PVP map order has %d entries", len(s.pvpMapOrder))
	}
	return 1 + rand.IntN(len(s.pvpMapOrder)-1), nil
}

// resolveCurrentPVPRoomMapIndexLocked 在建房时就把「这一局用哪张图」定下来。
//
// ★ op41（房间信息）与 op45（开始决斗）必须报同一张图，而且都不能是 0。
// 索引 0 在 PVF 里是 `50000` 随机行，客户端拿到 0 不会预加载决斗地图，要等服务端
// 发 op45 才开始加载；而场景重建会把此前下发的 actor 全清掉 —— 现场表现就是
// 「匹配进决斗房间看不到对方」。
//
// 手动开房由玩家选了具体地图（非 0）→ 原样保留；自动匹配 / 玩家选「随机」→ 这里就抽签，
// 保证 op41、op42、op45 三条包报的是同一个索引。
func (s *Service) resolveCurrentPVPRoomMapIndexLocked(requested int16) int16 {
	if requested != 0 {
		return requested
	}
	index, err := s.currentPVPStartMapIndexLocked()
	if err != nil {
		// 地图表取不到时保持 0：op45 启动前还会再取一次，取不到就以 19 拒绝，
		// 不在建房阶段把玩家踢回大厅。
		return 0
	}
	return int16(index)
}

func (s *Service) startCurrentPVPRoomLocked(room *currentPVPRoom, owner *gameSession) error {
	reject := func(code byte, reason string) error {
		s.logGameEvent(owner, "game-pvp-start-rejected", "room_id", room.id, "reason", reason)
		return s.sendGameUpperFailure(owner, uint16(dnfenum.CmdPacketSetPVPReadyState), code)
	}
	if room.started || room.seatFor(owner) != int(room.manager) {
		return nil
	}
	if room.isPractice() {
		// sub_1FD8F50 starts practice locally via manager+164(state 10).
		// It does not send C2S53 or use the ordinary op45 match loader.
		return reject(19, "practice_mode_has_separate_start")
	}
	if room.battleMode == 8 {
		if len(room.totalCombatants()) != 2 {
			return reject(19, "total_match_requires_two_owners")
		}
		if err := s.refreshCurrentPVPTotalRoomLocked(room); err != nil {
			s.logGameEvent(owner, "game-pvp-total-roster-unavailable", "error", err)
			return reject(19, "total_match_roster_unavailable")
		}
	}
	combatants, red, blue := 0, 0, 0
	for seat, member := range room.members {
		if member == nil || room.seatState[seat] == 3 || room.seatState[seat] == 4 {
			continue
		}
		combatants++
		if seat != int(room.manager) && room.ready[seat] == 0 {
			return reject(22, "player_not_ready")
		}
		if room.seatState[seat] == 1 {
			red++
		}
		if room.seatState[seat] == 2 {
			blue++
		}
	}
	if combatants < 2 {
		return reject(22, "fewer_than_two_combatants")
	}
	if (room.battleMode == 2 || room.battleMode == 3 || room.battleMode == 5 || room.battleMode == 8) && (red == 0 || red != blue) {
		return reject(14, "unbalanced_teams")
	}
	if err := s.ensureCurrentPVPMapOrderLocked(); err != nil {
		return reject(19, "runtime_pvp_map_list_unavailable")
	}
	selected := int(room.request.mapIndex)
	if selected < 0 || selected >= len(s.pvpMapOrder) {
		return reject(19, "map_index_out_of_range")
	}
	if selected == 0 {
		var err error
		selected, err = s.currentPVPStartMapIndexLocked()
		if err != nil {
			return reject(19, "runtime_pvp_map_list_unavailable")
		}
	}
	// 回写房间：op42（房间状态）与赛后广播都读 `room.request.mapIndex`，
	// 不回写就会出现「op45 报图 A、op42 报 0」的不一致。
	room.request.mapIndex = int16(selected)
	room.match = &currentPVPMatch{winner: 0xff, startedAt: s.gameplayNow()}
	if room.battleMode == 3 {
		room.match.relay = newCurrentPVPRelayMatch()
	}
	if room.battleMode == 8 {
		room.match.total = newCurrentPVPTotalMatch()
	}
	room.started = true
	room.ready[room.manager] = 1
	for _, member := range room.members {
		if member == nil {
			continue
		}
		if err := s.sendGameUpperRawClass(member.session, 44, []byte{room.manager, 1}, 0); err != nil {
			return err
		}
		// sub_1D68000 reads precisely u8 selected-map-index + u8 battle-mode.
		if err := s.sendGameUpperRawClass(member.session, 45, []byte{byte(selected), room.battleMode}, 0); err != nil {
			return err
		}
	}
	s.broadcastCurrentPVPPacketLocked(room, nil, currentPVPRoomStateMsgID,
		buildCurrentPVPRoomStateBody(room.id, room.state(), room.manager, room.request.mapIndex, room.battleMode, 0))
	s.logGameEvent(owner, "game-pvp-start-complete", "room_id", room.id, "map_index", selected,
		"map_id", s.pvpMapOrder[selected], "battle_mode", room.battleMode, "combatants", combatants,
		"sequence", "op44_ready_op45_start_op42_started")
	if room.match.total != nil {
		return s.scheduleCurrentPVPTotalPhaseLocked(room, false)
	}
	return nil
}

func (s *Service) handleCurrentPVPMatchSignal(session *gameSession, opcode uint16, body []byte) error {
	if session == nil || !isPVPChannel(session.channel) {
		return nil
	}
	valid := len(body) == 0
	if opcode == uint16(dnfenum.CmdPacketConnectP2pPVP) {
		valid = len(body) > 0 && body[0] <= currentPVPRoomSeatCount && len(body) == 1+2*int(body[0])
		if valid {
			var seen [currentPVPRoomSeatCount]bool
			for i := 1; i < len(body); i += 2 {
				seat := body[i]
				if seat >= currentPVPRoomSeatCount || body[i+1] > 1 || seen[seat] {
					valid = false
					break
				}
				seen[seat] = true
			}
		}
	}
	if !valid {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	if room := session.pvpRoom; room != nil && room.started {
		if s.pvpRooms[room.id] != room || room.channelID != session.channel.ID || room.seatFor(session) < 0 {
			return nil
		}
		if room.battleMode == 3 {
			return s.handleCurrentPVPRelayLoadLocked(room, session, opcode)
		}
		if room.battleMode == 8 {
			return s.handleCurrentPVPTotalLoadLocked(room, session, opcode)
		}
		// Normal matches advance locally after op45; league-specific replies
		// and generic unsupported-command failures interrupt the native loader.
		s.logGameEvent(session, "game-pvp-match-signal", "room_id", room.id, "opcode", opcode, "body_len", len(body))
	}
	return nil
}
