package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"strings"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

func townMapPathLooksLikeCoupleRoom(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"))
	p = strings.TrimPrefix(p, "map/")
	return strings.HasPrefix(p, "coupleroom/") && strings.HasSuffix(p, ".map")
}

func weddingPairInstance(a, b uint16) uint32 {
	if a == 0 || b == 0 || a == b {
		return 0
	}
	if a > b {
		a, b = b, a
	}
	return uint32(a)<<16 | uint32(b)
}

func (s *Service) currentWeddingRoomInstance(ctx context.Context, session *gameSession, c r.CharacterRecord) (uint32, error) {
	self, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || c.CharacterID != strconv.Itoa(int(self.character)) || c.AccountID != s.accountIDForSession(session) {
		return 0, wedding.ErrOwnership
	}
	state, err := wedding.Read(c)
	if err != nil || state.Phase != wedding.Married {
		return 0, wedding.ErrState
	}
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return 0, wedding.ErrUnavailable
	}
	partner, found, err := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, wedding.ErrState
	}
	if _, _, err = wedding.ValidatePair(c, partner); err != nil {
		return 0, err
	}
	return weddingPairInstance(self.character, uint16(state.PartnerID)), nil
}

func (s *Service) currentWeddingRoomScene(actor boundGameSessionCharacter, partner string) bool {
	n, err := wedding.NumericID(partner)
	if err != nil || s.onlinePlayers == nil || !s.boundGameSessionCharacterCurrent(actor) {
		return false
	}
	p, ok := s.onlinePlayers.PlayerForCharacter(actor.character)
	if !ok || p.Session != actor.session || p.TownID != 101 || p.AreaID != 0 || p.Instance != weddingPairInstance(actor.character, uint16(n)) {
		return false
	}
	area, ok := s.townArea(101, 0)
	return ok && townMapPathLooksLikeCoupleRoom(area.MapPath)
}

func buildCurrentWeddingRoom(room wedding.Room) []byte {
	slots := make([]int, 0, len(room.Placed))
	for slot := range room.Placed {
		slots = append(slots, int(slot))
	}
	sort.Ints(slots)
	b := make([]byte, 4+len(slots)*6)
	binary.LittleEndian.PutUint32(b, uint32(len(slots)))
	for i, slot := range slots {
		at := 4 + i*6
		binary.LittleEndian.PutUint16(b[at:], uint16(slot))
		binary.LittleEndian.PutUint32(b[at+2:], uint32(room.Placed[uint16(slot)].ItemID))
	}
	return append(b, buildCurrentWeddingTree(room.Tree)...)
}

func buildCurrentWeddingRoomInventory(room wedding.Room) []byte {
	slots := make([]int, 0, len(room.Bag))
	for slot := range room.Bag {
		slots = append(slots, int(slot))
	}
	sort.Ints(slots)
	b := make([]byte, 4+len(slots)*10)
	binary.LittleEndian.PutUint32(b, uint32(len(slots)))
	for i, slot := range slots {
		at := 4 + i*10
		stack := room.Bag[uint16(slot)]
		binary.LittleEndian.PutUint16(b[at:], uint16(slot))
		binary.LittleEndian.PutUint32(b[at+2:], uint32(stack.ItemID))
		binary.LittleEndian.PutUint32(b[at+6:], uint32(stack.Count))
	}
	// The current native reader consumes one reserved u32 after all rows.
	return binary.LittleEndian.AppendUint32(b, 0)
}

func (s *Service) writeCurrentWeddingRoom(session *gameSession, room wedding.Room) error {
	rules, err := s.currentWeddingRules()
	if err != nil {
		return err
	}
	if room.Tree != (wedding.TreeState{}) {
		extensions, e := s.currentWeddingExtensionRules()
		if e != nil {
			return e
		}
		now := s.gameplayNow()
		room.Tree, e = wedding.CurrentTree(room.Tree, extensions, now, currentWeddingDay(now))
		if e != nil {
			return e
		}
	}
	if err = s.sendGameUpperRawClass(session, 1011, buildCurrentWeddingRoomRefresh(room, rules), 0); err != nil {
		return err
	}
	if err := s.sendGameUpperRawClass(session, 1008, buildCurrentWeddingRoom(room), 0); err != nil {
		return err
	}
	return s.sendGameUpperRawClass(session, 1009, buildCurrentWeddingRoomInventory(room), 0)
}

func (s *Service) sendCurrentWeddingRoomSnapshot(session *gameSession, c r.CharacterRecord) error {
	state, err := wedding.Read(c)
	if err != nil {
		return err
	}
	if state.Phase != wedding.Married {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok || repos.Character == nil {
		return wedding.ErrUnavailable
	}
	partner, found, err := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
	if err != nil {
		return err
	}
	if !found {
		return wedding.ErrState
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		return err
	}
	room, err := owner.ReadRoom(ctx, c, partner)
	if err != nil {
		return err
	}
	return s.writeCurrentWeddingRoom(session, room)
}

func weddingResultACK(result int32) []byte {
	b := []byte{1, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[1:], uint32(result))
	return b
}

func (s *Service) handleCurrentWeddingRoomOpen(session *gameSession, body []byte) error {
	flag := byte(0)
	if len(body) == 14 {
		flag = body[13]
	}
	fail := func(reason string) error {
		s.logInfo("dnfbridge wedding room editor result", "event", "game-wedding-room-editor-result", "operation_flag", flag, "accepted", false, "reason", reason)
		return s.sendGameUpperRawClass(session, 1045, append(weddingResultACK(-1), flag), 1)
	}
	actor, ok := s.boundGameSessionCharacterSnapshot(session)
	fields := s.currentWeddingDiagnosticFields(session)
	fields = append(fields, "event", "game-wedding-room-editor-request", "body_len", len(body), "operation_flag", flag)
	s.logInfo("dnfbridge wedding room editor diagnostic", fields...)
	if len(body) != 14 || flag > 1 {
		return fail("request_or_binding")
	}
	if !ok {
		if flag == 1 {
			// Native UI teardown can close the inventory after return-select
			// has already retired its character. Acknowledge that no-op without
			// evicting any current editor or displaying a spurious error.
			s.logInfo("dnfbridge wedding room editor result", "event", "game-wedding-room-editor-result", "operation_flag", flag, "accepted", true, "editing", false, "reason", "already_unbound")
			return s.sendGameUpperRawClass(session, 1045, append(weddingResultACK(0), flag), 1)
		}
		return fail("request_or_binding")
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	// Native 1AC5372 writes 0 when opening. 1AC31E6 writes !BL,
	// and ACK handler 1AC5507 only opens the inventory for flag 0.
	if flag == 1 {
		// Closing only releases this exact session generation. It must also
		// work after an area transition; a close cannot evict the partner.
		for scope, editor := range s.wedding.roomEditors {
			if editor == actor {
				delete(s.wedding.roomEditors, scope)
			}
		}
		s.logInfo("dnfbridge wedding room editor result", "event", "game-wedding-room-editor-result", "char_id", actor.character, "operation_flag", flag, "accepted", true, "editing", false)
		return s.sendGameUpperRawClass(session, 1045, append(weddingResultACK(0), flag), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	req, _, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return fail("partner")
	}
	if !s.currentWeddingRoomScene(actor, req.Partner.CharacterID) {
		return fail("scene")
	}
	scope, _ := r.WeddingPairScope([]string{req.Actor.CharacterID, req.Partner.CharacterID})
	if s.wedding.roomEditors == nil {
		s.wedding.roomEditors = map[string]boundGameSessionCharacter{}
	}
	if old, used := s.wedding.roomEditors[scope]; used && old != actor {
		partnerID := ""
		switch strconv.Itoa(int(old.character)) {
		case req.Actor.CharacterID:
			partnerID = req.Partner.CharacterID
		case req.Partner.CharacterID:
			partnerID = req.Actor.CharacterID
		}
		if s.currentWeddingRoomScene(old, partnerID) {
			return fail("partner_editing")
		}
		delete(s.wedding.roomEditors, scope)
	}
	s.wedding.roomEditors[scope] = actor
	if err = s.sendGameUpperRawClass(session, 1045, append(weddingResultACK(0), flag), 1); err != nil {
		delete(s.wedding.roomEditors, scope)
		return err
	}
	if !s.boundGameSessionCharacterCurrent(actor) {
		delete(s.wedding.roomEditors, scope)
		return nil
	}
	c, err := s.currentWeddingCharacter(ctx, actor)
	if err != nil {
		delete(s.wedding.roomEditors, scope)
		return err
	}
	if err = s.sendCurrentWeddingRoomSnapshot(session, c); err != nil {
		delete(s.wedding.roomEditors, scope)
		return err
	}
	s.logInfo("dnfbridge wedding room editor result", "event", "game-wedding-room-editor-result", "char_id", actor.character, "operation_flag", flag, "accepted", true, "editing", true)
	return nil
}

func decodeCurrentWeddingRoomMove(body []byte) (wedding.RoomMove, bool) {
	if len(body) != 27 || body[13] > 1 || body[20] > 1 {
		return wedding.RoomMove{}, false
	}
	// 1AC583F and 1AC30E5 append two seven-byte endpoints after the native
	// prefix: container:u8, slot:u16, item:u32. There is no quantity field.
	q := wedding.RoomMove{FromBag: body[13] == 1, FromSlot: binary.LittleEndian.Uint16(body[14:]), Count: 1, SourceItemID: int64(binary.LittleEndian.Uint32(body[16:])), ToBag: body[20] == 1, ToSlot: binary.LittleEndian.Uint16(body[21:]), ExpectedDestination: int64(binary.LittleEndian.Uint32(body[23:]))}
	fromLimit, toLimit := wedding.RoomPlacementCapacity, wedding.RoomPlacementCapacity
	if q.FromBag {
		fromLimit = wedding.RoomInventoryCapacity
	}
	if q.ToBag {
		toLimit = wedding.RoomInventoryCapacity
	}
	return q, q.SourceItemID > 0 && int(q.FromSlot) < fromLimit && int(q.ToSlot) < toLimit
}

func (s *Service) handleCurrentWeddingRoomMove(session *gameSession, body []byte) error {
	q, valid := decodeCurrentWeddingRoomMove(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	fail := func(reason string, cause error) error {
		s.logInfo("dnfbridge wedding room move result", "event", "game-wedding-room-move-result", "char_id", actor.character, "source_item", q.SourceItemID, "committed", false, "reason", reason, "error", cause)
		return s.sendGameUpperRawClass(session, 1046, weddingResultACK(-1), 1)
	}
	fields := s.currentWeddingDiagnosticFields(session)
	fields = append(fields, "event", "game-wedding-room-move-request", "body_len", len(body), "decoded", valid)
	if valid {
		fields = append(fields, "from_bag", q.FromBag, "from_slot", q.FromSlot, "source_item", q.SourceItemID, "to_bag", q.ToBag, "to_slot", q.ToSlot, "destination_item", q.ExpectedDestination)
	}
	s.logInfo("dnfbridge wedding room move diagnostic", fields...)
	if !valid || !bound {
		return fail("request_or_binding", nil)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	req, actors, err := s.currentWeddingPartnerRequest(ctx, actor)
	if err != nil {
		return fail("partner", err)
	}
	scope, _ := r.WeddingPairScope([]string{req.Actor.CharacterID, req.Partner.CharacterID})
	editorCurrent := s.wedding.roomEditors[scope] == actor
	sceneCurrent := s.currentWeddingRoomScene(actor, req.Partner.CharacterID)
	if !editorCurrent || !sceneCurrent {
		if s.wedding.roomEditors[scope] == actor {
			delete(s.wedding.roomEditors, scope)
		}
		if !sceneCurrent {
			return fail("scene", nil)
		}
		return fail("editor_not_open", nil)
	}
	revalidate := req.Revalidate
	req.Revalidate = func() bool { return revalidate() && s.currentWeddingRoomScene(actor, req.Partner.CharacterID) }
	release, err := s.reserveCurrentGuildParticipants(ctx, actors)
	if err != nil {
		return fail("asset_reservation", err)
	}
	owner, err := s.currentWeddingOwner()
	if err != nil {
		release()
		return err
	}
	result, err := owner.MoveFurniture(ctx, req, q)
	release()
	if err != nil {
		return fail("commit", err)
	}
	published := errors.Join(s.sendGameUpperRawClass(session, 1046, weddingResultACK(0), 1), s.publishCurrentWeddingPair(result))
	s.logInfo("dnfbridge wedding room move result", "event", "game-wedding-room-move-result", "char_id", actor.character, "source_item", q.SourceItemID, "committed", true, "projection_ok", published == nil)
	return published
}
