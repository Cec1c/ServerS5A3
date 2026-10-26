package dnfbridge

const (
	currentFiendWarAdmissionPhaseQueue        = "queue_pending_move"
	currentFiendWarAdmissionPhaseTownArea     = "op36_town_set_user_area"
	currentFiendWarAdmissionPhaseTownPosition = "op35_town_set_user_position"
)

// A key is comparable and intentionally excludes phase: the op36 request and
// later op35 reports describe the same pending move.
type currentFiendWarAdmissionDiagnosticKey struct {
	instance  string
	character uint16
	from      int
	to        int
	reason    string
}

// currentFiendWarAdmissionDiagnostic is a per-attempt snapshot. It is passed
// explicitly through the FiendWar selection path so maintenance retries can
// keep their existing silent behavior without sharing mutable diagnostics.
type currentFiendWarAdmissionDiagnostic struct {
	phase            string
	charID           uint16
	connID           string
	raidKey          uint32
	raidInstanceID   string
	memberArea       int
	memberGroup      byte
	memberAreaOpen   bool
	memberAreaGroup  byte
	townID           int
	townArea         int
	positionValid    bool
	sceneReady       bool
	requestDungeon   uint32
	pendingPresent   bool
	pendingInstance  string
	pendingFrom      int
	pendingTo        int
	pendingConfirmed bool
	reason           string
}

func newCurrentFiendWarAdmissionDiagnostic(p *gameSession, dungeonID uint32) currentFiendWarAdmissionDiagnostic {
	d := currentFiendWarAdmissionDiagnostic{
		phase:          "normal_selection",
		memberArea:     -1,
		pendingFrom:    -1,
		pendingTo:      -1,
		requestDungeon: dungeonID,
	}
	if p == nil {
		return d
	}
	d.charID, d.connID = p.selectedCharacterID, p.connID
	p.townMu.Lock()
	position := p.townPositionSnapshot
	pending := p.fiendWarPendingMove
	d.sceneReady = p.townSceneReadyCharacterID == p.selectedCharacterID
	if pending != nil && pending.character == p.selectedCharacterID && pending.instance != "" {
		d.pendingPresent = true
		d.pendingInstance = pending.instance
		d.pendingFrom, d.pendingTo = pending.from, pending.to
		d.pendingConfirmed = pending.confirmed
	}
	p.townMu.Unlock()
	d.townID, d.townArea = int(position.TownID), int(position.AreaID)
	d.positionValid = position.CharacterID == p.selectedCharacterID && position.PositionValid
	return d
}

func (d *currentFiendWarAdmissionDiagnostic) captureRaid(state runtimeRaidState) {
	if d == nil {
		return
	}
	d.raidKey, d.raidInstanceID = state.RaidKey, ""
	if state.Battle == nil {
		return
	}
	d.raidInstanceID = state.Battle.InstanceID
	if state.Battle.FiendWar == nil {
		return
	}
	d.memberGroup = state.Battle.MemberGroups[d.charID]
	d.memberArea = state.Battle.FiendWar.MemberArea(d.charID, d.memberGroup)
	d.memberAreaOpen = state.Battle.FiendWar.AreaOpen(d.memberArea)
	d.memberAreaGroup = state.Battle.FiendWar.Areas[d.memberArea].Group
	if d.requestDungeon == 0 {
		d.requestDungeon = uint32(state.Battle.FiendWar.Config.Areas[d.memberArea].Dungeon)
	}
}

func (d *currentFiendWarAdmissionDiagnostic) setReason(reason string) {
	if d != nil {
		d.reason = reason
	}
}

// currentFiendWarQueuedAdmissionDiagnostic limits automatic diagnostics to a
// real pending move that has not entered a dungeon runtime yet.
func currentFiendWarQueuedAdmissionDiagnostic(p *gameSession, phase string) (currentFiendWarAdmissionDiagnostic, bool) {
	if p == nil || !isCurrentFiendWarChannel(p) {
		return currentFiendWarAdmissionDiagnostic{}, false
	}
	p.dungeon.mu.Lock()
	runtimePresent := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	if runtimePresent {
		return currentFiendWarAdmissionDiagnostic{}, false
	}
	d := newCurrentFiendWarAdmissionDiagnostic(p, 0)
	if !d.pendingPresent {
		return currentFiendWarAdmissionDiagnostic{}, false
	}
	d.phase = phase
	return d, true
}

// logCurrentFiendWarAdmissionDiagnostic writes a bounded application event
// through the service logger. It intentionally does not use packet logging.
func (s *Service) logCurrentFiendWarAdmissionDiagnostic(d currentFiendWarAdmissionDiagnostic) {
	if s == nil || d.charID == 0 || d.reason == "" {
		return
	}
	s.logInfo("dnfbridge fiendwar admission diagnostic",
		"kind", "game-fiendwar-admission-diagnostic",
		"phase", d.phase,
		"char_id", d.charID,
		"conn_id", d.connID,
		"raid_key", d.raidKey,
		"raid_instance_id", d.raidInstanceID,
		"member_area", d.memberArea,
		"member_group", d.memberGroup,
		"member_area_open", d.memberAreaOpen,
		"member_area_group", d.memberAreaGroup,
		"town_id", d.townID,
		"town_area", d.townArea,
		"position_valid", d.positionValid,
		"scene_ready", d.sceneReady,
		"request_dungeon", d.requestDungeon,
		"pending_present", d.pendingPresent,
		"pending_instance", d.pendingInstance,
		"pending_from", d.pendingFrom,
		"pending_to", d.pendingTo,
		"pending_confirmed", d.pendingConfirmed,
		"reason", d.reason)
}

// logCurrentFiendWarAutomaticAdmissionDiagnostic is called only from the
// op35/op36 queue path, never from maintenance. One session keeps only its
// latest comparable key, so repeated position reports do not grow state or
// flood the log.
func (s *Service) logCurrentFiendWarAutomaticAdmissionDiagnostic(p *gameSession, d currentFiendWarAdmissionDiagnostic) {
	if s == nil || p == nil || !d.pendingPresent || d.reason == "" {
		return
	}
	key := currentFiendWarAdmissionDiagnosticKey{
		instance:  d.pendingInstance,
		character: d.charID,
		from:      d.pendingFrom,
		to:        d.pendingTo,
		reason:    d.reason,
	}
	p.townMu.Lock()
	if p.fiendWarAdmissionDiagnosticKey == key {
		p.townMu.Unlock()
		return
	}
	p.fiendWarAdmissionDiagnosticKey = key
	p.townMu.Unlock()
	s.logCurrentFiendWarAdmissionDiagnostic(d)
}
