package raid

import (
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// These are backend-owned identities frozen at raid start. No packet adapter
// may substitute character/account/group values supplied by a client.
type FiendWarParticipant struct {
	CharacterID, AccountID string
	GroupID                uint32
}

type fiendWarMovementReservation struct {
	group, from, to uint32
	characters      []string
	confirmed       map[string]bool
	end             time.Time
}

// FiendWarMovement owns occupancy and the all-member entry barrier only.
// It does not assign unknown native area-state bytes or manufacture combat
// results. Bridge integration must prepare actual maps before confirming and
// release a dungeon run only after its owned completion/abandonment event.
// Copy-returning operations are published under the bridge instance lock.
type FiendWarMovement struct {
	instance       string
	areas          map[uint32]FiendWarArea
	members        map[string]FiendWarParticipant
	locations      map[string]uint32
	pending        map[string]fiendWarMovementReservation
	active         map[string]fiendWarMovementReservation
	used           map[string]bool
	last, deadline time.Time
	closed         bool
}

func fiendWarInstanceKey(value string) bool {
	x, err := hex.DecodeString(value)
	return err == nil && len(x) == 16
}

func (p *FiendWarProgram) BeginMovement(instance string, members []FiendWarParticipant, now time.Time) (*FiendWarMovement, error) {
	if p == nil || p.configuration.MemberMin == 0 || len(p.configuration.Battle.PhaseLimitSeconds) != 1 || p.configuration.Battle.PhaseLimitSeconds[0] == 0 || !fiendWarInstanceKey(instance) || now.IsZero() || len(members) < int(p.configuration.MemberMin) || len(members) > int(p.configuration.MemberMax) {
		return nil, fmt.Errorf("Fiend War movement start identity/clock/member limits invalid")
	}
	s := &FiendWarMovement{instance: instance, areas: map[uint32]FiendWarArea{}, members: map[string]FiendWarParticipant{}, locations: map[string]uint32{}, pending: map[string]fiendWarMovementReservation{}, active: map[string]fiendWarMovementReservation{}, used: map[string]bool{}, last: now, deadline: now.Add(time.Duration(p.configuration.Battle.PhaseLimitSeconds[0]) * time.Second)}
	var origin uint32
	for _, a := range p.configuration.Areas {
		s.areas[a.ID] = a
		// The actual staging area is the unique non-dungeon node admitting
		// the whole raid. Derive it from configuration rather than a new ID.
		if a.DungeonID == -1 && a.MemberMax == p.configuration.MemberMax {
			if origin != 0 {
				return nil, fmt.Errorf("Fiend War staging area ambiguous")
			}
			origin = a.ID
		}
	}
	if origin == 0 {
		return nil, fmt.Errorf("Fiend War staging area unavailable")
	}
	accounts := map[string]bool{}
	groups := map[uint32]int{}
	for _, m := range members {
		if m.CharacterID == "" || m.AccountID == "" || m.GroupID == 0 || accounts[m.AccountID] || s.members[m.CharacterID].CharacterID != "" {
			return nil, fmt.Errorf("Fiend War frozen participant identity invalid")
		}
		accounts[m.AccountID] = true
		groups[m.GroupID]++
		s.members[m.CharacterID], s.locations[m.CharacterID] = m, origin
	}
	// A group must fit at least one configured adjacent node. It cannot
	// silently split an eight-member raid into a four-member area.
	for _, count := range groups {
		fits := false
		for _, id := range s.areas[origin].Moves {
			fits = fits || count <= int(s.areas[id].MemberMax)
		}
		if !fits {
			return nil, fmt.Errorf("Fiend War group exceeds adjacent area capacity")
		}
	}
	return s, nil
}

func (s *FiendWarMovement) clone() *FiendWarMovement {
	n := *s
	n.members, n.locations, n.used = copyAntonMap(s.members), copyAntonMap(s.locations), copyAntonMap(s.used)
	n.pending, n.active = map[string]fiendWarMovementReservation{}, map[string]fiendWarMovementReservation{}
	for key, r := range s.pending {
		r.characters = append([]string(nil), r.characters...)
		r.confirmed = copyAntonMap(r.confirmed)
		n.pending[key] = r
	}
	for key, r := range s.active {
		r.characters = append([]string(nil), r.characters...)
		r.confirmed = copyAntonMap(r.confirmed)
		n.active[key] = r
	}
	return &n
}

func (s *FiendWarMovement) validate(instance string, actor FiendWarParticipant, now time.Time) error {
	if s == nil || s.closed || instance != s.instance || actor.CharacterID == "" || s.members[actor.CharacterID] != actor || now.IsZero() || now.Before(s.last) || !now.Before(s.deadline) {
		return fmt.Errorf("Fiend War movement owner or phase clock invalid")
	}
	return nil
}

func (s *FiendWarMovement) Location(character string) (uint32, bool) {
	if s == nil {
		return 0, false
	}
	a, exists := s.locations[character]
	return a, exists
}

type FiendWarAreaRun struct {
	GroupID, From, To uint32
	DungeonID         int64
	Deadline          time.Time
	Participants      []FiendWarParticipant
}

// AreaRun returns a detached frozen identity/deadline snapshot for the bridge
// to bind to its actual prepared dungeon run. It carries no client bytes.
func (s *FiendWarMovement) AreaRun(run string) (FiendWarAreaRun, bool) {
	if s == nil {
		return FiendWarAreaRun{}, false
	}
	r, exists := s.active[run]
	if !exists {
		return FiendWarAreaRun{}, false
	}
	result := FiendWarAreaRun{GroupID: r.group, From: r.from, To: r.to, DungeonID: s.areas[r.to].DungeonID, Deadline: r.end}
	for _, character := range r.characters {
		result.Participants = append(result.Participants, s.members[character])
	}
	return result, true
}

func (s *FiendWarMovement) PrepareMove(instance, run string, actor FiendWarParticipant, from, to uint32, now time.Time) (*FiendWarMovement, error) {
	if err := s.validate(instance, actor, now); err != nil {
		return nil, err
	}
	if !fiendWarInstanceKey(run) || s.used[run] {
		return nil, fmt.Errorf("Fiend War movement run reused or invalid")
	}
	if r, ok := s.pending[run]; ok {
		if r.group == actor.GroupID && r.from == from && r.to == to && now.Before(r.end) {
			n := s.clone()
			n.last = now
			return n, nil
		}
		return nil, fmt.Errorf("Fiend War reservation identity changed")
	}
	area, exists := s.areas[from]
	target, targetExists := s.areas[to]
	edge := false
	for _, id := range area.Moves {
		edge = edge || id == to
	}
	if !exists || !targetExists || !edge {
		return nil, fmt.Errorf("Fiend War movement outside actual area graph")
	}
	var characters []string
	for _, m := range s.members {
		if m.GroupID == actor.GroupID {
			if s.locations[m.CharacterID] != from {
				return nil, fmt.Errorf("Fiend War group is not together at origin")
			}
			characters = append(characters, m.CharacterID)
		}
	}
	for _, r := range s.pending {
		if r.group == actor.GroupID {
			return nil, fmt.Errorf("Fiend War group already preparing entry")
		}
	}
	sort.Strings(characters)
	for _, r := range s.active {
		if r.group == actor.GroupID {
			return nil, fmt.Errorf("Fiend War group still owns an active area run")
		}
	}
	occupied := 0
	for _, id := range s.locations {
		if id == to {
			occupied++
		}
	}
	for _, r := range s.pending {
		if r.to == to {
			occupied += len(r.characters)
		}
	}
	if occupied+len(characters) > int(target.MemberMax) {
		return nil, fmt.Errorf("Fiend War destination capacity exhausted")
	}
	n := s.clone()
	n.pending[run] = fiendWarMovementReservation{group: actor.GroupID, from: from, to: to, characters: characters, confirmed: map[string]bool{}, end: s.deadline}
	n.last = now
	return n, nil
}

func (s *FiendWarMovement) ConfirmMove(instance, run string, actor FiendWarParticipant, now time.Time) (*FiendWarMovement, bool, error) {
	if err := s.validate(instance, actor, now); err != nil {
		return nil, false, err
	}
	r, ok := s.pending[run]
	if !ok || r.group != actor.GroupID || !now.Before(r.end) {
		return nil, false, fmt.Errorf("Fiend War prepared movement unavailable or expired")
	}
	n := s.clone()
	r = n.pending[run]
	r.confirmed[actor.CharacterID] = true
	committed := len(r.confirmed) == len(r.characters)
	if committed {
		for _, character := range r.characters {
			n.locations[character] = r.to
		}
		delete(n.pending, run)
		n.used[run] = true
		if area := n.areas[r.to]; area.DungeonID > 0 {
			limit := now.Add(time.Duration(area.LimitSeconds) * time.Second)
			if limit.Before(r.end) {
				r.end = limit
			}
			n.active[run] = r
		}
	} else {
		n.pending[run] = r
	}
	n.last = now
	return n, committed, nil
}

func (s *FiendWarMovement) CancelPreparedMove(instance, run string, actor FiendWarParticipant, now time.Time) (*FiendWarMovement, error) {
	if err := s.validate(instance, actor, now); err != nil {
		return nil, err
	}
	r, exists := s.pending[run]
	if !exists || r.group != actor.GroupID {
		return nil, fmt.Errorf("Fiend War cancellation does not own reservation")
	}
	n := s.clone()
	delete(n.pending, run)
	n.used[run] = true
	n.last = now
	return n, nil
}

// ReleaseAreaRun is an owned backend completion/abandonment hook. It does not
// count clears or relocate players; those effects belong to the battle reducer.
func (s *FiendWarMovement) ReleaseAreaRun(instance, run string, actor FiendWarParticipant, now time.Time) (*FiendWarMovement, error) {
	if err := s.validate(instance, actor, now); err != nil {
		return nil, err
	}
	r, exists := s.active[run]
	if !exists || r.group != actor.GroupID {
		return nil, fmt.Errorf("Fiend War area run is not owned")
	}
	n := s.clone()
	delete(n.active, run)
	n.last = now
	return n, nil
}

func (s *FiendWarMovement) Tick(now time.Time) (*FiendWarMovement, error) {
	if s == nil || now.IsZero() || now.Before(s.last) {
		return nil, fmt.Errorf("Fiend War movement clock invalid")
	}
	n := s.clone()
	for key, r := range n.pending {
		if !now.Before(r.end) {
			delete(n.pending, key)
			n.used[key] = true
		}
	}
	if !now.Before(n.deadline) {
		n.pending = map[string]fiendWarMovementReservation{}
		n.active = map[string]fiendWarMovementReservation{}
		n.closed = true
	}
	n.last = now
	return n, nil
}
