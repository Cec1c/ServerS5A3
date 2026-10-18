package dnfbridge

import (
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/inventory"
	r "longheng.io/server/internal/modules/dnf/repository"
)

func (s *Service) handleCurrentGuildCargoTransfer(session *gameSession, request gameplayRequest) error {
	op := request.Opcode
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-cargo-class-rejected", "command_class_mismatch") {
		return nil
	}
	session.guild.mu.Lock()
	session.guild.cargoPending++
	session.guild.mu.Unlock()
	s.guildCargoMu.Lock()
	defer s.guildCargoMu.Unlock()
	needsRefresh := true
	defer func() {
		session.guild.mu.Lock()
		session.guild.cargoPending--
		id := session.guild.cargoLoadedGuild
		session.guild.mu.Unlock()
		if needsRefresh && id != 0 {
			// All actor/scene guards are released before this deferred refresh.
			if err := s.handleCurrentGuildCargoQueryLocked(session, currentGuildCargoOpcode, nil, id); err != nil {
				s.logGameEvent(session, "game-guild-cargo-refresh-failed", "error", err)
			}
		}
	}()
	if op == 249 {
		// V110 callback 0x1fbd690 clears the cargo waiting stack on failure.
		// Its protected success helper's swap semantics remain unproven.
		return s.sendGameUpperFailure(session, op, 8)
	}
	fail := func(err error) error {
		code := byte(8)
		switch {
		case errors.Is(err, guild.ErrNotMember):
			code = 100
		case errors.Is(err, guild.ErrPermissionDenied):
			code = 36
		case errors.Is(err, guild.ErrCargoUninitialized):
			code = 196
		case errors.Is(err, inventory.ErrGuildCargoInventoryFull):
			code = 204
		case errors.Is(err, inventory.ErrGuildCargoRequest), errors.Is(err, inventory.ErrGuildCargoStale), errors.Is(err, inventory.ErrItemLocked):
			code = 198
		}
		s.logGameEvent(session, "game-guild-cargo-transfer-rejected", "opcode", op, "reason", err)
		return s.sendGameUpperFailure(session, op, code)
	}
	var deposit inventory.GuildCargoDepositRequest
	var withdraw inventory.GuildCargoWithdrawalRequest
	var valid bool
	switch op {
	case 247:
		deposit, valid = parseCurrentGuildCargoDepositRequest(request.Body)
	case 248:
		withdraw, valid = parseCurrentGuildCargoWithdrawalRequest(request.Body)
	}
	if !valid || request.Sequence == nil {
		return fail(inventory.ErrGuildCargoRequest)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(session)
	if !bound || identity.generation == 0 {
		return fail(guild.ErrNotMember)
	}
	requestID, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return fail(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	if !session.townMu.TryLock() {
		return fail(inventory.ErrGuildCargoRequest)
	}
	var result inventory.GuildCargoResult
	var ack []byte
	err = func() error {
		defer session.townMu.Unlock()
		revalidate := func() bool {
			if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || !session.townPositionSnapshot.PositionValid {
				return false
			}
			if !session.dungeon.mu.TryLock() {
				return false
			}
			inDungeon := session.dungeon.runtime != nil
			session.dungeon.mu.Unlock()
			return !inDungeon
		}
		if !revalidate() {
			return inventory.ErrGuildCargoRequest
		}
		area, found := s.townArea(int64(session.townPositionSnapshot.TownID), int64(session.townPositionSnapshot.AreaID))
		if !found || !townMapPathLooksLikeGuildHall(area.MapPath) {
			return inventory.ErrGuildCargoRequest
		}
		items, err := s.currentPVFItemCatalog()
		if err != nil {
			return err
		}
		limit, err := currentGuildCargoSceneLimit(items.source, area.MapPath)
		if err != nil {
			return err
		}
		repos, ok := s.repositoryGroup()
		if !ok {
			return guild.ErrRepositoryUnavailable
		}
		owner := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
		view, err := guild.ReadCargo(ctx, repos.Guild, guild.CargoQuery{Owner: owner, Revalidate: revalidate})
		if err != nil {
			return err
		}
		session.guild.mu.Lock()
		loaded := session.guild.cargoLoadedGuild == view.GuildID && session.guild.cargoLoadedGeneration == identity.generation
		session.guild.mu.Unlock()
		if !loaded {
			return guild.ErrCargoUninitialized
		}
		itemOwner, err := inventory.NewOwner(repos)
		if err != nil {
			return err
		}
		rules := currentGuildCargoRules(items, limit, s.gameplayNow())
		if op == 247 {
			deposit.Owner, deposit.GuildID, deposit.RequestID, deposit.Revalidate = owner, view.GuildID, requestID, revalidate
			result, err = itemOwner.DepositGuildCargo(ctx, deposit, rules, s.gameplayNow())
			if err == nil {
				ack, err = buildCurrentGuildCargoDepositSuccessBody(deposit, result)
			}
		} else {
			withdraw.Owner, withdraw.GuildID, withdraw.RequestID, withdraw.Revalidate = owner, view.GuildID, requestID, revalidate
			result, err = itemOwner.WithdrawGuildCargo(ctx, withdraw, rules, s.gameplayNow())
			if err == nil {
				ack, err = buildCurrentGuildCargoWithdrawalSuccessBody(withdraw, result)
			}
		}
		return err
	}()
	if err != nil {
		return fail(err)
	}
	if result.Replayed {
		// A duplicate success would move local objects twice. Error zero clears
		// the waiting stacks, then authoritative snapshots repair both containers.
		code := byte(0) // 247 explicitly suppresses its dialog for zero.
		if op == 248 {
			code = 8
		} // 248 has no silent-zero branch.
		err = s.sendGameUpperFailure(session, op, code)
	} else {
		err = s.sendGameUpperSuccess(session, op, ack)
	}
	if err != nil {
		return err
	}
	refresh := alignedcmd.ItemSlotRefresh{ListType: result.Destination.List, SlotIndex: int16(result.Destination.Slot)}
	if op == 247 {
		refresh = alignedcmd.ItemSlotRefresh{ListType: deposit.SourceList, SlotIndex: int16(deposit.SourceSlot)}
	}
	if err := s.sendSelectedIncrementalItemSlotRefreshes(session, "guild_cargo_transfer", []alignedcmd.ItemSlotRefresh{refresh}); err != nil {
		return err
	}
	release()
	release = nil
	// The existing projection rechecks each current guild owner and hall/NPC.
	s.refreshCurrentGuildCargoLocked(ctx, result.GuildID, identity)
	needsRefresh = false
	return nil
}
