package quest

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

// Numeric character stats already participate in the same locked settlement as
// inventory and quests. This versioned namespace is NOT a coupon/manual grant.
const ticketAuthorizationPrefix = "story_ticket_v1_"
const ticketAuthorizationVersion = 1

var ErrTicketStoryAuthorization = errors.New("invalid level ticket story authorization")

type TicketStoryAuthorization struct {
	CharacterID        string
	PolicyVersion      int64
	Cutoff             int
	Generation         int64
	Revision           int64
	SourceItemID       int64
	GrantedAt          int64
	BlockedQuestionIDs []int64
	DamagedQuestionIDs []int64
}

// Identity names the latest actual ticket settlement. Reconnecting and question
// retries do not create another generation or consume an item.
func (a TicketStoryAuthorization) Identity() string {
	return fmt.Sprintf("%s:level_up_ticket:v%d:%d", a.CharacterID, a.PolicyVersion, a.Generation)
}

func ReadTicketStoryAuthorization(c repo.CharacterRecord) (TicketStoryAuthorization, bool, error) {
	s := c.Stats
	v, present := s[ticketAuthorizationPrefix+"version"]
	if !present {
		return TicketStoryAuthorization{}, false, nil
	}
	a := TicketStoryAuthorization{CharacterID: c.CharacterID, PolicyVersion: v, Cutoff: int(s[ticketAuthorizationPrefix+"cutoff"]), Generation: s[ticketAuthorizationPrefix+"generation"], Revision: s[ticketAuthorizationPrefix+"revision"], SourceItemID: s[ticketAuthorizationPrefix+"source_item"], GrantedAt: s[ticketAuthorizationPrefix+"granted_at"]}
	if c.CharacterID == "" || v != ticketAuthorizationVersion || a.Cutoff < 1 || a.Cutoff >= c.Level || a.Generation < 1 || a.Revision < 1 || a.SourceItemID <= 0 || a.GrantedAt <= 0 {
		return TicketStoryAuthorization{}, true, ErrTicketStoryAuthorization
	}
	blocked, damaged := map[int64]struct{}{}, map[int64]struct{}{}
	for k, v := range s {
		if !strings.HasPrefix(k, ticketAuthorizationPrefix+"question_") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(k, ticketAuthorizationPrefix+"question_"), 10, 64)
		if err != nil || id <= 0 || (v != 1 && v != 2) {
			return TicketStoryAuthorization{}, true, ErrTicketStoryAuthorization
		}
		if v == 1 {
			blocked[id] = struct{}{}
		} else {
			damaged[id] = struct{}{}
		}
	}
	a.BlockedQuestionIDs = sortedQuestIDSet(blocked)
	a.DamagedQuestionIDs = sortedQuestIDSet(damaged)
	return a, true, nil
}

func saveTicketStoryAuthorization(c *repo.CharacterRecord, a TicketStoryAuthorization, p AutoCompleteMainPlan) {
	if c.Stats == nil {
		c.Stats = map[string]int64{}
	}
	for k := range c.Stats {
		if strings.HasPrefix(k, ticketAuthorizationPrefix+"question_") {
			delete(c.Stats, k)
		}
	}
	for _, id := range p.BlockedQuestionIDs {
		c.Stats[ticketAuthorizationPrefix+"question_"+strconv.FormatInt(id, 10)] = 1
	}
	for _, id := range p.DamagedQuestionIDs {
		c.Stats[ticketAuthorizationPrefix+"question_"+strconv.FormatInt(id, 10)] = 2
	}
	for k, v := range map[string]int64{"version": a.PolicyVersion, "cutoff": int64(a.Cutoff), "generation": a.Generation, "revision": a.Revision, "source_item": a.SourceItemID, "granted_at": a.GrantedAt} {
		c.Stats[ticketAuthorizationPrefix+k] = v
	}
}

// Called only after a real ticket's level plan has passed validation, inside its
// existing settlement. Caller saves c and p.Record in that SAME transaction.
func (c *Catalog) AuthorizeLevelUpTicketStory(character *repo.CharacterRecord, r repo.QuestRecord, sourceItemID int64, at time.Time) (AutoCompleteMainPlan, error) {
	if c == nil || character == nil || sourceItemID <= 0 || at.Unix() <= 0 {
		return AutoCompleteMainPlan{}, ErrTicketStoryAuthorization
	}
	a, found, err := ReadTicketStoryAuthorization(*character)
	if err != nil {
		return AutoCompleteMainPlan{}, err
	}
	if !found {
		a = TicketStoryAuthorization{CharacterID: character.CharacterID, PolicyVersion: ticketAuthorizationVersion}
	}
	if a.Generation == math.MaxInt64 || a.Revision == math.MaxInt64 {
		return AutoCompleteMainPlan{}, ErrTicketStoryAuthorization
	}
	cutoff := character.Level - 1
	if cutoff > a.Cutoff {
		a.Cutoff = cutoff
	}
	a.Generation++
	a.Revision++
	a.SourceItemID = sourceItemID
	a.GrantedAt = at.Unix()
	ch, err := ticketStoryEligibility(*character, a.Cutoff)
	if err != nil || r.CharacterID != character.CharacterID {
		return AutoCompleteMainPlan{}, ErrTicketStoryAuthorization
	}
	p, err := c.PlanLevelUpTicketQuestClear(r, ch, at)
	if err != nil {
		return AutoCompleteMainPlan{}, err
	}
	saveTicketStoryAuthorization(character, a, p)
	return p, nil
}

func ticketStoryEligibility(c repo.CharacterRecord, cutoff int) (CharacterEligibility, error) {
	job, err := strconv.Atoi(c.Job)
	grow, ok := repo.CharacterGrowTypeInDomain(c)
	ch := CharacterEligibility{Level: cutoff, Job: job, GrowType: int(grow)}
	if err != nil || !ok || !definitionCharacterJobValid(ch) {
		return ch, ErrTicketStoryAuthorization
	}
	return ch, nil
}

// No repositories and no nested transaction: the normal Question finish owns
// the lock, reward receipt and rollback. Only a committed explicit answer may
// resume this policy; an old completed/answerless save is reported, not repaired.
func (c *Catalog) ResumeLevelUpTicketStory(character *repo.CharacterRecord, r repo.QuestRecord, finishedID int64, at time.Time) (AutoCompleteMainPlan, bool, error) {
	if character == nil {
		return AutoCompleteMainPlan{}, false, ErrTicketStoryAuthorization
	}
	// O(1) for all characters without a ticket grant, before a catalog lookup.
	if _, ok := character.Stats[ticketAuthorizationPrefix+"version"]; !ok {
		return AutoCompleteMainPlan{}, false, nil
	}
	if c == nil {
		return AutoCompleteMainPlan{}, false, ErrTicketStoryAuthorization
	}
	d, ok := c.byID[finishedID]
	if !ok || normalizeQuestTag(d.Type) != "question" {
		return AutoCompleteMainPlan{}, false, nil
	}
	a, _, err := ReadTicketStoryAuthorization(*character)
	if err != nil {
		return AutoCompleteMainPlan{}, false, err
	}
	s, ok := questStateFor(r, finishedID)
	if !ok || !isCompletedQuestStatus(s.Status) || !validStoryQuestionAnswer(d, s) || r.CharacterID != character.CharacterID || at.Unix() <= 0 {
		return AutoCompleteMainPlan{}, false, ErrTicketStoryAuthorization
	}
	if a.Revision == math.MaxInt64 {
		return AutoCompleteMainPlan{}, false, ErrTicketStoryAuthorization
	}
	ch, err := ticketStoryEligibility(*character, a.Cutoff)
	if err != nil {
		return AutoCompleteMainPlan{}, false, err
	}
	p, err := c.PlanLevelUpTicketQuestClear(r, ch, at)
	if err != nil {
		return AutoCompleteMainPlan{}, false, err
	}
	// Repeating the event with the same resulting state is a true zero-write plan.
	if len(p.ChangedFields) == 0 && sameStoryIDs(a.BlockedQuestionIDs, p.BlockedQuestionIDs) && sameStoryIDs(a.DamagedQuestionIDs, p.DamagedQuestionIDs) {
		return p, false, nil
	}
	a.Revision++
	saveTicketStoryAuthorization(character, a, p)
	return p, true, nil
}

func sameStoryIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[int64]struct{}{}
	for _, id := range a {
		set[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}
