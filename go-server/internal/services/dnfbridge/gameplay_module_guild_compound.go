package dnfbridge

import (
	"context"
	"errors"
	"strconv"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/guildcompound"
)

func guildCompoundGameplayModule() gameplayModuleDefinition {
	module := gameplayModuleDefinition{
		Name:              "guild-compound",
		LegacyHandlers:    map[uint16]gameplayHandler{},
		UpperHandlers:     map[uint16]gameplayHandler{},
		LegacyNormalizers: map[uint16]gameplayLegacyNormalizer{},
	}
	for opcode, kind := range map[uint16]guildcompound.CompoundKind{
		uint16(dnfenum.CmdPacketCompoundGem):  guildcompound.CompoundGem,
		uint16(dnfenum.CmdPacketCompoundFlag): guildcompound.CompoundFlag,
	} {
		handler := defaultClassGameplayHandler("guild-compound-class-rejected", "class mismatch", func(s *Service, g *gameSession, body []byte) error {
			return s.handleCurrentGuildCompound(g, opcode, kind, body)
		})
		module.LegacyHandlers[opcode] = handler
		module.UpperHandlers[opcode] = handler
		module.LegacyNormalizers[opcode] = func(body []byte) []byte { return stripLegacyTransportTrailer(body, guildcompound.RequestBodyLen) }
	}
	return module
}

func (s *Service) handleCurrentGuildCompound(g *gameSession, opcode uint16, kind guildcompound.CompoundKind, body []byte) error {
	if g == nil {
		return nil
	}
	reject := func(err error) error {
		code := byte(1)
		if errors.Is(err, guildcompound.ErrNoSpace) {
			code = 95
		}
		s.logGameEvent(g, "guild-compound-rejected", "kind", kind.String(), "error", err)
		if sendErr := s.sendGameUpperRawClass(g, opcode, []byte{0, code}, 1); sendErr != nil {
			return sendErr
		}
		// A client can remove the selected rows optimistically before the
		// result arrives. A rejected or rolled-back request therefore also
		// needs the authoritative list38 snapshot, otherwise the untouched
		// materials remain hidden until the player reopens the window.
		if refreshErr := s.sendCurrentGuardianGemList38Refresh(g, "guild_compound_rejected"); refreshErr != nil {
			s.logGameEvent(g, "guild-compound-rejected-refresh-failed", "kind", kind.String(), "error", refreshErr)
		}
		return nil
	}
	request, err := guildcompound.DecodeRequest(body)
	if err != nil {
		return reject(err)
	}
	if g.selectedCharacterID == 0 {
		return reject(guildcompound.ErrInvalid)
	}
	g.dungeon.mu.Lock()
	inDungeon := g.dungeon.runtime != nil
	g.dungeon.mu.Unlock()
	if inDungeon {
		return reject(guildcompound.ErrInvalid)
	}
	repositories, ok := s.repositoryGroup()
	if !ok {
		return reject(guildcompound.ErrInvalid)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err)
	}
	now := s.gameplayNow()
	rules, err := currentGuildCompoundRules(catalog, now)
	if err != nil {
		return reject(err)
	}
	owner, err := guildcompound.NewOwner(repositories)
	if err != nil {
		return reject(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	result, err := owner.Apply(ctx, guildcompound.Command{
		AccountID: s.accountIDForSession(g), CharacterID: strconv.Itoa(int(g.selectedCharacterID)),
		Kind: kind, Request: request, Rules: rules, Now: now,
	})
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(g, "guild-compound-committed", "kind", kind.String(), "count", request.Count,
		"grade", request.Grade, "success", result.Success, "result_item", result.ResultItem, "result_slot", result.ResultSlot)
	ack := []byte{1}
	if !result.Success {
		ack = []byte{0, 1}
	}
	// Both native result callbacks consume status/error only. A legal request now
	// always commits one output; rebuild list38 so the consumed rows and the new
	// instance are both visible to the current client.
	if err := s.sendGameUpperRawClass(g, opcode, ack, 1); err != nil {
		return err
	}
	// A socket failure must never replay the committed asset transaction.
	return s.sendCurrentGuardianGemList38Refresh(g, "guild_compound_after_ack")
}
