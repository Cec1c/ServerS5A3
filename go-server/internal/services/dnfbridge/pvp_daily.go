package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) loadPVPDailyCatalog() (*dnfpvp.Catalog, error) {
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.pvpDailyCatalog != nil {
		return s.pvpDailyCatalog, nil
	}
	archive, err := s.initialEquipmentArchiveLocked()
	if err != nil {
		return nil, err
	}
	s.pvpDailyCatalog, err = dnfpvp.Load(archive)
	return s.pvpDailyCatalog, err
}

func buildCurrentPVPMissions(groups ...[]dnfpvp.Row) ([]byte, error) {
	count := 0
	for _, rows := range groups {
		if len(rows) > math.MaxUint8-count {
			return nil, fmt.Errorf("PVP mission list exceeds native u8 count")
		}
		count += len(rows)
	}
	var w packetWriter
	w.writeByte(byte(count))
	seen := make(map[uint16]bool, count)
	for _, rows := range groups {
		for _, row := range rows {
			if row.ID == 0 || row.ID > math.MaxInt16 || seen[row.ID] || row.Target == 0 || row.Progress > row.Target {
				return nil, fmt.Errorf("invalid PVP mission row %d", row.ID)
			}
			seen[row.ID] = true
			w.writeUint16(row.ID)
			w.writeUint32(row.Progress)
			w.writeUint32(row.Target)
		}
	}
	return w.bytes(), nil
}

func (s *Service) sendCurrentPVPMissions(session *gameSession, opcode uint16, groups ...[]dnfpvp.Row) error {
	body, err := buildCurrentPVPMissions(groups...)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, opcode, body, 0)
}

// sub_1D5A470 consumes 51 bytes in ordinary personVsPerson mode. Its extra
// three actor rows are exclusive to totalMatch. It does not carry daily tasks.
func buildCurrentPVPCertification(state dnfpvp.Snapshot, catalog *dnfpvp.Catalog) ([]byte, error) {
	c := state.Character
	grade := statU32(c, "pvp_grade", 0)
	if int(grade)+1 >= len(catalog.ExperienceBounds) {
		return nil, fmt.Errorf("PVP grade %d has no experience bounds", grade)
	}
	var w packetWriter
	w.writeUint32(statU32(c, dnfpvp.WinsKey, 0))
	w.writeUint32(statU32(c, dnfpvp.LossesKey, 0))
	w.writeUint32(statU32(c, "pvp_experience", 0))
	// sub_24A8600 stores wire12 at state+140 (lower), wire16 at +132 (upper).
	w.writeUint32(catalog.ExperienceBounds[grade])
	w.writeUint32(catalog.ExperienceBounds[grade+1])
	w.writeByte(byte(grade))
	w.writeByte(byte(statU32(c, "pvp_rating_grade", 0)))
	oldGrade := uint32(0xffffffff)
	if state.BeforeGrade != nil {
		oldGrade = *state.BeforeGrade
	}
	w.writeUint32(oldGrade)
	w.writeUint32(statU32(c, "pvp_rank_point", 0))
	w.writeUint32(statU32(c, "pvp_max_rank_point", statU32(c, "pvp_rank_point", 0)))
	w.writeUint32(statU32(c, dnfpvp.CertificationKey, 0))
	w.writeByte(0)
	w.writeUint32(statU32(c, "pvp_op48_field39", 0))
	w.writeUint32(statU32(c, "pvp_total_match_rank_point", 0))
	w.writeUint32(statU32(c, "pvp_total_match_certification_count", 0))
	return w.bytes(), nil
}

func (s *Service) prepareCurrentPVPDaily(session *gameSession) (dnfpvp.Snapshot, error) {
	catalog, err := s.loadPVPDailyCatalog()
	if err != nil {
		return dnfpvp.Snapshot{}, err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return dnfpvp.Snapshot{}, fmt.Errorf("PVP repositories unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	return dnfpvp.Prepare(ctx, repos, catalog, session.accountID, strconv.Itoa(int(session.selectedCharacterID)), s.gameplayNow())
}

func (s *Service) sendCurrentPVPDailyState(session *gameSession) error {
	if session == nil || session.conn == nil || session.selectedCharacterID == 0 {
		return nil
	}
	if currentPVPMatchingGameType(session) == 7 {
		return s.sendCurrentPVPTotalProgress(session)
	}
	state, err := s.prepareCurrentPVPDaily(session)
	if err != nil {
		return err
	}
	catalog, err := s.loadPVPDailyCatalog()
	if err != nil {
		return err
	}
	rewards, err := s.grantCurrentPVPDailyItems(session, catalog)
	if err != nil {
		return err
	}
	state.Character = rewards.Character
	body, err := buildCurrentPVPCertification(state, catalog)
	if err != nil {
		return err
	}
	if err = s.sendGameUpperRawClass(session, 48, body, 0); err != nil {
		return err
	}
	if err = s.sendCurrentPVPTotalRewards(session, dnfpvp.TotalRewards{Character: state.Character, Items: rewards.Items}); err != nil {
		return err
	}
	// Current op108 does not activate event 2207, so each row is exactly
	// u16 ID + u32 progress + u32 target, with no reward override extension.
	if isPVPChannel(session.channel) && session.channel.Type == 8 {
		if err = s.sendCurrentPVPMissions(session, 323, state.Rows, state.NormalRows); err != nil {
			return err
		}
	}
	s.logGameEvent(session, "game-pvp-daily-state-sent", "day", state.Day, "missions", state.Rows,
		"normal_mission_count", len(state.NormalRows),
		"certification_count", state.Character.Stats[dnfpvp.CertificationKey],
		"wins", state.Character.Stats[dnfpvp.WinsKey], "losses", state.Character.Stats[dnfpvp.LossesKey],
		"emblems_granted", state.EmblemsGranted, "emblems_total", state.Character.Stats[dnfpvp.EmblemsKey],
		"duel_exp", state.Character.Stats[dnfpvp.ExperienceKey], "rp", state.Character.Stats[dnfpvp.RankPointKey])
	return nil
}

func (room *currentPVPRoom) ranked1v1() bool {
	if room == nil || room.matchingToken == 0 || room.battleMode != 2 || !room.combatant(0) || !room.combatant(1) {
		return false
	}
	for seat, member := range room.members {
		if seat >= 2 && room.combatant(seat) {
			return false
		}
		if seat < 2 && (member.session == nil || member.session.channel.Type != 8) {
			return false
		}
	}
	return true
}

func (s *Service) settleCurrentPVPDaily(room *currentPVPRoom) error {
	if room != nil && room.match != nil && room.match.total != nil {
		return s.settleCurrentPVPTotal(room)
	}
	ranked := room.ranked1v1()
	manual := room != nil && room.match != nil && !room.match.startedAt.IsZero() && room.matchingToken == 0 && room.battleMode >= 1 && room.battleMode <= 5
	combatants := 0
	if manual {
		for seat, member := range room.members {
			if !room.combatant(seat) {
				continue
			}
			combatants++
			if member.session == nil || member.session.channel.Type != 24 {
				manual = false
			}
		}
		manual = manual && combatants >= 2
	}
	if !ranked && !manual {
		return nil
	}
	catalog, err := s.loadPVPDailyCatalog()
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("PVP repositories unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	for seat, member := range room.members {
		if !room.combatant(seat) || room.match.settled[seat] != nil {
			continue
		}
		won := byte(seat) == room.match.winner
		if manual && room.match.winner != 0xff && room.battleMode != 1 && room.battleMode != 4 {
			won = room.seatState[seat] == room.seatState[room.match.winner]
		}
		opponentJob, opponentRP, opponentRated := int64(0), int64(0), false
		if ranked {
			opponent := room.members[1-seat]
			opponentJob, opponentRP, opponentRated = int64(opponent.job), int64(opponent.rankPoint), opponent.ratingInitialized
		}
		state, err := dnfpvp.Complete(ctx, repos, catalog, member.session.accountID, strconv.Itoa(int(member.characterID)), dnfpvp.Match{
			Key:       fmt.Sprintf("pvp:%d:%d:%p", room.id, room.match.startedAt.UnixNano(), room.match),
			StartedAt: room.match.startedAt, CompletedAt: room.match.completedAt,
			Won: won, Draw: room.match.winner == 0xff, Manual: manual,
			OpponentJob: opponentJob, Reports: room.match.missionReports[seat],
			OpponentRP: opponentRP, OpponentRated: opponentRated,
		})
		if err != nil {
			return err
		}
		room.match.settled[seat] = &state
		member.currencySlot2 = statU32(state.Character, dnfpvp.EmblemsKey, 0)
		s.logGameEvent(member.session, "game-pvp-daily-settled", "room_id", room.id, "missions", state.Rows,
			"certification_count", state.Character.Stats[dnfpvp.CertificationKey],
			"wins", state.Character.Stats[dnfpvp.WinsKey], "losses", state.Character.Stats[dnfpvp.LossesKey],
			"emblems_granted", state.EmblemsGranted, "emblems_total", member.currencySlot2,
			"exp_before", state.Character.Stats["pvp_result_before_exp"], "exp_delta", state.ExperienceDelta,
			"exp_after", state.Character.Stats[dnfpvp.ExperienceKey],
			"rp_before", state.Character.Stats["pvp_result_before_rp"], "rp_after", state.Character.Stats[dnfpvp.RankPointKey])
	}
	return nil
}

func (s *Service) publishCurrentPVPResults(room *currentPVPRoom) error {
	if room.match.total != nil {
		return s.publishCurrentPVPTotalResults(room)
	}
	for seat, member := range room.members {
		if member == nil {
			continue
		}
		stage := &room.match.resultStage[seat]
		state := room.match.settled[seat]
		if *stage == 0 {
			if state != nil {
				catalog, err := s.loadPVPDailyCatalog()
				if err != nil {
					return err
				}
				// Match scene actors may have been recreated. Restore the actual
				// pre-match values before op47 resets the result controller.
				before := *state
				before.Character = dnfrepo.CloneCharacter(state.Character)
				for key, saved := range map[string]string{
					dnfpvp.ExperienceKey: "pvp_result_before_exp",
					dnfpvp.RankPointKey:  "pvp_result_before_rp",
					dnfpvp.GradeKey:      "pvp_result_before_grade",
				} {
					before.Character.Stats[key] = state.Character.Stats[saved]
				}
				body, err := buildCurrentPVPCertification(before, catalog)
				if err != nil {
					return err
				}
				if err := s.sendGameUpperRawClass(member.session, 48, body, 0); err != nil {
					return err
				}
				if err := s.sendCurrentPVPMissions(member.session, 323, state.BeforeRows, state.BeforeNormalRows); err != nil {
					return err
				}
			}
			*stage = 1
		}
		if *stage == 1 {
			if err := s.sendGameUpperRawClass(member.session, 47, buildCurrentPVPResultBody(room, member), 0); err != nil {
				return err
			}
			*stage = 2
		}
		if state == nil {
			continue
		}
		if *stage == 2 {
			catalog, err := s.loadPVPDailyCatalog()
			if err != nil {
				return err
			}
			body, err := buildCurrentPVPCertification(*state, catalog)
			if err != nil {
				return err
			}
			if err = s.sendGameUpperRawClass(member.session, 48, body, 0); err != nil {
				return err
			}
			*stage = 3
		}
		if *stage == 3 {
			if err := s.sendCurrentPVPMissions(member.session, 578, state.ChangedRows); err != nil {
				return err
			}
			*stage = 4
		}
	}
	// Keep both pre-match ratings frozen until every recipient's result is sent.
	for seat, member := range room.members {
		if state := room.match.settled[seat]; state != nil {
			member.grade = byte(state.Character.Stats[dnfpvp.GradeKey])
			member.rankPoint = uint32(state.Character.Stats[dnfpvp.RankPointKey])
			member.ratingInitialized = state.Character.Stats["pvp_rating_initialized"] != 0
		}
	}
	return nil
}

func (s *Service) handleCurrentPVPMissionReport(session *gameSession, opcode uint16, body []byte) error {
	if opcode == 377 {
		return s.observeCurrentPVPWinPose(session, body)
	}
	if session == nil || len(body) < 1 || body[0] == 0 || body[0] > 3 || len(body) != 1+2*int(body[0]) {
		return nil
	}
	s.pvpMu.Lock()
	defer s.pvpMu.Unlock()
	room := session.pvpRoom
	if room == nil || !room.started || room.match == nil || room.match.settlementPending {
		return nil
	}
	if room.match.total != nil {
		return s.handleCurrentPVPTotalMissionReportLocked(room, session, opcode, body)
	}
	if !room.ranked1v1() {
		return nil
	}
	seat := room.seatFor(session)
	if seat < 0 || seat > 1 || room.members[seat].characterID != session.selectedCharacterID {
		return nil
	}
	state, err := s.prepareCurrentPVPDaily(session)
	if err != nil {
		return err
	}
	catalog, err := s.loadPVPDailyCatalog()
	if err != nil {
		return err
	}
	accepted := map[uint16]bool{}
	for i := 1; i < len(body); i += 2 {
		id := binary.LittleEndian.Uint16(body[i:])
		if accepted[id] {
			return nil
		}
		valid := false
		for tier, row := range state.Rows {
			if row.ID != id || row.Progress >= row.Target {
				continue
			}
			for _, m := range catalog.Pools[tier] {
				if m.ID != id {
					continue
				}
				valid = opcode == 876 && m.Condition == "first aerial attack" || opcode == 877 && m.Condition == "combo damage rate" ||
					opcode == 376 && (m.Condition == "reduce hp" || m.Condition == "remains hp" || m.Condition == "remain hp")
			}
		}
		if !valid {
			return nil
		}
		accepted[id] = true
	}
	if room.match.missionReports[seat] == nil {
		room.match.missionReports[seat] = map[uint16]bool{}
	}
	for id := range accepted {
		room.match.missionReports[seat][id] = true
	}
	return nil
}
