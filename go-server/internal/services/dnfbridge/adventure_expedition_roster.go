package dnfbridge

import (
	"context"
	"sort"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

type currentAdventureExpeditionSentMember struct {
	RecordID   string
	Name       string
	Job, Grow  byte
	PackedGrow int
}

type currentAdventureExpeditionSentView struct {
	AccountID  string
	Generation uint64
	Selected   uint16
	Members    map[byte]currentAdventureExpeditionSentMember
}

// Selection changes the character generation after the preselection 441 reply.
// Refresh that authenticated account view before the town UI becomes visible.
func (s *Service) sendCurrentAdventureExpeditionRosterAfterSelection(session *gameSession) error {
	if session == nil || session.selectedCharacterID == 0 || session.currentAdventureExpeditionView.AccountID == "" {
		return nil
	}
	if !session.loginPacketSeen || !session.accountFromToken || session.currentAdventureExpeditionView.AccountID != s.accountIDForSession(session) {
		return errCurrentAdventureStateInvalid
	}
	if s.currentAdventureExpeditionViewCurrent(session, session.currentAdventureExpeditionView) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	s.adventureGroupRuntimeMu.Lock()
	defer s.adventureGroupRuntimeMu.Unlock()
	runtime, err := s.currentAdventureExpeditionInfoState(ctx, session)
	if err != nil {
		return err
	}
	return s.sendCurrentAdventureExpeditionStatePush(session, runtime, s.gameplayNow())
}

func (s *Service) currentAdventureExpeditionRosterView(ctx context.Context, session *gameSession, runtime adventuregroup.RuntimeState) ([]byte, currentAdventureExpeditionSentView, error) {
	if session == nil || !session.loginPacketSeen || !session.accountFromToken || strings.TrimSpace(session.accountID) == "" {
		return nil, currentAdventureExpeditionSentView{}, errCurrentAdventureRuntimeUnavailable
	}
	repositories, ok := s.repositoryGroup()
	if !ok || repositories.Character == nil {
		return nil, currentAdventureExpeditionSentView{}, errCurrentAdventureRuntimeUnavailable
	}
	view := currentAdventureExpeditionSentView{AccountID: s.accountIDForSession(session), Generation: session.characterGeneration, Selected: session.selectedCharacterID}
	characters, err := repositories.Character.ListByAccount(ctx, view.AccountID, defaultCharacterSlots)
	if err != nil {
		return nil, currentAdventureExpeditionSentView{}, err
	}
	if s.adventureGroupTable == nil {
		return nil, currentAdventureExpeditionSentView{}, errCurrentAdventureRuntimeUnavailable
	}
	body, members, err := buildCurrentAdventureExpeditionRosterBody(runtime, characters, view.AccountID, s.gameplayNow(), s.adventureGroupTable.Runtime())
	view.Members = members
	return body, view, err
}

func (s *Service) currentAdventureExpeditionViewCurrent(session *gameSession, view currentAdventureExpeditionSentView) bool {
	return session != nil && session.loginPacketSeen && session.accountFromToken && view.AccountID != "" &&
		view.AccountID == s.accountIDForSession(session) && view.Generation == session.characterGeneration && view.Selected == session.selectedCharacterID
}

// The native reader's area zero supplies idle account members without creating
// an expedition. This copy is a presentation projection and is never saved.
func buildCurrentAdventureExpeditionRosterBody(runtime adventuregroup.RuntimeState, characters []repo.CharacterRecord, accountID string, now time.Time, configs ...adventuregroup.RuntimeConfig) ([]byte, map[byte]currentAdventureExpeditionSentMember, error) {
	rows := append([]repo.CharacterRecord(nil), characters...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Slot < rows[j].Slot })
	slots := make(map[int]bool)
	ids := make(map[string]bool)
	for _, c := range rows {
		if c.AccountID != accountID || c.CharacterID == "" || c.Slot < 0 || c.Slot >= defaultCharacterSlots || slots[c.Slot] || ids[c.CharacterID] {
			return nil, nil, errCurrentAdventureStateInvalid
		}
		slots[c.Slot], ids[c.CharacterID] = true, true
	}
	busy, known := currentAdventureExpeditionBusyIdentities(runtime, rows)
	members := make(map[byte]currentAdventureExpeditionSentMember)
	var idle []adventuregroup.ExpeditionMember
	if known {
		for _, c := range rows {
			job := numericCharacterStat(c.Job)
			grow, validGrow := currentAdventureExpeditionBranch(c)
			if busy[c.CharacterID] || c.Name == "" || c.Level <= 0 || job < 0 || job > 255 || !validGrow {
				continue
			}
			selector := byte(c.Slot)
			members[selector] = currentAdventureExpeditionSentMember{RecordID: c.CharacterID, Name: c.Name, Job: byte(job), Grow: grow, PackedGrow: characterGrowTypeInt(c)}
			idle = append(idle, adventuregroup.ExpeditionMember{CharacterRecordID: c.CharacterID, CharacterID: uint16(selector), Name: c.Name, Level: currentAdventureInfoRosterHonorLevel(c), Job: byte(job), GrowType: grow})
		}
	}
	projection := runtime
	projection.Expeditions = make(map[string]adventuregroup.Expedition, len(runtime.Expeditions)+1)
	for key, value := range runtime.Expeditions {
		value.Members = append([]adventuregroup.ExpeditionMember(nil), value.Members...)
		for i, member := range value.Members {
			// Legacy runtime stored base character level here. This is a wire
			// HonorExpert projection; leave the persisted expedition unchanged.
			value.Members[i].Level = 0
			if c, found := currentAdventureExpeditionMemberCharacter(member, rows); found {
				value.Members[i].Level = currentAdventureInfoRosterHonorLevel(c)
				if grow, valid := currentAdventureExpeditionBranch(c); valid {
					value.Members[i].GrowType = grow
				}
			}
		}
		projection.Expeditions[key] = value
	}
	if len(configs) > 0 {
		config := configs[0]
		for _, area := range config.ExpeditionAreas {
			key := adventuregroup.ExpeditionKey(area.Index)
			if _, active := projection.Expeditions[key]; !active && area.Index > 0 {
				projection.Expeditions[key] = adventuregroup.Expedition{Area: area.Index, Attributes: config.AreaAttributes(area.Index, now)}
			}
		}
	}
	if len(idle) > 0 {
		projection.Expeditions["0"] = adventuregroup.Expedition{Members: idle}
	}
	return buildCurrentAdventureExpeditionStateBody(projection, now), members, nil
}

// The expedition native tables accept the branch, while the repository keeps
// the full packed branch/awakening value for every other character projection.
func currentAdventureExpeditionBranch(c repo.CharacterRecord) (byte, bool) {
	growth := characterGrowType(c)
	return byte(growth.Branch), growth.Present && growth.InDomain && growth.Branch >= 1 && growth.Branch <= 5
}

func currentAdventureExpeditionMemberCharacter(member adventuregroup.ExpeditionMember, characters []repo.CharacterRecord) (repo.CharacterRecord, bool) {
	var match repo.CharacterRecord
	found := false
	for _, c := range characters {
		matches := member.CharacterRecordID != "" && c.CharacterID == member.CharacterRecordID
		if member.CharacterRecordID == "" {
			branch, valid := currentAdventureExpeditionBranch(c)
			matches = c.Slot == int(member.CharacterID) && c.Name == member.Name && numericCharacterStat(c.Job) == int(member.Job) &&
				(characterGrowTypeByte(c) == member.GrowType || valid && branch == member.GrowType)
		}
		if matches {
			if found {
				return repo.CharacterRecord{}, false
			}
			match, found = c, true
		}
	}
	return match, found
}

// Legacy records only contain the wire slot. Never reinterpret it as a DB ID;
// require their recorded slot/name/job/grow to identify exactly one real row.
func currentAdventureExpeditionBusyIdentities(runtime adventuregroup.RuntimeState, characters []repo.CharacterRecord) (map[string]bool, bool) {
	busy := make(map[string]bool)
	for _, expedition := range runtime.Expeditions {
		for _, member := range expedition.Members {
			c, found := currentAdventureExpeditionMemberCharacter(member, characters)
			if !found || c.CharacterID == "" {
				return busy, false
			}
			busy[c.CharacterID] = true
		}
	}
	return busy, true
}

func (s *Service) validateCurrentAdventureExpeditionSentMembers(session *gameSession, requests []currentAdventureExpeditionMemberRequest, characters []repo.CharacterRecord) bool {
	view := session.currentAdventureExpeditionView
	if !s.currentAdventureExpeditionViewCurrent(session, view) {
		return false
	}
	for _, r := range requests {
		published, ok := view.Members[r.Selector]
		if !ok || published.Job != r.Job || published.Grow != r.GrowType {
			return false
		}
		matches := 0
		for _, c := range characters {
			branch, valid := currentAdventureExpeditionBranch(c)
			if c.Slot == int(r.Selector) && c.AccountID == view.AccountID && c.CharacterID == published.RecordID && c.Name == published.Name && numericCharacterStat(c.Job) == int(r.Job) && valid && branch == r.GrowType && characterGrowTypeInt(c) == published.PackedGrow {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	return true
}
