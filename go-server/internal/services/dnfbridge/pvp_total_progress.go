package dnfbridge

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) loadPVPTotalCatalog() (*dnfpvp.Catalog, error) {
	s.initialEquipmentMu.Lock()
	defer s.initialEquipmentMu.Unlock()
	if s.pvpTotalCatalog != nil {
		return s.pvpTotalCatalog, nil
	}
	archive, err := s.initialEquipmentArchiveLocked()
	if err != nil {
		return nil, err
	}
	s.pvpTotalCatalog, err = dnfpvp.LoadTotal(archive)
	return s.pvpTotalCatalog, err
}

// 1D5AB40 reads three 26-byte records. 3292590/3292749 use the four
// thresholds as before lower/upper and after lower/upper respectively.
func buildCurrentPVPTotalCertification(state dnfpvp.TotalSnapshot, selected string, catalog *dnfpvp.Catalog, before, tournamentChannel bool) ([]byte, error) {
	index := -1
	for i, c := range state.Characters {
		if c.CharacterID == selected {
			index = i
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("total certification owner missing")
	}
	c := repo.CloneCharacter(state.Characters[index])
	stats := state.Stats
	if before {
		c = repo.CloneCharacter(state.Before[index])
		stats = state.BeforeStats
	}
	if c.Stats == nil {
		c.Stats = map[string]int64{}
	}
	c.Stats["pvp_total_match_rank_point"] = stats[dnfpvp.RankPointKey]
	c.Stats["pvp_total_match_certification_count"] = stats[dnfpvp.CertificationKey]
	if tournamentChannel {
		// 1D5A917 uses this alternate RP slot only for preTournament (31).
		// TotalMatch consumes its RP from the dedicated wire43 field.
		c.Stats[dnfpvp.RankPointKey] = stats[dnfpvp.RankPointKey]
		c.Stats[dnfpvp.MaxRankKey] = stats[dnfpvp.MaxRankKey]
	}
	base, err := buildCurrentPVPCertification(dnfpvp.Snapshot{Character: c}, catalog)
	if err != nil {
		return nil, err
	}
	var w packetWriter
	w.writeBytes(base)
	for i, after := range state.Characters {
		old := state.Before[i]
		if before {
			after = old
		}
		bg, ag := old.Stats[dnfpvp.GradeKey], after.Stats[dnfpvp.GradeKey]
		if bg < 0 || ag < 0 || bg+1 >= int64(len(catalog.ExperienceBounds)) || ag+1 >= int64(len(catalog.ExperienceBounds)) {
			return nil, fmt.Errorf("invalid total actor grade")
		}
		w.writeUint32(uint32(old.Stats[dnfpvp.ExperienceKey]))
		w.writeUint32(uint32(after.Stats[dnfpvp.ExperienceKey]))
		w.writeByte(byte(bg))
		w.writeByte(byte(ag))
		w.writeUint32(catalog.ExperienceBounds[bg])
		w.writeUint32(catalog.ExperienceBounds[bg+1])
		w.writeUint32(catalog.ExperienceBounds[ag])
		w.writeUint32(catalog.ExperienceBounds[ag+1])
	}
	return w.bytes(), nil
}

func (s *Service) sendCurrentPVPTotalProgress(session *gameSession) error {
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("total-match repositories missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	catalog, err := s.loadPVPTotalCatalog()
	if err != nil {
		return err
	}
	selected := strconv.Itoa(int(session.selectedCharacterID))
	missions, err := dnfpvp.PrepareTotalMissions(ctx, repos, catalog, session.accountID, selected, s.gameplayNow())
	if err != nil {
		return err
	}
	rewards, err := s.grantCurrentPVPTotalTargets(ctx, repos, catalog, session)
	if err != nil {
		return err
	}
	if err := s.sendCurrentPVPTotalRewards(session, rewards); err != nil {
		return err
	}
	// Native op323 replaces the entire mission map at 1C52993. Include both
	// families once, even before the account registers a three-character team.
	if err := s.sendCurrentPVPMissions(session, 323, missions.Rows, missions.NormalRows); err != nil {
		return err
	}
	s.logGameEvent(session, "game-pvp-total-missions-sent", "day", missions.Day, "missions", missions.Rows,
		"normal_mission_count", len(missions.NormalRows), "emblems_granted", missions.EmblemsGranted)
	team, err := dnfpvp.ReadTotalTeam(ctx, repos, session.accountID)
	if err != nil {
		return err
	}
	if team.Team.Name == "" || team.Slots[0] == 255 || team.Slots[1] == 255 || team.Slots[2] == 255 {
		return nil
	}
	state, err := dnfpvp.PrepareTotal(ctx, repos, catalog, session.accountID, selected, team.Team.Characters, s.gameplayNow())
	if err != nil {
		return err
	}
	body, err := buildCurrentPVPTotalCertification(state, selected, catalog, false, session.channel.Type == 31)
	if err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 48, body, 0)
}

func (s *Service) settleCurrentPVPTotal(room *currentPVPRoom) error {
	catalog, err := s.loadPVPTotalCatalog()
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("total-match repositories missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	seats := room.totalCombatants()
	if len(seats) != 2 {
		return fmt.Errorf("total-match frozen owners missing")
	}
	for side, seat := range seats {
		if room.match.total.settled[seat] != nil {
			continue
		}
		member, peer := room.members[seat], room.members[seats[1-side]]
		if member.total == nil || peer.total == nil {
			return fmt.Errorf("total-match frozen actors missing")
		}
		remaining := 0
		for _, dead := range room.match.total.defeated[seat] {
			if !dead {
				remaining++
			}
		}
		state, err := dnfpvp.CompleteTotal(ctx, repos, catalog, member.session.accountID, strconv.Itoa(int(member.characterID)), member.total.snapshot.Team.Characters, dnfpvp.TotalMatchFacts{
			Match: dnfpvp.Match{Key: fmt.Sprintf("total:%d:%d:%p", room.id, room.match.startedAt.UnixNano(), room.match), StartedAt: room.match.startedAt, CompletedAt: room.match.completedAt,
				Won: byte(seat) == room.match.winner, Draw: room.match.winner == 255, Manual: room.matchingToken == 0,
				OpponentRP: int64(peer.total.rankPoint), OpponentRated: peer.total.ratingInitialized},
			Kills: room.match.total.kills[seat], Remaining: remaining, ReportsCount: room.match.total.reports[seat],
		})
		if err != nil {
			return err
		}
		state.Rewards, err = s.grantCurrentPVPTotalTargets(ctx, repos, catalog, member.session)
		if err != nil {
			return err
		}
		for i := range state.Characters {
			if state.Characters[i].CharacterID == state.Rewards.Character.CharacterID {
				state.Characters[i].Stats[dnfpvp.EmblemsKey] = state.Rewards.Character.Stats[dnfpvp.EmblemsKey]
			}
		}
		room.match.total.settled[seat] = &state
		for _, c := range state.Characters {
			if c.CharacterID == strconv.Itoa(int(member.characterID)) {
				member.currencySlot2 = uint32(c.Stats[dnfpvp.EmblemsKey])
			}
		}
	}
	return nil
}

func (s *Service) publishCurrentPVPTotalResults(room *currentPVPRoom) error {
	catalog, err := s.loadPVPTotalCatalog()
	if err != nil {
		return err
	}
	for seat, member := range room.members {
		if member == nil {
			continue
		}
		state, stage := room.match.total.settled[seat], &room.match.resultStage[seat]
		if *stage == 0 {
			if state != nil {
				body, err := buildCurrentPVPTotalCertification(*state, strconv.Itoa(int(member.characterID)), catalog, true, member.session.channel.Type == 31)
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
			body, err := buildCurrentPVPTotalCertification(*state, strconv.Itoa(int(member.characterID)), catalog, false, member.session.channel.Type == 31)
			if err != nil {
				return err
			}
			if err := s.sendGameUpperRawClass(member.session, 48, body, 0); err != nil {
				return err
			}
			*stage = 3
		}
		if *stage == 3 {
			if err := s.sendCurrentPVPTotalRewards(member.session, state.Rewards); err != nil {
				return err
			}
			if err := s.sendCurrentPVPMissions(member.session, 578, state.ChangedRows); err != nil {
				return err
			}
			*stage = 4
		}
	}
	for seat, member := range room.members {
		if state := room.match.total.settled[seat]; state != nil {
			member.total.rankPoint = uint32(state.Stats[dnfpvp.RankPointKey])
			member.total.ratingInitialized = state.Stats["pvp_rating_initialized"] != 0
			member.total.snapshot.Characters = state.Characters
		}
	}
	return nil
}

func (s *Service) handleCurrentPVPTotalMissionReportLocked(room *currentPVPRoom, session *gameSession, opcode uint16, body []byte) error {
	seat := room.seatFor(session)
	if opcode != 376 || room.matchingToken == 0 || !isCurrentPVPTotalChannel(session.channel) || !room.combatant(seat) || room.match.total.choosing || room.match.total.selected[seat] >= 3 {
		return nil
	}
	catalog, err := s.loadPVPTotalCatalog()
	if err != nil {
		return err
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fmt.Errorf("total-match repositories missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	member := room.members[seat]
	state, err := dnfpvp.PrepareTotal(ctx, repos, catalog, session.accountID, strconv.Itoa(int(member.characterID)), member.total.snapshot.Team.Characters, s.gameplayNow())
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
			if row.ID == id && row.Progress < row.Target {
				for _, m := range catalog.Pools[tier] {
					valid = valid || m.ID == id && m.Condition == "reduce hp"
				}
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
	if room.match.total.reports[seat] == nil {
		room.match.total.reports[seat] = map[uint16]uint32{}
	}
	for id := range accepted {
		if !room.match.missionReports[seat][id] {
			room.match.total.reports[seat][id]++
			room.match.missionReports[seat][id] = true
		}
	}
	return nil
}
