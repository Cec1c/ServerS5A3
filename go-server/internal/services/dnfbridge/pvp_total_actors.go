package dnfbridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	dnfpvp "longheng.io/server/internal/modules/dnf/pvp"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/skillcmd"
)

type currentPVPTotalTeam struct {
	snapshot          dnfpvp.TotalTeamSnapshot
	actors            [3][]byte
	rankPoint         uint32
	ratingInitialized bool
}

// sub_2003570 uses the durable UI slot, not the position in the skill list.
// Slot 255 learns an unassigned skill without assigning a shortcut (268FA30).
func buildCurrentPVPTotalSkills(record dnfrepo.SkillRecord, active byte) ([]byte, error) {
	state, found := dnfrepo.SkillTree(record, int(active))
	if !found || active >= currentSkillInfoTreeCount {
		return nil, fmt.Errorf("total-match active skill tree missing")
	}
	type row struct {
		slot  byte
		id    uint16
		level byte
	}
	rows := []row{}
	assigned := map[uint16]bool{}
	for slot, id := range record.Layouts[int(active)] {
		if slot < 0 || slot >= 204 {
			return nil, fmt.Errorf("invalid total-match skill slot %d", slot)
		}
		level := state.Skills[int64(id)].Level
		if level == 0 {
			continue
		}
		if level < 0 || level > 255 || id >= 512 {
			return nil, fmt.Errorf("invalid total-match skill %d", id)
		}
		rows = append(rows, row{byte(slot), id, byte(level)})
		assigned[id] = true
	}
	for id, skill := range state.Skills {
		if id < 0 || id >= 512 || skill.Level < 0 || skill.Level > 255 {
			return nil, fmt.Errorf("invalid total-match learned skill %d", id)
		}
		if skill.Level > 0 && !assigned[uint16(id)] {
			rows = append(rows, row{255, uint16(id), byte(skill.Level)})
		}
	}
	if len(rows) > 255 {
		return nil, fmt.Errorf("too many total-match skills")
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].slot != rows[j].slot {
			return rows[i].slot < rows[j].slot
		}
		return rows[i].id < rows[j].id
	})
	var w packetWriter
	w.writeByte(byte(len(rows)))
	w.writeByte(active)
	for _, row := range rows {
		w.writeByte(row.slot)
		w.writeUint16(row.id)
		w.writeByte(row.level)
	}
	return w.bytes(), nil
}

func (s *Service) loadCurrentPVPTotalTeam(ctx context.Context, session *gameSession) (*currentPVPTotalTeam, error) {
	repos, ok := s.repositoryGroup()
	if !ok || repos.Skill == nil || repos.Equipment == nil {
		return nil, fmt.Errorf("total-match actor repositories missing")
	}
	snapshot, err := dnfpvp.ReadTotalTeam(ctx, repos, session.accountID)
	if err != nil {
		return nil, err
	}
	s.initialEquipmentMu.Lock()
	archive, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err != nil {
		return nil, err
	}
	snapshot, err = dnfpvp.ResolveTotalTeam(session.accountID, snapshot.Team.Name, snapshot.Slots, snapshot.Characters[:], archive)
	if err != nil {
		return nil, err
	}
	team := &currentPVPTotalTeam{snapshot: snapshot}
	account, found, err := repos.Account.Load(ctx, session.accountID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("total-match account missing")
	}
	progress, err := dnfpvp.ReadTotalProgress(account)
	if err != nil {
		return nil, err
	}
	team.rankPoint = uint32(progress.Stats[dnfpvp.RankPointKey])
	team.ratingInitialized = progress.Stats["pvp_rating_initialized"] != 0
	selectedFound := false
	for _, c := range snapshot.Characters {
		selectedFound = selectedFound || c.CharacterID == strconv.Itoa(int(session.selectedCharacterID))
	}
	if !selectedFound {
		return nil, fmt.Errorf("selected character outside total-match roster")
	}
	for i, c := range snapshot.Characters {
		skills, err := skillcmd.PreparePVPSkills(ctx, repos, session.accountID, c.CharacterID)
		if err != nil {
			return nil, err
		}
		active := byte(dnfrepo.ActiveSkillTree(skills))
		skillBody, err := buildCurrentPVPTotalSkills(skills, active)
		if err != nil {
			return nil, err
		}
		stats, found := s.characterPVFStatsForUserInfo(ctx, session, c, true)
		if !found {
			return nil, fmt.Errorf("total-match character %s PVF stats missing", c.CharacterID)
		}
		// Preflight equipment errors instead of treating a failed load as naked.
		if _, _, err := repos.Equipment.Load(ctx, c.CharacterID); err != nil {
			return nil, err
		}
		r := csharpLegacyUserInfoReader{ctx: ctx, repo: repos.LegacyUserInfo, characterID: c.CharacterID, service: s, session: session, pvfStats: stats, hasPVFStats: true}
		team.actors[i], err = r.buildCurrentPVPTotalActor(c, skillBody)
		if err != nil {
			return nil, err
		}
	}
	return team, nil
}

// Exact special=1 payload of current EXE 2237EB0, called by NOTI 800.
// The enclosing packet owns the name and index; this is not a mode1 record.
func (r *csharpLegacyUserInfoReader) buildCurrentPVPTotalActor(c dnfrepo.CharacterRecord, skills []byte) ([]byte, error) {
	job, err := strconv.ParseUint(c.Job, 10, 8)
	if err != nil {
		return nil, err
	}
	var w packetWriter
	w.writeByte(rosterLevel(c))
	w.writeByte(byte(job))
	w.writeByte(byte(statU32(c, "grow_type", 0) & 127))
	w.writeByte(boolByte(dnfpvp.IsPrivateCharacter(c)))
	stats := r.buildCurrentUserInfoMode1StatBlob(r.one("legacy_character_subtype1_fields", csharpUserInfoSubtype1Columns()), c)
	w.writeUint32(uint32(len(stats)))
	w.writeBytes(stats)
	w.writeByte(currentExtraEquipmentSlotState(c))
	writeCurrentSceneObjectEquipSummary(&w, currentSceneObjectMode0EquipSummary(c, true))
	w.writeUint32(statU32(c, "clone_title_item_id", 0))
	w.writeZero(4) // 2002850, 2002880, 1B1D610, reserved u8
	rows := r.currentMode1EquipmentObjectRows()
	if len(currentMode1EquipmentDeferredUpdateRows(rows)) != 0 {
		return nil, fmt.Errorf("total-match equipment has no verified create representation")
	}
	create := currentMode1EquipmentCreateRows(rows)
	w.writeByte(byte(len(create)))
	for _, row := range create {
		writeCurrentMode1EquipmentCreateRow(&w, row)
	}
	w.writeUint32(statU32(c, "clone_title_item_id", 0))
	// Skill-object +0 has native constructor default zero (268F852). There is
	// no persisted counterpart in this server; do not invent a character key.
	w.writeUint16(0)
	w.writeBytes(skills)
	w.writeUint32(0) // same unpopulated auxiliary actor list as normal mode1
	// Current PVF excludes Creator (job 10) from matching. Its five auxiliary
	// values are still present on the wire and ignored by other actor types.
	w.writeZero(20)
	creature := r.currentEquippedCreature()
	w.writeUint32(creature.itemID)
	w.writeUint32(creature.serialOrHandle)
	return w.bytes(), nil
}

func writeCurrentPVPTotalSummary(w *packetWriter, team *currentPVPTotalTeam) {
	w.writeRawDstr(rosterNameBytes(team.snapshot.Team.Name))
	for _, c := range team.snapshot.Characters {
		w.writeRawDstr(rosterNameBytes(c.Name))
		w.writeByte(byte(numericCharacterStat(c.Job)))
		w.writeByte(byte(statU32(c, "grow_type", 0) & 127))
		w.writeByte(boolByte(dnfpvp.IsPrivateCharacter(c)))
	}
	w.writeUint32(team.rankPoint)
}

func buildCurrentPVPTotalActors(members []*currentPVPMember) []byte {
	var w packetWriter
	w.writeByte(byte(len(members)))
	for _, m := range members {
		w.writeUint16(m.characterID)
		w.writeByte(3)
		for i, c := range m.total.snapshot.Characters {
			w.writeByte(byte(i))
			w.writeRawDstr(rosterNameBytes(c.Name))
			w.writeBytes(m.total.actors[i])
		}
	}
	return w.bytes()
}

func buildCurrentPVPTotalSummaries(members []*currentPVPMember) []byte {
	var w packetWriter
	w.writeByte(byte(len(members)))
	for _, m := range members {
		w.writeUint32(uint32(m.characterID))
		writeCurrentPVPTotalSummary(&w, m.total)
	}
	return w.bytes()
}

func (s *Service) sendCurrentPVPTotalActors(target *gameSession, members []*currentPVPMember) error {
	if len(members) == 0 {
		return nil
	}
	for _, packet := range []struct {
		id   uint16
		body []byte
	}{{802, buildCurrentPVPTotalSummaries(members)}, {800, buildCurrentPVPTotalActors(members)}} {
		if err := s.sendGameUpperRawClass(target, packet.id, packet.body, 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) refreshCurrentPVPTotalRoomLocked(room *currentPVPRoom) error {
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	members := []*currentPVPMember{}
	loaded := map[*currentPVPMember]*currentPVPTotalTeam{}
	for seat, m := range room.members {
		if !room.combatant(seat) {
			continue
		}
		team, err := s.loadCurrentPVPTotalTeam(ctx, m.session)
		if err != nil {
			return err
		}
		loaded[m] = team
		members = append(members, m)
	}
	// Publish only once the complete room roster has loaded successfully.
	for m, team := range loaded {
		m.total = team
	}
	for _, m := range room.members {
		if m == nil {
			continue
		}
		if m.total != nil {
			if err := s.sendGameUpperRawClass(m.session, 801, buildPVPTotalTeamSelection(m.total.snapshot), 0); err != nil {
				return err
			}
		}
		if err := s.sendCurrentPVPTotalActors(m.session, members); err != nil {
			return err
		}
	}
	return nil
}
