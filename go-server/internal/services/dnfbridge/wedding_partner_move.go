package dnfbridge

import (
	"context"
	"encoding/binary"
	"strconv"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/wedding"
)

// One confirmation belongs to the exact bound owner and spouse scene. Native
// 1044 has no nonce: its slot/destination must never be treated as one.
type currentWeddingPartnerMovePending struct {
	actor, partner                 boundGameSessionCharacter
	token                          uint32
	channel                        uint16
	town, area                     uint32
	x, y                           int32
	direction                      byte
	sourcePresence, targetPresence uint64
	expires                        time.Time
	committed, projected           bool
}

func decodeCurrentWeddingPartnerMoveCheck(body []byte) (uint32, uint16, bool) {
	if len(body) != 21 {
		return 0, 0, false
	}
	token, slot := binary.LittleEndian.Uint32(body[13:]), binary.LittleEndian.Uint32(body[17:])
	return token, uint16(slot), slot >= 3 && slot <= 288
}

type currentWeddingPartnerMoveConfirm struct {
	slot       uint16
	town, area uint32
	x, y       int32
}

func decodeCurrentWeddingPartnerMoveConfirm(body []byte) (currentWeddingPartnerMoveConfirm, bool) {
	if len(body) != 33 {
		return currentWeddingPartnerMoveConfirm{}, false
	}
	slot := binary.LittleEndian.Uint32(body[13:])
	q := currentWeddingPartnerMoveConfirm{slot: uint16(slot), town: binary.LittleEndian.Uint32(body[17:]), area: binary.LittleEndian.Uint32(body[21:]), x: int32(binary.LittleEndian.Uint32(body[25:])), y: int32(binary.LittleEndian.Uint32(body[29:]))}
	return q, slot >= 3 && slot <= 288 && q.town > 0 && q.town <= 255 && q.area <= 255 && q.x >= 0 && q.x <= 32767 && q.y >= 0 && q.y <= 32767
}

// The u16 is the destination channel, not the source item slot. The following
// u32 is read but unused by the current native handler and remains zero.
func buildCurrentWeddingPartnerMoveACK(code int32, p *currentWeddingPartnerMovePending) []byte {
	b := make([]byte, 23)
	b[0] = 1
	binary.LittleEndian.PutUint32(b[1:], uint32(code))
	if p != nil {
		binary.LittleEndian.PutUint16(b[5:], p.channel)
		binary.LittleEndian.PutUint32(b[11:], p.town)
		binary.LittleEndian.PutUint32(b[15:], p.area)
		binary.LittleEndian.PutUint16(b[19:], uint16(p.x))
		binary.LittleEndian.PutUint16(b[21:], uint16(p.y))
	}
	return b
}

func (s *Service) currentWeddingPartnerMoveScenes(actor, partner boundGameSessionCharacter, rules wedding.PartnerMoveRules) (onlinePlayerInfo, onlinePlayerInfo, bool) {
	if s.onlinePlayers == nil || !s.boundGameSessionCharacterCurrent(actor) || !s.boundGameSessionCharacterCurrent(partner) || isPVPChannel(actor.session.channel) || isPVPChannel(partner.session.channel) {
		return onlinePlayerInfo{}, onlinePlayerInfo{}, false
	}
	a, fa := s.onlinePlayers.PlayerForCharacter(actor.character)
	b, fb := s.onlinePlayers.PlayerForCharacter(partner.character)
	if !fa || !fb || a.Session != actor.session || b.Session != partner.session || a.AccountID != s.accountIDForSession(actor.session) || b.AccountID != s.accountIDForSession(partner.session) || a.presenceGeneration == 0 || b.presenceGeneration == 0 || a.ChannelID <= 0 || a.ChannelID > 65535 || a.ChannelID != b.ChannelID || a.ChannelID != currentTownPresenceChannelID(actor.session) || b.ChannelID != currentTownPresenceChannelID(partner.session) || a.RaidBattleInstance != "" || b.RaidBattleInstance != "" || b.Instance != 0 || b.TownID == 17 || b.PositionX < 0 || b.PositionX > 32767 || b.PositionY < 0 || b.PositionY > 32767 {
		return a, b, false
	}
	for _, identity := range []boundGameSessionCharacter{actor, partner} {
		if !identity.session.dungeon.mu.TryLock() {
			return a, b, false
		}
		inDungeon := identity.session.dungeon.runtime != nil
		identity.session.dungeon.mu.Unlock()
		if inDungeon {
			return a, b, false
		}
	}
	from, fromOK := s.townArea(int64(a.TownID), int64(a.AreaID))
	to, toOK := s.townArea(int64(b.TownID), int64(b.AreaID))
	if !fromOK || !toOK || townMapPathLooksLikeSeria(to.MapPath) || townMapPathLooksLikeWeddingChurch(to.MapPath) || townMapPathLooksLikeCoupleRoom(to.MapPath) || townMapPathLooksLikeGuildHall(to.MapPath) || s.currentDedicatedChannelTown(int64(b.TownID)) {
		return a, b, false
	}
	if townMapPathLooksLikeWeddingChurch(from.MapPath) || townMapPathLooksLikeCoupleRoom(from.MapPath) || townMapPathLooksLikeGuildHall(from.MapPath) || s.currentDedicatedChannelTown(int64(a.TownID)) {
		return a, b, false
	}
	seria := townMapPathLooksLikeSeria(from.MapPath)
	if seria && !rules.Seria || !seria && (!rules.Village || a.Instance != 0) {
		return a, b, false
	}
	for _, identity := range []boundGameSessionCharacter{actor, partner} {
		if !identity.session.townMu.TryLock() {
			return a, b, false
		}
		ready := identity.session.townSceneReadyCharacterID == identity.character
		identity.session.townMu.Unlock()
		if !ready {
			return a, b, false
		}
	}
	return a, b, true
}

func (s *Service) currentWeddingPartnerMoveVoucher(ctx context.Context, owner wedding.Owner, actor boundGameSessionCharacter, slot uint16, rule wedding.PartnerMoveRules) bool {
	bag, found, e := owner.Repositories.Inventory.Load(ctx, strconv.Itoa(int(actor.character)))
	if e != nil || !found || bag.CharacterID != strconv.Itoa(int(actor.character)) {
		return false
	}
	stack, found := bag.Slots[experienceItemMainSlotKey(int16(slot))]
	if !found || stack.ItemID != rule.ItemID || stack.Count <= 0 || stack.Count > rule.StackLimit {
		return false
	}
	if e := r.BindInventoryContainerState(ctx, owner.Repositories.Settings, &bag); e != nil {
		return false
	}
	catalog, e := s.currentPVFItemCatalog()
	if e != nil {
		return false
	}
	definition, e := catalog.ResolveItem(uint32(rule.ItemID))
	if e != nil {
		return false
	}
	return wedding.ValidatePartnerMoveVoucher(&bag, rule, slot, currentPVFItemPlacementPolicy(definition), s.gameplayNow()) == nil
}

func (s *Service) handleCurrentWeddingPartnerMoveCheck(session *gameSession, body []byte) error {
	fail := func() error {
		return s.sendGameUpperRawClass(session, 1043, buildCurrentWeddingPartnerMoveACK(-1, nil), 1)
	}
	token, slot, valid := decodeCurrentWeddingPartnerMoveCheck(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !valid || !bound {
		return fail()
	}
	s.wedding.mu.Lock()
	defer s.wedding.mu.Unlock()
	delete(s.wedding.partnerMoves, actor.character)
	ctx, cancel := context.WithTimeout(context.Background(), currentRepositorySnapshotTimeout)
	defer cancel()
	owner, e := s.currentWeddingOwner()
	if e != nil {
		return fail()
	}
	rules, e := s.currentWeddingExtensionRules()
	if e != nil {
		return fail()
	}
	c, e := s.currentWeddingCharacter(ctx, actor)
	if e != nil {
		return fail()
	}
	state, e := wedding.Read(c)
	if e != nil || state.Phase != wedding.Married || currentWeddingRelationToken(state) != token {
		return fail()
	}
	last := c.Stats["wedding_v1_partner_move_at_ms"]
	if last < 0 || last > s.gameplayNow().UnixMilli() || last > 0 && s.gameplayNow().UnixMilli()-last < rules.PartnerMove.CooldownMilliseconds {
		return fail()
	}
	partner, online := s.onlineGameSessionCharacterSnapshot(uint16(state.PartnerID))
	if !online {
		return fail()
	}
	b, e := s.currentWeddingCharacter(ctx, partner)
	if e != nil {
		return fail()
	}
	if _, _, e = wedding.ValidatePair(c, b); e != nil {
		return fail()
	}
	from, to, valid := s.currentWeddingPartnerMoveScenes(actor, partner, rules.PartnerMove)
	if !valid || !s.currentWeddingPartnerMoveVoucher(ctx, owner, actor, slot, rules.PartnerMove) {
		return fail()
	}
	if e = s.currentWeddingPartnerMoveAdmission(ctx, owner.Repositories, c, wedding.PartnerMoveLocation{Town: int64(to.TownID), Area: int64(to.AreaID), X: int64(to.PositionX), Y: int64(to.PositionY), Direction: int64(to.Direction)}); e != nil {
		return fail()
	}
	pending := &currentWeddingPartnerMovePending{actor: actor, partner: partner, token: token, channel: uint16(to.ChannelID), town: uint32(to.TownID), area: uint32(to.AreaID), x: int32(to.PositionX), y: int32(to.PositionY), direction: to.Direction, sourcePresence: from.presenceGeneration, targetPresence: to.presenceGeneration, expires: s.gameplayNow().Add(30 * time.Second)}
	if s.wedding.partnerMoves == nil {
		s.wedding.partnerMoves = map[uint16]*currentWeddingPartnerMovePending{}
	}
	s.wedding.partnerMoves[actor.character] = pending
	if e = s.sendGameUpperRawClass(session, 1043, buildCurrentWeddingPartnerMoveACK(0, pending), 1); e != nil {
		delete(s.wedding.partnerMoves, actor.character)
		return e
	}
	return nil
}

func (s *Service) handleCurrentWeddingPartnerMove(session *gameSession, body []byte) error {
	fail := func() error { return s.sendGameUpperRawClass(session, 1044, weddingResultACK(-1), 1) }
	q, valid := decodeCurrentWeddingPartnerMoveConfirm(body)
	actor, bound := s.boundGameSessionCharacterSnapshot(session)
	if !valid || !bound {
		return fail()
	}
	var pending *currentWeddingPartnerMovePending
	project := false
	e := func() error {
		s.wedding.mu.Lock()
		defer s.wedding.mu.Unlock()
		pending = s.wedding.partnerMoves[actor.character]
		if pending == nil || pending.actor != actor || !s.gameplayNow().Before(pending.expires) || pending.town != q.town || pending.area != q.area || pending.x != q.x || pending.y != q.y {
			return wedding.ErrState
		}
		if pending.committed {
			if !pending.projected {
				pending.projected = true
				project = true
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
		defer cancel()
		owner, e := s.currentWeddingOwner()
		if e != nil {
			return e
		}
		rules, e := s.currentWeddingExtensionRules()
		if e != nil {
			return e
		}
		c, e := s.currentWeddingCharacter(ctx, actor)
		if e != nil {
			return e
		}
		state, e := wedding.Read(c)
		if e != nil || state.Phase != wedding.Married || currentWeddingRelationToken(state) != pending.token || uint16(state.PartnerID) != pending.partner.character {
			return wedding.ErrState
		}
		from, to, ok := s.currentWeddingPartnerMoveScenes(actor, pending.partner, rules.PartnerMove)
		if !ok || from.presenceGeneration != pending.sourcePresence || to.presenceGeneration != pending.targetPresence || uint32(to.TownID) != pending.town || uint32(to.AreaID) != pending.area || int32(to.PositionX) != pending.x || int32(to.PositionY) != pending.y {
			return wedding.ErrState
		}
		partner, e := s.currentWeddingCharacter(ctx, pending.partner)
		if e != nil {
			return e
		}
		if _, _, e = wedding.ValidatePair(c, partner); e != nil {
			return e
		}
		catalog, e := s.currentPVFItemCatalog()
		if e != nil {
			return e
		}
		definition, e := catalog.ResolveItem(uint32(rules.PartnerMove.ItemID))
		if e != nil {
			return e
		}
		release, e := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{actor, pending.partner})
		if e != nil {
			return e
		}
		defer release()
		req := wedding.PairRequest{Actor: wedding.Actor{AccountID: c.AccountID, CharacterID: c.CharacterID}, Partner: wedding.Actor{AccountID: partner.AccountID, CharacterID: partner.CharacterID}, Now: s.gameplayNow(), Revalidate: func() bool {
			a, b, ok := s.currentWeddingPartnerMoveScenes(actor, pending.partner, rules.PartnerMove)
			return ok && a.presenceGeneration == pending.sourcePresence && b.presenceGeneration == pending.targetPresence && uint32(b.TownID) == pending.town && uint32(b.AreaID) == pending.area && int32(b.PositionX) == pending.x && int32(b.PositionY) == pending.y
		}}
		_, e = owner.MoveToPartner(ctx, req, rules.PartnerMove, wedding.PartnerMoveCommand{Slot: q.slot, FromTown: int64(from.TownID), FromArea: int64(from.AreaID), Destination: wedding.PartnerMoveLocation{Town: int64(pending.town), Area: int64(pending.area), X: int64(pending.x), Y: int64(pending.y), Direction: int64(pending.direction)}, Slots: currentPVFItemPlacementPolicy(definition), Admit: func(g r.Group, c r.CharacterRecord, destination wedding.PartnerMoveLocation) error {
			return s.currentWeddingPartnerMoveAdmission(ctx, g, c, destination)
		}})
		if e == nil {
			pending.committed = true
			pending.projected = true
			project = true
		}
		return e
	}()
	if e != nil {
		s.logGameEvent(session, "game-wedding-partner-move-rejected", "error", e)
		return fail()
	}
	if !project || !s.boundGameSessionCharacterCurrent(actor) {
		return s.sendGameUpperRawClass(session, 1044, weddingResultACK(0), 1)
	}
	if e = s.sendSelectedIncrementalItemSlotRefreshes(session, "wedding_partner_move_committed", []alignedcmd.ItemSlotRefresh{{ListType: r.MainInventoryListType, SlotIndex: int16(q.slot)}}); e != nil {
		return e
	}
	// This is a committed trip, not another map authorization. The existing
	// town owner refuses a newer persisted location and owns native op24 order.
	// Never replay a partially written scene on this connection after failure.
	location := guild.TeleportLocation{Town: int64(pending.town), Area: int64(pending.area), X: int64(pending.x), Y: int64(pending.y), Direction: int64(pending.direction)}
	move := buildCurrentSoloTeleportMoveBody(currentSoloTeleportRequest{TownID: byte(pending.town), AreaID: byte(pending.area), PositionX: int16(pending.x), PositionY: int16(pending.y), Direction: pending.direction}, uint16(pending.town))
	if e = s.handleTownSetUserAreaWithCommit(session, move, false, &location); e != nil {
		return e
	}
	return s.sendGameUpperRawClass(session, 1044, weddingResultACK(0), 1)
}

func (s *Service) currentWeddingPartnerMoveAdmission(ctx context.Context, g r.Group, c r.CharacterRecord, destination wedding.PartnerMoveLocation) error {
	area, found := s.townArea(destination.Town, destination.Area)
	if !found || !destination.Valid() || area.MinLevel > int64(c.Level) || c.Stats["area_state"] < 0 || c.Stats["area_state"] > 255 {
		return wedding.ErrState
	}
	if _, found := c.Stats["area_state"]; !found {
		return wedding.ErrState
	}
	allowed, _, e := s.townAreaQuestRequirement(ctx, g, c.CharacterID, area.NeedQuests)
	if e != nil {
		return e
	}
	if !allowed {
		return wedding.ErrState
	}
	return nil
}
