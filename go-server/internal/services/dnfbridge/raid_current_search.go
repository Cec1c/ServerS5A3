package dnfbridge

import (
	"bytes"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
	"longheng.io/server/internal/modules/dnf/raid"
)

func currentRaidSearchName(raw []byte) (string, bool) {
	if len(raw) > 1020 {
		return "", false
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return "", false
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
	name := string(decoded)
	if err != nil || !bytes.Equal(raw, encoded) || len(utf16.Encode([]rune(name))) > 255 {
		return "", false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return "", false
		}
	}
	return strings.ToLower(name), true
}

// This directory is scoped to the real Service raid owner. It does not
// invent a cross-server registry or offline attack parties. RaidKey carries
// the actual advertised server and channel, independently of sort fields.
func (s *Service) currentRaidSearchEntries(session *gameSession, req raid.CurrentSearchRequest, name string) []raid.CurrentMetadata {
	route, supported := currentRaidRouteForChannel(int(session.channel.Type))
	if !supported {
		return nil
	}
	s.raidMu.Lock()
	states := make([]runtimeRaidState, 0, len(s.raids))
	for _, state := range s.raids {
		if state == nil || !state.CurrentLayout || state.RouteOrRaidType != route || len(state.Members) == 0 {
			continue
		}
		if (req.Mode == 1 || req.OnlyMyChannel) && byte(state.RaidKey>>16) != byte(session.channel.ID) {
			continue
		}
		if req.Mode == 0 && req.ExcludeProceeding && state.Started {
			continue
		}
		states = append(states, cloneRuntimeRaidState(*state))
	}
	s.raidMu.Unlock()
	entries := make([]raid.CurrentMetadata, 0, len(states))
	for _, state := range states {
		if req.Mode == 0 && name != "" {
			text, valid := currentRaidSearchName(state.NameBytes)
			if !valid || !strings.Contains(text, name) {
				continue
			}
		}
		entries = append(entries, currentRuntimeRaidMetadata(state))
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		cmp := 0
		switch req.SortColumn {
		case 2:
			cmp = int(byte(a.RaidKey>>16)) - int(byte(b.RaidKey>>16)) // SortChannel
		case 3:
			an, _ := currentRaidSearchName(a.NameBytes)
			bn, _ := currentRaidSearchName(b.NameBytes)
			cmp = strings.Compare(an, bn) // SortRaidName
		case 4:
			cmp = int(a.Phase) - int(b.Phase) // Same phase field as NOTI591.
		case 5:
			cmp = len(a.Members) - len(b.Members) // Native default SortMemberNum.
		case 6:
			cmp = int(a.Flags[0]) - int(b.Flags[0]) // SortMode, verified single flag.
			// 0 is the unspecialized order. SortPcRoom=1 has no recovered
			// authoritative special-field owner, so it retains stable key order.
		}
		if cmp == 0 {
			return a.RaidKey < b.RaidKey
		}
		if req.Reverse {
			return cmp > 0
		}
		return cmp < 0
	})
	return entries
}

func (s *Service) handleCurrentRaidSearch(session *gameSession, op uint16, body []byte) error {
	req, err := raid.DecodeCurrentSearchRequest(body)
	name, valid := currentRaidSearchName(req.NameBytes)
	if err != nil || !valid || !s.currentRaidAuthenticated(session) {
		return s.sendRaidCommandFailure(session, op)
	}
	if _, supported := currentRaidRouteForChannel(int(session.channel.Type)); !supported {
		return s.sendRaidCommandFailure(session, op)
	}
	entries := s.currentRaidSearchEntries(session, req, name)
	if req.Mode == 1 {
		response, err := raid.BuildCurrentDirectoryBody(entries)
		if err != nil {
			return err
		}
		return s.sendGameUpperRawClassCodec(session, 590, response, 0, true)
	}
	total := uint32(len(entries))
	offset := req.Offset
	if offset > total {
		offset = total
	}
	end := uint64(offset) + raid.CurrentRaidSearchPageSize
	if end > uint64(total) {
		end = uint64(total)
	}
	response, err := raid.BuildCurrentSearchPageBody(total, offset, entries[int(offset):int(end)])
	if err != nil {
		return err
	}
	s.logGameEvent(session, "game-current-raid-search-sent", "total", total, "offset", offset, "entries", end-uint64(offset), "sort_column", req.SortColumn, "only_my_channel", req.OnlyMyChannel, "exclude_proceeding", req.ExcludeProceeding, "msg_id", 753)
	return s.sendGameUpperRawClassCodec(session, 753, response, 0, true)
}
