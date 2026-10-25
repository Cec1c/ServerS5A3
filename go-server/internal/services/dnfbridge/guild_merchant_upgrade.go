package dnfbridge

import (
	"encoding/binary"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

const currentGuildMerchantUpgradeOpcode uint16 = 840

// The native sender copies 25 bytes: an unused 13-byte prefix followed by
// base NPC, current level and target level. Existing clients leave the prefix
// uninitialized; it is neither identity nor part of the durable request key.
func (s *Service) handleCurrentGuildMerchantUpgrade(session *gameSession, request gameplayRequest) error {
	if !requireDefaultGameplayClassification(s, session, request, "game-guild-merchant-class-rejected", "command_class_mismatch") {
		return nil
	}
	fail := func(err error) error {
		code := byte(3)
		if errors.Is(err, guild.ErrMerchantEssence) {
			code = 22
		}
		s.logGameEvent(session, "game-guild-merchant-upgrade-rejected", "error", err)
		return s.sendGameUpperFailure(session, currentGuildMerchantUpgradeOpcode, code)
	}
	if !s.options.guildEssenceEnabled || request.Sequence == nil || len(request.Body) != 25 {
		return fail(guild.ErrMerchantInteraction)
	}
	npc := binary.LittleEndian.Uint32(request.Body[13:])
	if npc != 389 && npc != 390 {
		return fail(guild.ErrMerchantNPC)
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
	catalog, err := guild.LoadMerchantUpgradeCatalog(items.source)
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
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return nil
	}
	defer release()
	if !session.townMu.TryLock() {
		return fail(guild.ErrMerchantInteraction)
	}
	result, err := func() (guild.MerchantUpgradeReceipt, error) {
		defer session.townMu.Unlock()
		p := session.townPositionSnapshot
		area, found := s.townArea(int64(p.TownID), int64(p.AreaID))
		if !found || !townMapPathLooksLikeGuildHall(area.MapPath) {
			return guild.MerchantUpgradeReceipt{}, guild.ErrMerchantInteraction
		}
		_, err := loadGuildMerchantNPC(items.source, area.MapPath, npc)
		if err != nil {
			return guild.MerchantUpgradeReceipt{}, err
		}
		return guild.ApplyMerchantUpgrade(ctx, repos.Guild, guild.MerchantUpgradeRequest{
			Owner: owner, GuildID: g.ID, RequestID: requestID, NPCID: npc,
			CurrentLevel: binary.LittleEndian.Uint32(request.Body[17:]), TargetLevel: binary.LittleEndian.Uint32(request.Body[21:]),
			Revalidate: func() bool {
				current := session.townPositionSnapshot
				if !s.boundGameSessionCharacterCurrent(identity) || session.townSceneReadyCharacterID != identity.character || current != p || p.CharacterID != identity.character || p.TownID != 8 || !p.PositionValid {
					return false
				}
				if !session.dungeon.mu.TryLock() {
					return false
				}
				inDungeon := session.dungeon.runtime != nil
				session.dungeon.mu.Unlock()
				return !inDungeon
			},
			CanInteract: func(actor r.CharacterRecord, _ *guild.Guild, _ bool) bool {
				// Mouse-opened windows do not use the keyboard nearby-NPC range.
				return actor.Stats["town_id"] == int64(p.TownID) && actor.Stats["area_id"] == int64(p.AreaID)
			},
		}, catalog, s.gameplayNow())
	}()
	if err != nil {
		return fail(err)
	}
	if err := s.sendGameUpperSuccess(session, currentGuildMerchantUpgradeOpcode, nil); err != nil {
		return err
	}
	// Recover the latest committed level even when this is an older receipt.
	current, err := guild.Load(ctx, repos.Settings)
	if err != nil {
		return err
	}
	currentGuild, _, found := current.GuildForOwner(owner)
	if !found || currentGuild.ID != result.GuildID {
		return guild.ErrNotMember
	}
	level, err := guild.MerchantLevel(currentGuild, result.MerchantIndex)
	if err != nil {
		return err
	}
	body := make([]byte, 5)
	body[0] = result.MerchantIndex
	binary.LittleEndian.PutUint32(body[1:], uint32(level))
	if err = s.sendGameUpperRawClass(session, 778, body, 0); err != nil {
		return err
	}
	if err = s.writeCurrentGuildEssenceSnapshot(session); err != nil {
		return err
	}
	s.queueCurrentGuildEssenceRefresh(result.GuildID, identity.character)
	return nil
}

func loadGuildMerchantNPC(source pvf.Source, mapPath string, id uint32) (guildGuardianNPC, error) {
	bad := guild.ErrMerchantInteraction
	if (id != 389 && id != 390) || !townMapPathLooksLikeGuildHall(mapPath) {
		return guildGuardianNPC{}, bad
	}
	read := func(root, name string) (*pvf.Document, error) {
		path, text, err := readDungeonDropText(source, root, name)
		if err != nil {
			return nil, err
		}
		return pvf.Parse(path, text)
	}
	list, err := read("npc", "npc.lst")
	if err != nil {
		return guildGuardianNPC{}, err
	}
	path := ""
	for _, row := range pvf.ParseList(list) {
		if row.ID == int64(id) {
			if path != "" {
				return guildGuardianNPC{}, bad
			}
			path = row.Path
		}
	}
	if path == "" {
		return guildGuardianNPC{}, bad
	}
	npc, err := read("npc", path)
	if err != nil {
		return guildGuardianNPC{}, err
	}
	kind := "defence"
	if id == 389 {
		kind = "mercenary"
	}
	typeTokens, found := npc.Section("guild agit shop npc")
	if !found || len(typeTokens) != 1 || typeTokens[0].Value != kind {
		return guildGuardianNPC{}, bad
	}
	n := guildGuardianNPC{id: int(id), dx: 40, dy: 40}
	if tokens, found := npc.Section("clickable distance add"); found {
		if len(tokens) != 2 || tokens[0].Kind != pvf.TokenInt || tokens[1].Kind != pvf.TokenInt || tokens[0].Int < 0 || tokens[0].Int > 65535 || tokens[1].Int < 0 || tokens[1].Int > 65535 {
			return guildGuardianNPC{}, bad
		}
		n.dx += int(tokens[0].Int)
		n.dy += int(tokens[1].Int)
	}
	doc, err := read("map", mapPath)
	if err != nil {
		return guildGuardianNPC{}, err
	}
	mapDef := worldmap.ParseMap(0, doc.Path, doc)
	found = false
	for _, spawn := range mapDef.NPCs {
		if spawn.NPCID == int64(id) {
			if found || spawn.Position.X < 0 || spawn.Position.X > 65535 || spawn.Position.Y < 0 || spawn.Position.Y > 65535 {
				return guildGuardianNPC{}, bad
			}
			n.x, n.y, found = int(spawn.Position.X), int(spawn.Position.Y), true
		}
	}
	if !found {
		return guildGuardianNPC{}, bad
	}
	return n, nil
}
