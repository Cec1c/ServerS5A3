package dnfbridge

import (
	"bytes"
	"context"
	"math"
	"strconv"
	"time"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"

	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/itemplacement"
	r "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/platform/eventloop"
)

// CMD17 needs its transport serial for paid horn messages. Other modes keep
// their existing handlers. Headerless calls cannot consume paid items.
func chatGameplayModule() gameplayModuleDefinition {
	run := func(s *Service, p *gameSession, req gameplayRequest) error {
		if !requireDefaultGameplayClassification(s, p, req, "game-chat-rejected", "command_class_required") {
			return nil
		}
		if len(req.Body) > 0 && (req.Body[0] == 11 || req.Body[0] == 13) {
			return s.handleCurrentMegaphone(p, req)
		}
		return s.handleCurrentPeerChat(p, req.Opcode, req.Body)
	}
	return gameplayModuleDefinition{Name: "chat", LegacyHandlers: map[uint16]gameplayHandler{17: run}, UpperHandlers: map[uint16]gameplayHandler{17: run}}
}

func currentMegaphoneDefinition(catalog *pvfDungeonDropCatalog, mode byte) (inventory.MegaphoneDefinition, error) {
	id, path := inventory.MegaphoneItem(mode)
	if catalog == nil || id == 0 {
		return inventory.MegaphoneDefinition{}, inventory.ErrMegaphone
	}
	item, err := catalog.ResolveItem(uint32(id))
	if err != nil {
		return inventory.MegaphoneDefinition{}, err
	}
	if item.Kind != dungeonDropItemStackable || item.PVFPath != path || item.StackableType != "[waste]" {
		return inventory.MegaphoneDefinition{}, inventory.ErrMegaphone
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, item.PVFPath)
	if err != nil {
		return inventory.MegaphoneDefinition{}, err
	}
	level, found := doc.Int("minimum level")
	if !found || level < 1 || level > 100 {
		return inventory.MegaphoneDefinition{}, inventory.ErrMegaphone
	}
	places := doc.Texts("action usable place")
	village, room := false, false
	for _, p := range places {
		switch p {
		case "[village]":
			village = true
		case "[seria room]":
			room = true
		default:
			return inventory.MegaphoneDefinition{}, inventory.ErrMegaphone
		}
	}
	if !village || !room {
		return inventory.MegaphoneDefinition{}, inventory.ErrMegaphone
	}
	usage, err := resolveCurrentItemUsageDefinition(catalog, id)
	if err != nil {
		return inventory.MegaphoneDefinition{}, err
	}
	return inventory.MegaphoneDefinition{ItemID: id, PVFPath: path, StackableType: item.StackableType, MinimumLevel: int(level), Usage: usage,
		Slots: itemplacement.SlotPolicy{ListType: r.MainInventoryListType, Start: item.SlotStart, End: item.SlotEnd, AllowQuickSlots: true},
		Project: func(stack r.ItemStack, now time.Time) r.ItemStack {
			v, _ := applyCurrentPVFItemExpirationAt(stack, item, now)
			return v
		}}, nil
}

// V200 200C9A0: type, server group, decoration, owner channel, sender UID,
// name DSTR, message DSTR, group-name DSTR. This receiver never requires the
// sender's scene actor, and predicts one source debit on the sender's client.
func buildCurrentMegaphoneBody(mode, group, channel byte, id uint16, name, message []byte) []byte {
	var w packetWriter
	w.writeBytes([]byte{mode, group, 0, channel})
	w.writeUint16(id)
	w.writeRawDstr(name)
	w.writeRawDstr(message)
	w.writeRawDstr(nil)
	return w.bytes()
}

func validCurrentMegaphoneText(message []byte) bool {
	if len(message) == 0 || len(message) > 1016 || bytes.IndexByte(message, 0) >= 0 {
		return false
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(message)
	if err != nil {
		return false
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes(decoded)
	// V200 2A0330A rejects 255 or more UTF-16 code units, not 255 bytes.
	return err == nil && bytes.Equal(encoded, message) && len(utf16.Encode([]rune(string(decoded)))) < 255
}

func (s *Service) handleCurrentMegaphone(p *gameSession, req gameplayRequest) error {
	if p == nil {
		return errInvalidGameSessionEvent
	}
	ctx, cancel := guildChatContext(p)
	defer cancel()
	reject := func(err error) error {
		s.logGameEvent(p, "game-megaphone-rejected", "reason", err)
		return s.sendCurrentGuildChatFrame(ctx, p, 17, []byte{0, 1}, 1)
	}
	q, err := decodeCurrentPeerChatRequest(req.Body)
	if err != nil || s.options.currentClientVersion != "official-24.9" || req.Sequence == nil || q.TargetID != 0 || q.Unknown > math.MaxInt16 ||
		len(q.TargetName) != 0 || q.HasTrailingValue || len(req.Body) != 11+len(q.Message) || !validCurrentMegaphoneText(q.Message) {
		return reject(inventory.ErrMegaphone)
	}
	high, known := p.guild.purchaseSequence.currentHigh()
	if !known || *req.Sequence != high {
		return reject(inventory.ErrMegaphoneReplay)
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || p.loginRevoked.Load() || p.townSceneReadyCharacterID != identity.character {
		return reject(errInvalidGameSessionEvent)
	}
	p.dungeon.mu.Lock()
	inDungeon := p.dungeon.runtime != nil
	p.dungeon.mu.Unlock()
	if inDungeon {
		return reject(inventory.ErrMegaphone)
	}
	account := s.accountIDForSession(p)
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		return reject(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	repos, available := s.repositoryGroup()
	if !available || repos.Character == nil {
		return reject(inventory.ErrOwnerUnavailable)
	}
	c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(identity.character)))
	if err != nil || !found || c.AccountID != account {
		return reject(inventory.ErrCharacterRequired)
	}
	name, validName := currentPartyChatName(c.Name)
	channel, validChannel := currentSessionOwnerChannelField(p)
	group := c.Stats["server_group_id"]
	if !validName || !validChannel || group < 0 || group >= 250 {
		return reject(inventory.ErrMegaphone)
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil {
		return reject(err)
	}
	d, err := currentMegaphoneDefinition(catalog, q.MessageType)
	if err != nil {
		return reject(err)
	}
	nonce, err := p.guild.purchaseSequence.identity(0)
	if err != nil {
		return reject(err)
	}
	owner, err := inventory.NewOwner(repos)
	if err != nil {
		return reject(err)
	}
	result, err := owner.ConsumeMegaphone(ctx, inventory.MegaphoneCommand{AccountID: account, CharacterID: identity.character, Mode: q.MessageType, Slot: int16(q.Unknown),
		Session: nonce, Sequence: *req.Sequence, Now: s.gameplayNow().UTC(), Definition: d,
		Revalidate: func() bool { return !p.loginRevoked.Load() && s.boundGameSessionCharacterCurrent(identity) }})
	if err != nil {
		return reject(err)
	}
	body := buildCurrentMegaphoneBody(q.MessageType, byte(group), channel, identity.character, name, q.Message)
	peers := s.currentMegaphonePeers(identity, q.MessageType)
	sourceChannel := p.channel.ID
	validate := func() error {
		if p.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(identity) {
			return errInvalidGameSessionEvent
		}
		return nil
	}
	// The database is authoritative. Echo before the final slot projection so
	// native self-prediction cannot decrement the freshly projected count twice.
	sourceErr := s.sendCurrentGuildChatFrameChecked(ctx, p, 17, []byte{1}, 1, validate)
	if sourceErr == nil {
		sourceErr = s.sendCurrentGuildChatFrameChecked(ctx, p, 118, body, 0, validate)
	}
	if sourceErr == nil {
		entry := currentItemListEntryFromStack(r.MainInventoryListType, int16(q.Unknown), result.Stack)
		if result.Remaining == 0 {
			entry = currentItemListEntry{}
			entry.patchCore(int16(q.Unknown), math.MaxUint32, 0)
		}
		sourceErr = s.sendCurrentGuildChatFrameChecked(ctx, p, 14, buildCurrentItemUpdateBody(r.MainInventoryListType, []currentItemListEntry{entry}), 0, validate)
	}
	s.releaseCurrentCharacterAssets(reservation)
	for _, peer := range peers {
		if err := s.queueCurrentMegaphone(peer, q.MessageType, sourceChannel, group, body); err != nil {
			s.logWarn("megaphone recipient unavailable", "target_char_id", peer.character, "error", err)
		}
	}
	s.logGameEvent(p, "game-megaphone-committed", "item_id", d.ItemID, "slot", q.Unknown, "remaining", result.Remaining, "mode", q.MessageType, "recipient_candidates", len(peers)+1)
	return sourceErr
}

func (s *Service) currentMegaphonePeers(source boundGameSessionCharacter, mode byte) []boundGameSessionCharacter {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peers []boundGameSessionCharacter
	for id, p := range s.gameSessions {
		if p == nil || p == source.session || id == source.character || p.selectedCharacterID != id || p.loginRevoked.Load() || (mode == 13 && p.channel.ID != source.session.channel.ID) {
			continue
		}
		peers = append(peers, boundGameSessionCharacter{session: p, character: id, generation: p.characterGeneration})
	}
	return peers
}

func (s *Service) queueCurrentMegaphone(peer boundGameSessionCharacter, mode byte, channel int, group int64, body []byte) error {
	p := peer.session
	if p == nil || p.loginRevoked.Load() {
		return errInvalidGameSessionEvent
	}
	body = append([]byte(nil), body...)
	expires := time.Now().Add(createWriteTimeout)
	account := s.accountIDForSession(p)
	run := func() error {
		ctx, cancel := guildChatContext(p)
		defer cancel()
		ctx, deadline := context.WithDeadline(ctx, expires)
		defer deadline()
		reservation, err := s.reserveCurrentCharacterAssets(ctx, peer, account)
		if err != nil {
			return nil
		}
		defer s.releaseCurrentCharacterAssets(reservation)
		validate := func() error {
			if p.loginRevoked.Load() || !s.boundGameSessionCharacterCurrent(peer) || mode == 13 && p.channel.ID != channel {
				return errInvalidGameSessionEvent
			}
			repos, ok := s.repositoryGroup()
			if !ok || repos.Character == nil {
				return errInvalidGameSessionEvent
			}
			c, found, err := repos.Character.Load(ctx, strconv.Itoa(int(peer.character)))
			if err != nil || !found || c.AccountID != account || c.Stats["server_group_id"] != group {
				return errInvalidGameSessionEvent
			}
			return nil
		}
		if err := s.sendCurrentGuildChatFrameChecked(ctx, p, 118, body, 0, validate); err != nil {
			s.logWarn("megaphone recipient delivery failed", "target_char_id", peer.character, "error", err)
		}
		return nil
	}
	if p.events == nil || p.events.loop == nil {
		return run()
	}
	if !p.events.active.Load() {
		return errInvalidGameSessionEvent
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return p.events.loop.Submit(eventloop.Event{Type: "game-megaphone-delivery", Priority: eventloop.PriorityMedium, Context: ctx,
		Data: gameSessionEventTask{source: "game-megaphone-delivery", characterID: peer.character, characterGeneration: peer.generation, async: true, run: run}})
}
