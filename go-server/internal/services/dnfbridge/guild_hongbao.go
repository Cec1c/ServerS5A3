package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"

	guild "longheng.io/server/internal/modules/dnf/guild"
)

const currentGuildHongbaoListOpcode uint16 = 1300
const currentGuildHongbaoPointsOpcode uint16 = 1301
const currentGuildHongbaoPointsMaxMembers = 1000

func (s *Service) handleCurrentGuildHongbaoList(session *gameSession, body []byte) error {
	if s.options.guildHongbaoEnabled {
		return s.handleCurrentGuildHongbaoLiveQuery(session, currentGuildHongbaoListOpcode, body)
	}
	return s.handleCurrentGuildHongbaoQuery(session, currentGuildHongbaoListOpcode, body)
}

func (s *Service) handleCurrentGuildHongbaoPoints(session *gameSession, body []byte) error {
	return s.handleCurrentGuildHongbaoQuery(session, currentGuildHongbaoPointsOpcode, body)
}

func (s *Service) sendCurrentGuildHongbaoFailure(session *gameSession, op uint16) error {
	if op == currentGuildHongbaoIssueOpcode || op == currentGuildHongbaoClaimOpcode {
		return s.sendGameUpperFailure(session, op, 34)
	}
	// Query callbacks consume an additional u32 after the normal failure header.
	return s.sendGameUpperRawClass(session, op, []byte{0, 34, 34, 0, 0, 0}, 1)
}

func (s *Service) handleCurrentGuildHongbaoQuery(session *gameSession, op uint16, body []byte) error {
	fail := func() error { return s.sendCurrentGuildHongbaoFailure(session, op) }
	if len(body) != 0 {
		return fail()
	}
	source, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok {
		return fail()
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{source})
	if err != nil {
		return nil
	}
	defer release()
	g, err := s.readCurrentGuildDisplay(ctx, source, op == currentGuildHongbaoPointsOpcode, true)
	if !s.boundGameSessionCharacterCurrent(source) {
		return nil
	}
	if err != nil || g == nil {
		return fail()
	}
	members := guild.SortedMembers(g)
	var response []byte
	if op == currentGuildHongbaoPointsOpcode {
		response, err = buildCurrentGuildHongbaoPointsBody(selectCurrentGuildHongbaoPointsMembers(members, strconv.Itoa(int(source.character))))
		if err != nil {
			return fail()
		}
	} else {
		if len(members) > 65535 {
			return fail()
		}
		// This server has no hongbao issuance/claim producer yet. The collection
		// is empty; do not turn guild mileage into fictional spending or grants.
		var w packetWriter
		w.writeUint16(uint16(len(members)))
		w.writeUint32(0) // Issuance spending: no transactions exist in this implementation.
		w.writeUint32(0) // Claims: no transactions exist in this implementation.
		w.writeUint32(0) // Empty hongbao collection.
		response = w.bytes()
	}
	if !s.boundGameSessionCharacterCurrent(source) {
		return nil
	}
	return s.sendGameUpperSuccess(session, op, response)
}

// DNF.exe VA 0x00f48d00 reads op1301 into a fixed 1000 * 40-byte stack buffer
// without bounds checks, while UI VA 0x01196eb9 caps displayed ranking rows to
// top 30. For guilds above 1000 members, send the top 1000 contributors and
// guarantee the viewer's own entry is always included.
func selectCurrentGuildHongbaoPointsMembers(members []guild.Member, viewerCharacterID string) []guild.Member {
	if len(members) <= currentGuildHongbaoPointsMaxMembers {
		return members
	}
	ranked := append([]guild.Member(nil), members...)
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].ContributionPoints != ranked[j].ContributionPoints {
			return ranked[i].ContributionPoints > ranked[j].ContributionPoints
		}
		if ranked[i].Grade != ranked[j].Grade {
			return ranked[i].Grade < ranked[j].Grade
		}
		return ranked[i].CharacterID < ranked[j].CharacterID
	})
	selected := ranked[:currentGuildHongbaoPointsMaxMembers]
	if viewerCharacterID == "" {
		return selected
	}
	for _, m := range selected {
		if m.CharacterID == viewerCharacterID {
			return selected
		}
	}
	for _, m := range ranked[currentGuildHongbaoPointsMaxMembers:] {
		if m.CharacterID == viewerCharacterID {
			selected[len(selected)-1] = m
			break
		}
	}
	return selected
}

func buildCurrentGuildHongbaoPointsBody(members []guild.Member) ([]byte, error) {
	// The native reader has a fixed 1000 * 40-byte stack buffer, no bound check.
	// Callers must bound large guilds via selectCurrentGuildHongbaoPointsMembers.
	if len(members) > currentGuildHongbaoPointsMaxMembers {
		return nil, fmt.Errorf("hongbao roster exceeds native limit")
	}
	var w packetWriter
	w.writeUint32(uint32(len(members)))
	for _, m := range members {
		id, err := strconv.ParseUint(m.CharacterID, 10, 32)
		name := rosterNameBytes(m.Name)
		if err != nil || id == 0 || m.Level < 0 || m.Level > 255 || len(name) > 29 {
			return nil, fmt.Errorf("invalid hongbao member projection")
		}
		var row [40]byte
		binary.LittleEndian.PutUint32(row[:4], uint32(id))
		row[4] = byte(m.Level)
		copy(row[5:35], name) // 30-byte native name, including its NUL terminator.
		binary.LittleEndian.PutUint32(row[35:39], m.ContributionPoints)
		w.writeBytes(row[:])
	}
	return w.bytes(), nil
}
