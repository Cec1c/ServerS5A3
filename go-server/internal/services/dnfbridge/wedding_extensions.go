package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"

	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

type currentWeddingExtensionRequestState struct {
	identity    boundGameSessionCharacter
	sequence    uint64
	opcode      uint16
	fingerprint string
	semantic    int32
	itemID      int64
	slot        int16
}

func (s *Service) currentWeddingExtensionRules() (*wedding.ExtensionRules, error) {
	s.wedding.catalogMu.Lock()
	defer s.wedding.catalogMu.Unlock()
	if s.wedding.extensionRules != nil || s.wedding.extensionLoadErr != nil {
		return s.wedding.extensionRules, s.wedding.extensionLoadErr
	}
	s.initialEquipmentMu.Lock()
	archive, e := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if e == nil {
		s.wedding.extensionRules, e = wedding.LoadExtensionRules(archive)
	}
	s.wedding.extensionLoadErr = e
	return s.wedding.extensionRules, e
}

// Native C0/1012 and the tail of C0/1008 use the same packed 13 bytes.
func buildCurrentWeddingTree(tree wedding.TreeState) []byte {
	b := make([]byte, 13)
	binary.LittleEndian.PutUint32(b, uint32(tree.Index))
	binary.LittleEndian.PutUint32(b[4:], uint32(tree.Expires))
	binary.LittleEndian.PutUint32(b[8:], uint32(tree.Growth))
	b[12] = byte(tree.WaterCount)
	return b
}

// NOTI1011 is an authoritative slot update, including explicit empty rows.
// Native1009 replaces its ordinary cargo inventory but does not erase the
// marriage model's placed-slot table; only sending nonempty rows leaves ghosts.
func buildCurrentWeddingRoomRefresh(room wedding.Room, rules *wedding.Rules) []byte {
	positions := map[int]bool{}
	if rules != nil {
		for _, furniture := range rules.Furniture {
			for position := range furniture.Positions {
				positions[int(position)] = true
			}
		}
	}
	ordered := make([]int, 0, len(positions))
	for position := range positions {
		ordered = append(ordered, position)
	}
	sort.Ints(ordered)
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(ordered)+wedding.RoomInventoryCapacity))
	appendRow := func(kind byte, id int64, slot uint16, count int64) {
		b = append(b, kind)
		b = binary.LittleEndian.AppendUint32(b, uint32(id))
		b = binary.LittleEndian.AppendUint16(b, slot)
		b = binary.LittleEndian.AppendUint32(b, uint32(count))
	}
	for _, slot := range ordered {
		stack := room.Placed[uint16(slot)]
		appendRow(0, stack.ItemID, uint16(slot), stack.Count)
	}
	for slot := uint16(0); slot < wedding.RoomInventoryCapacity; slot++ {
		stack := room.Bag[slot]
		appendRow(1, stack.ItemID, slot, stack.Count)
	}
	return b
}
func buildCurrentWeddingCapsule(state wedding.CapsuleState, spousePending bool) []byte {
	b := make([]byte, 7)
	binary.LittleEndian.PutUint32(b, uint32(state.Progress))
	b[4], b[5] = byte(state.Own), byte(state.Taken)
	if spousePending {
		b[6] = 1
	}
	return b
}
func currentWeddingExtensionACK(op uint16, result int32, state wedding.CapsuleState) []byte {
	b := weddingResultACK(result)
	if result != 0 {
		return b
	}
	switch op {
	case 1052:
		b = binary.LittleEndian.AppendUint32(b, uint32(state.Progress))
		b = append(b, byte(state.Own))
	case 1053:
		b = append(b, byte(state.Own))
	case 1054:
		b = append(b, byte(state.Taken))
	}
	return b
}

// Exact native tree handlers are silent for -1. Capsule handlers use -1
// for relationship corruption, not normal stock/energy/capacity limits.
// Each command has its own error meanings; never copy codes between them.
func currentWeddingExtensionFailureCode(op uint16, err error) int32 {
	switch op {
	case 1048:
		if errors.Is(err, wedding.ErrTreeOccupied) {
			return -4
		}
	case 1049:
		switch {
		case errors.Is(err, wedding.ErrTreeExpired):
			return -11
		case errors.Is(err, wedding.ErrTreeMissing):
			return -4
		case errors.Is(err, wedding.ErrTreeMature):
			return -9
		case errors.Is(err, wedding.ErrTreeWaterQuota):
			return -6
		}
	case 1050:
		switch {
		case errors.Is(err, wedding.ErrTreeMissing):
			return -4
		case errors.Is(err, wedding.ErrTreeNotMature):
			return -6
		case errors.Is(err, wedding.ErrTreeExpired):
			return -7
		}
	case 1051:
		if errors.Is(err, wedding.ErrTreeMissing) {
			return -4
		}
	case 1052:
		switch {
		case errors.Is(err, wedding.ErrCapsuleEnergy):
			return -4
		case errors.Is(err, wedding.ErrCapsuleOwnLimit):
			return -5
		}
	case 1053:
		switch {
		case errors.Is(err, wedding.ErrCapsuleEmpty):
			return -4
		case errors.Is(err, wedding.ErrCapsulePartnerLimit):
			return -6
		}
	case 1054:
		if errors.Is(err, errDungeonPickupInventoryFull) {
			return -6
		}
	}
	return -1
}

func decodeCurrentWeddingTreeCommand(op uint16, body []byte) (wedding.TreeCommand, bool) {
	c := wedding.TreeCommand{Opcode: op}
	switch op {
	case 1047:
		if len(body) != 23 {
			return c, false
		}
		c.Slot = binary.LittleEndian.Uint16(body[13:])
		c.ItemID = int64(binary.LittleEndian.Uint32(body[15:]))
		c.Count = int64(binary.LittleEndian.Uint32(body[19:]))
		return c, c.Slot < wedding.RoomInventoryCapacity && c.ItemID > 0 && c.Count == 1
	case 1048, 1049:
		if len(body) != 19 {
			return c, false
		}
		c.Slot = binary.LittleEndian.Uint16(body[13:])
		c.ItemID = int64(binary.LittleEndian.Uint32(body[15:]))
		return c, c.Slot >= 3 && c.Slot <= 288 && c.ItemID > 0
	case 1050, 1051:
		return c, len(body) == 0
	default:
		return c, false
	}
}

func currentWeddingExtensionHandler(s *Service, session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-wedding-extension-class-rejected", "command_class_mismatch") {
		return nil
	}
	return s.handleCurrentWeddingExtension(session, request)
}

func (s *Service) handleCurrentWeddingExtension(session *gameSession, request gameplayRequest) error {
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	fail := func(code int32) error {
		if request.Opcode == 1049 {
			s.logInfo("dnfbridge wedding tree watering result", "event", "game-wedding-tree-water-result", "char_id", actor.character, "body_len", len(request.Body), "accepted", false, "result", code)
		} else {
			s.logInfo("dnfbridge wedding extension result", "event", "game-wedding-extension-result", "char_id", actor.character, "opcode", request.Opcode, "accepted", false, "result", code)
		}
		return s.sendGameUpperRawClass(session, request.Opcode, currentWeddingExtensionACK(request.Opcode, code, wedding.CapsuleState{}), 1)
	}
	op := request.Opcode
	if !bound || request.Sequence == nil || op < 1047 || op > 1054 {
		return fail(-1)
	}
	var tree wedding.TreeCommand
	if op <= 1051 {
		var valid bool
		tree, valid = decodeCurrentWeddingTreeCommand(op, request.Body)
		if !valid {
			return fail(-1)
		}
	} else if len(request.Body) != 0 {
		return fail(-1)
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	fingerprint := fmt.Sprintf("%d:%x", op, request.Body)
	if s.wedding.extensionRequests == nil {
		s.wedding.extensionRequests = map[uint16]*currentWeddingExtensionRequestState{}
	}
	if previous := s.wedding.extensionRequests[actor.character]; previous != nil && previous.identity == actor {
		if *request.Sequence < previous.sequence || *request.Sequence == previous.sequence && (previous.opcode != op || previous.fingerprint != fingerprint) {
			return fail(-1)
		}
		if *request.Sequence == previous.sequence {
			if previous.semantic != 0 {
				return fail(previous.semantic)
			}
			return s.finishCurrentWeddingExtensionLocked(actor, op, nil, nil)
		}
	}
	receipt := &currentWeddingExtensionRequestState{identity: actor, sequence: *request.Sequence, opcode: op, fingerprint: fingerprint, semantic: -1}
	s.wedding.extensionRequests[actor.character] = receipt
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	req, actors, e := s.currentWeddingPartnerRequest(ctx, actor)
	if e != nil {
		return fail(-1)
	}
	if op <= 1051 && !s.currentWeddingRoomScene(actor, req.Partner.CharacterID) {
		return fail(-1)
	}
	if op == 1047 {
		scope, _ := r.WeddingPairScope([]string{req.Actor.CharacterID, req.Partner.CharacterID})
		if s.wedding.roomEditors[scope] != actor {
			return fail(-1)
		}
	}
	if op == 1054 && (s.validateUpgradeNPCScene(actor, 20011) != nil || !s.currentWeddingRoomScene(actor, req.Partner.CharacterID)) {
		return fail(-1)
	}
	previousValidate := req.Revalidate
	req.Revalidate = func() bool {
		if !previousValidate() || session.loginRevoked.Load() {
			return false
		}
		if op <= 1051 {
			return s.currentWeddingRoomScene(actor, req.Partner.CharacterID)
		}
		return op != 1054 || s.validateUpgradeNPCScene(actor, 20011) == nil && s.currentWeddingRoomScene(actor, req.Partner.CharacterID)
	}
	release, e := s.reserveCurrentGuildParticipants(ctx, actors)
	if e != nil {
		return fail(-1)
	}
	owner, e := s.currentWeddingOwner()
	if e != nil {
		release()
		return fail(-1)
	}
	rules, e := s.currentWeddingExtensionRules()
	if e != nil {
		release()
		return fail(-1)
	}
	nonce, e := session.guild.purchaseSequence.identity(0)
	if e != nil {
		release()
		return fail(-1)
	}
	stamp := wedding.ExtensionReceipt{Session: nonce, Sequence: *request.Sequence, Opcode: op, Fingerprint: fingerprint}
	var result wedding.PairResult
	var reward *wedding.TreeReward
	var mails map[string]string
	if op <= 1051 {
		tree.Day, tree.Receipt = currentWeddingDay(req.Now), stamp
		if op == 1048 || op == 1049 {
			catalog, loadErr := s.currentPVFItemCatalog()
			if loadErr != nil {
				release()
				return fail(-1)
			}
			definition, loadErr := catalog.ResolveItem(uint32(tree.ItemID))
			if loadErr != nil || definition.Kind != dungeonDropItemStackable {
				release()
				return fail(-1)
			}
			tree.Slots = currentPVFItemPlacementPolicy(definition)
		}
		if op == 1050 {
			tree.Deliver = s.currentWeddingTreeDelivery(ctx, req.Now)
		}
		committed, err := owner.MutateTree(ctx, req, rules, tree)
		e = err
		result = committed.PairResult
		if e == nil && !committed.Replayed && committed.Reward.ItemID != 0 {
			r := committed.Reward
			reward = &r
			mails = committed.MailIDs
		}
	} else {
		command := wedding.CapsuleCommand{Opcode: op, Receipt: stamp}
		if op == 1054 {
			command.Grant = s.currentWeddingCapsuleGrant(ctx, owner, rules.Capsule.ItemID)
		}
		committed, err := owner.MutateCapsule(ctx, req, rules.Capsule, command)
		e = err
		result = committed.PairResult
	}
	release()
	if e != nil {
		s.logGameEvent(session, "game-wedding-extension-rejected", "opcode", op, "error", e)
		receipt.semantic = currentWeddingExtensionFailureCode(op, e)
		return fail(receipt.semantic)
	}
	receipt.semantic = 0
	if op == 1049 && result.Room != nil {
		s.logInfo("dnfbridge wedding tree watering result", "event", "game-wedding-tree-water-result", "char_id", actor.character, "accepted", true, "result", 0, "growth", result.Room.Tree.Growth, "water_count", result.Room.Tree.WaterCount)
	} else {
		s.logInfo("dnfbridge wedding extension result", "event", "game-wedding-extension-result", "char_id", actor.character, "opcode", op, "accepted", true, "result", 0)
	}
	// Commit precedes all socket publication. A failed socket cannot reverse
	// the debit/mail transaction or prevent the other spouse's refresh.
	return s.finishCurrentWeddingExtensionLocked(actor, op, &result, &currentWeddingTreePublication{reward: reward, mails: mails})
}

type currentWeddingTreePublication struct {
	reward *wedding.TreeReward
	mails  map[string]string
}

func (s *Service) finishCurrentWeddingExtensionLocked(actor boundGameSessionCharacter, op uint16, result *wedding.PairResult, harvest *currentWeddingTreePublication) error {
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	rules, e := s.currentWeddingExtensionRules()
	if e != nil {
		return e
	}
	if result == nil {
		req, _, e := s.currentWeddingPartnerRequest(ctx, actor)
		if e != nil {
			return e
		}
		owner, e := s.currentWeddingOwner()
		if e != nil {
			return e
		}
		a, fa, e := owner.Repositories.Character.Load(ctx, req.Actor.CharacterID)
		if e != nil {
			return e
		}
		b, fb, e := owner.Repositories.Character.Load(ctx, req.Partner.CharacterID)
		if e != nil || !fa || !fb {
			return errors.Join(e, wedding.ErrState)
		}
		room, e := owner.ReadRoom(ctx, a, b)
		if e != nil {
			return e
		}
		result = &wedding.PairResult{Actor: a, Partner: b, Room: &room}
	}
	state, e := wedding.ReadCapsule(result.Actor, rules.Capsule)
	if e != nil {
		return e
	}
	var published error
	published = errors.Join(published, s.sendGameUpperRawClass(actor.session, op, currentWeddingExtensionACK(op, 0, state), 1), s.publishCurrentWeddingPair(*result))
	for _, character := range []r.CharacterRecord{result.Actor, result.Partner} {
		id, e := wedding.NumericID(character.CharacterID)
		if e != nil {
			published = errors.Join(published, e)
			continue
		}
		peer, online := s.onlineGameSessionCharacterSnapshot(uint16(id))
		if !online || s.accountIDForSession(peer.session) != character.AccountID {
			continue
		}
		if op <= 1051 && result.Room != nil {
			published = errors.Join(published, s.sendGameUpperRawClass(peer.session, 1012, buildCurrentWeddingTree(result.Room.Tree), 0))
		}
		if op >= 1052 {
			published = errors.Join(published, s.sendCurrentWeddingCapsuleState(peer.session, character))
		}
		if harvest != nil && harvest.reward != nil && harvest.mails[character.CharacterID] != "" {
			body := buildCurrentWeddingTreeHarvestReward(*harvest.reward)
			published = errors.Join(published, s.sendGameUpperRawClass(peer.session, 1014, body, 0), s.sendMailboxAlarmForSession(peer.session, uint16(id), "wedding_tree_harvest"))
		}
	}
	return published
}

// Initial selection and dungeon settlement may hold scene/asset locks. These
// readers deliberately never acquire wedding.mu; they project fresh owned rows.
func (s *Service) sendCurrentWeddingCapsuleState(session *gameSession, expected r.CharacterRecord) error {
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || expected.CharacterID != strconv.Itoa(int(actor.character)) || expected.AccountID != s.accountIDForSession(session) {
		return wedding.ErrOwnership
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	c, e := s.currentWeddingCharacter(ctx, actor)
	if e != nil {
		return e
	}
	state, e := wedding.Read(c)
	if e != nil {
		return e
	}
	if state.Phase != wedding.Married {
		return s.sendGameUpperRawClass(session, 1015, make([]byte, 7), 0)
	}
	rules, e := s.currentWeddingExtensionRules()
	if e != nil {
		return e
	}
	own, e := wedding.ReadCapsule(c, rules.Capsule)
	if e != nil {
		return e
	}
	repos, _ := s.repositoryGroup()
	partner, found, e := repos.Character.Load(ctx, strconv.FormatInt(state.PartnerID, 10))
	if e != nil {
		return e
	}
	if !found {
		return wedding.ErrState
	}
	if _, _, e = wedding.ValidatePair(c, partner); e != nil {
		return e
	}
	other, e := wedding.ReadCapsule(partner, rules.Capsule)
	if e != nil {
		return e
	}
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	return s.sendGameUpperRawClass(session, 1015, buildCurrentWeddingCapsule(own, other.Taken > 0), 0)
}
func (s *Service) sendCurrentWeddingCapsuleGauge(session *gameSession, expected r.CharacterRecord) error {
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || expected.CharacterID != strconv.Itoa(int(actor.character)) || expected.AccountID != s.accountIDForSession(session) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	c, e := s.currentWeddingCharacter(ctx, actor)
	if e != nil {
		return e
	}
	rules, e := s.currentWeddingExtensionRules()
	if e != nil {
		return e
	}
	state, e := wedding.ReadCapsule(c, rules.Capsule)
	if e != nil {
		return e
	}
	if !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	body := binary.LittleEndian.AppendUint32(nil, uint32(state.Progress))
	return s.sendGameUpperRawClass(session, 1016, body, 0)
}
