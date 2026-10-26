package dnfbridge

import (
	"encoding/binary"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const currentGuildGuardianOpcode uint16 = 1350

func (s *Service) handleCurrentGuildGuardian(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-guardian-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(err error) error {
		code := byte(3) // Matched 1350 callback: invalid access.
		if errors.Is(err, guild.ErrGuardianEssence) {
			code = 22
		}
		if errors.Is(err, guild.ErrNotMember) {
			code = 100
		}
		s.logGameEvent(session, "game-guild-guardian-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildGuardianOpcode, code)
	}
	if !s.options.guildEssenceEnabled {
		return fail(guild.ErrGuardianPolicy)
	}
	policy, err := guild.ParseGuardianPolicy(s.options.guildGuardianPolicy)
	if err != nil {
		return fail(err)
	}
	if request.Sequence == nil || len(request.Body) != 16 {
		return fail(guild.ErrGuardianInteraction)
	}
	action, id := binary.LittleEndian.Uint32(request.Body), binary.LittleEndian.Uint32(request.Body[4:])
	observed, value := binary.LittleEndian.Uint32(request.Body[8:]), binary.LittleEndian.Uint32(request.Body[12:])
	if action > guild.GuardianPurchase || id < 1 || id > 2 || (action >= guild.GuardianSelect && (observed != 0 || value != 0)) {
		return fail(guild.ErrGuardianInteraction)
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || identity.generation == 0 {
		return fail(guild.ErrCharacterNotFound)
	}
	requestID, err := session.guild.purchaseSequence.identity(*request.Sequence)
	if err != nil {
		return fail(err)
	}
	items, err := s.currentPVFItemCatalog()
	if err != nil {
		return fail(err)
	}
	catalog, err := guild.LoadGuardianCatalog(items.source)
	if err != nil {
		return fail(err)
	}
	var growth *guild.GuardianGrowthCatalog
	if action <= guild.GuardianSkillLast {
		growth, err = guild.LoadGuardianGrowthCatalog(items.source)
		if err != nil {
			return fail(err)
		}
	}
	scene, err := loadGuildGuardianScene(items.source)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(guild.ErrRepositoryUnavailable)
	}
	owner := r.GuildOwner{AccountID: s.accountIDForSession(session), CharacterID: strconv.Itoa(int(identity.character))}
	reg, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return fail(err)
	}
	g, _, found := reg.GuildForOwner(owner)
	if !found {
		return fail(guild.ErrNotMember)
	}
	expectedGuild := g.ID
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return nil
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(guild.ErrGuardianInteraction)
	}
	result, err := func() (guild.GuardianReceipt, error) {
		defer session.townMu.Unlock()
		revalidate := func() bool {
			p := session.townPositionSnapshot
			if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || p.CharacterID != identity.character || !p.PositionValid || p.TownID != 8 || int(p.AreaID) != scene.area {
				return false
			}
			if !session.dungeon.mu.TryLock() {
				return false
			}
			inDungeon := session.dungeon.runtime != nil
			session.dungeon.mu.Unlock()
			area, found := s.townArea(int64(p.TownID), int64(p.AreaID))
			return !inDungeon && found && townMapPathLooksLikeGuildHall(area.MapPath)
		}
		return guild.ApplyGuardian(ctx, repos.Guild, guild.GuardianRequest{
			Owner: owner, GuildID: expectedGuild, RequestID: requestID, Action: action, GuardianID: id, Policy: policy, Revalidate: revalidate, Observed: observed, Value: value, Growth: growth,
			CanInteract: func(actor r.CharacterRecord, g *guild.Guild, replay bool) bool {
				p := session.townPositionSnapshot
				if actor.Stats["town_id"] != int64(p.TownID) || actor.Stats["area_id"] != int64(p.AreaID) {
					return false
				}
				// A committed first purchase replaces the initial NPC. A receipt
				// recovery in the same hall reads state without reapplying it.
				return replay || scene.hasInteractionNPC(g, id)
			},
		}, catalog, s.gameplayNow())
	}()
	if err != nil {
		return fail(err)
	}
	if err = s.sendGameUpperSuccess(session, currentGuildGuardianOpcode, nil); err != nil {
		return err
	}
	if err = s.writeCurrentGuildEssenceSnapshot(session); err != nil {
		return err
	}
	s.queueCurrentGuildEssenceRefresh(result.GuildID, identity.character)
	return nil
}
