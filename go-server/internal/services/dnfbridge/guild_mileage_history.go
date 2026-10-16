package dnfbridge

import (
	"encoding/binary"
	"sort"
	"strconv"

	dnfguild "longheng.io/server/internal/modules/dnf/guild"
)

// Matched class1 op747 (RVA 0x1bc2340) reads count u32 then 48-byte rows.
// The UI uses timestamp at 0, a NUL-terminated name at 4, kind at 34 and
// content ID at 36 and amount at 40. Kinds 3/8 select dungeon/item income;
// kind 5 resolves the purchased content's name and selects the expense format.
func buildCurrentGuildMileageHistoryBody(history []dnfguild.MileageHistoryEntry) ([]byte, error) {
	rows := append([]dnfguild.MileageHistoryEntry(nil), history...)
	// Use persisted order for equal-second events; newest insertion comes first.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	if len(rows) > dnfguild.MileageHistoryLimit {
		rows = rows[:dnfguild.MileageHistoryLimit]
	}
	body := make([]byte, 4+48*len(rows))
	binary.LittleEndian.PutUint32(body, uint32(len(rows)))
	for i, entry := range rows {
		if !entry.Valid() {
			return nil, dnfguild.ErrMileageHistory
		}
		row := body[4+i*48 : 4+(i+1)*48]
		binary.LittleEndian.PutUint32(row, uint32(entry.At.Unix()))
		var name []byte
		for _, r := range entry.CharacterName {
			if r == 0 {
				return nil, dnfguild.ErrMileageHistory
			}
			encoded := rosterNameBytes(string(r))
			if len(name)+len(encoded) > 29 {
				break
			}
			name = append(name, encoded...)
		}
		if len(name) == 0 {
			return nil, dnfguild.ErrMileageHistory
		}
		copy(row[4:34], name)
		row[34] = entry.Kind
		binary.LittleEndian.PutUint32(row[36:40], entry.ContentID)
		binary.LittleEndian.PutUint32(row[40:44], entry.Amount)
	}
	return body, nil
}

func (s *Service) handleCurrentGuildMileageHistory(session *gameSession, request []byte) error {
	const op = uint16(747)
	reject := func() error { return s.sendGameUpperFailure(session, op, currentGuildGenericError) }
	if len(request) != 0 {
		return reject()
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound {
		return reject()
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject()
	}
	defer release()
	character, err := s.currentGuildOwnedSelectedCharacter(session)
	if err != nil {
		return reject()
	}
	_, registry, err := s.loadCurrentGuildRegistry(ctx)
	if err != nil {
		return err
	}
	g, member, found := registry.GuildForCharacter(strconv.Itoa(int(identity.character)))
	if !found || member.AccountID != character.AccountID || !s.boundGameSessionCharacterCurrent(identity) {
		return reject()
	}
	body, err := buildCurrentGuildMileageHistoryBody(g.MileageHistory)
	if err != nil {
		return reject()
	}
	return s.sendGameUpperSuccess(session, op, body)
}
