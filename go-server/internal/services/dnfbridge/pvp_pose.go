package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	pvp "longheng.io/server/internal/modules/dnf/pvp"
)

// 现用1EFF910发送u16任务ID和u8姿态。47结果已发完后才接收；
// settlementPending是战斗冻结标记，在结果阶段仍为true，不能用它挡掉姿态。
func (s *Service) observeCurrentPVPWinPose(g *gameSession, body []byte) error {
	if g == nil || len(body) != 3 || body[2] > 2 {
		return nil
	}
	id := binary.LittleEndian.Uint16(body)
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := g.pvpRoom
	if room == nil || room.match == nil || room.match.total != nil || !room.ranked1v1() || !room.match.resultSent {
		return nil
	}
	seat := room.seatFor(g)
	if seat < 0 || seat > 1 || room.match.winner != byte(seat) || room.members[seat].characterID != g.selectedCharacterID {
		return nil
	}
	snap := room.match.settled[seat]
	if snap == nil {
		return nil
	}
	valid := false
	catalog, err := s.loadPVPDailyCatalog()
	if err != nil {
		return err
	}
	for tier, row := range snap.Rows {
		if row.ID != id {
			continue
		}
		for _, m := range catalog.Pools[tier] {
			if m.ID == id && m.Condition == "win pose" && m.Parameter == 3 {
				valid = true
			}
		}
	}
	if !valid {
		return nil
	}
	if _, seen := room.match.poseReports[seat][id]; seen {
		return nil
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("胜利姿态仓储不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	state, err := pvp.RecordWinPose(ctx, repos, catalog, g.accountID, strconv.Itoa(int(g.selectedCharacterID)), pvp.WinPose{
		MatchKey: fmt.Sprintf("pvp:%d:%d:%p", room.id, room.match.startedAt.UnixNano(), room.match), MissionID: id, Pose: body[2],
		StartedAt: room.match.startedAt, CompletedAt: room.match.completedAt, ReportedAt: s.gameplayNow(),
	})
	if err != nil {
		return err
	}
	if !state.Accepted && !state.Replayed {
		return nil
	}
	if state.Replayed {
		if err := s.sendCurrentPVPMissions(g, 323, state.Rows, state.NormalRows); err != nil {
			return err
		}
	} else if len(state.ChangedRows) > 0 {
		if err := s.sendCurrentPVPMissions(g, 578, state.ChangedRows); err != nil {
			return err
		}
	}
	if err := s.sendGameUpperRawClass(g, 14, buildCurrentItemUpdateBody(0, []currentItemListEntry{currentPVPEmblemWalletEntry(statU32(state.Character, pvp.EmblemsKey, 0))}), 0); err != nil {
		return err
	}
	room.match.settled[seat] = &state.Snapshot
	room.members[seat].currencySlot2 = statU32(state.Character, pvp.EmblemsKey, 0)
	if room.match.poseReports[seat] == nil {
		room.match.poseReports[seat] = map[uint16]byte{}
	}
	room.match.poseReports[seat][id] = body[2]
	s.logGameEvent(g, "决斗胜利姿态计数", "room_id", room.id, "mission_id", id, "pose", body[2], "进度", state.Rows, "奖励印章", state.EmblemsGranted, "重放", state.Replayed)
	return nil
}
