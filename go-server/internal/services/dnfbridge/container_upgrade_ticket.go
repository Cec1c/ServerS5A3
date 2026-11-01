package dnfbridge

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/cargo"
	"longheng.io/server/internal/modules/dnf/inventory"
	"longheng.io/server/internal/modules/dnf/jobmap"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

func currentContainerUpgradeTicketDefinition(id int64, path string, doc *dnfpvf.Document) (cargo.UpgradeTicketDefinition, error) {
	d := cargo.UpgradeTicketDefinition{ItemID: id, PVFPath: path}
	if doc == nil {
		return d, cargo.ErrUpgradeTicket
	}
	kind, _ := doc.Text("stackable type")
	a, ok := doc.Section("action type")
	if kind != "[etc]" || !ok || len(a) != 1 || a[0].Kind != dnfpvf.TokenString {
		return d, cargo.ErrUpgradeTicket
	}
	switch a[0].Value {
	case "[upgrade cargo]":
		d.Action = cargo.UpgradeTicketPersonal
	case "[upgrade inventory]":
		d.Action = cargo.UpgradeTicketInventory
	case "[upgrade account cargo]":
		d.Action = cargo.UpgradeTicketAccount
	default:
		return d, cargo.ErrUpgradeTicket
	}
	min, ok := doc.Section("minimum level")
	if !ok || len(min) != 1 || min[0].Kind != dnfpvf.TokenInt || min[0].Int < 1 || min[0].Int > math.MaxUint8 {
		return d, cargo.ErrUpgradeTicket
	}
	d.MinimumLevel = int(min[0].Int)
	d.Jobs = doc.Texts("usable job")
	jobTokens, _ := doc.Section("usable job")
	if len(d.Jobs) == 0 || len(d.Jobs) != len(jobTokens) {
		return d, cargo.ErrUpgradeTicket
	}
	for _, tag := range d.Jobs {
		known := tag == "[all]"
		for j := 0; j <= jobmap.MaxID; j++ {
			for _, exact := range jobmap.QuestTags(j) {
				known = known || tag == exact
			}
		}
		if !known {
			return d, cargo.ErrUpgradeTicket
		}
	}
	d.Places = doc.Texts("action usable place")
	placeTokens, _ := doc.Section("action usable place")
	if len(d.Places) != len(placeTokens) {
		return d, cargo.ErrUpgradeTicket
	}
	for _, v := range d.Places {
		if v != "[village]" && v != "[seria room]" {
			return d, cargo.ErrUpgradeTicket
		}
	}
	// Refuse unimplemented future conditions rather than silently dropping them.
	// need material / daily purchase limit are NPC exchange costs/limits, not use costs.
	allowed := map[string]bool{}
	for _, tag := range strings.Split("name|explain|flavor text|grade|attach type|rarity|usable job|minimum level|icon|icon mark|stackable type|move wav|action type|action usable place|expiration date|stack limit|impossible contents|usable period|item category|daily purchase limit|need material", "|") {
		allowed[tag] = true
	}
	seen := map[string]bool{}
	for _, section := range doc.Sections {
		tag := section.Name
		if strings.HasPrefix(tag, "/") {
			continue
		}
		if !allowed[tag] {
			return d, fmt.Errorf("unsupported upgrade ticket condition %s", tag)
		}
		if seen[tag] && tag != "impossible contents" {
			return d, fmt.Errorf("duplicate upgrade ticket field %s", tag)
		}
		seen[tag] = true
		if tag == "impossible contents" {
			for _, v := range doc.Tokens[section.Start:section.End] {
				if v.Value != "gift" && v.Value != "ban redeemitem" {
					return d, fmt.Errorf("unsupported upgrade ticket restriction %s", v.Value)
				}
			}
		}
	}
	return d, nil
}

func currentContainerUpgradeAccountTiers(source interface{ ReadText(string) (string, error) }) ([]int64, error) {
	doc, err := parseDungeonCardPVFDocument(source, "etc/accountcargo.etc")
	if err != nil {
		return nil, err
	}
	values := doc.Ints("upgrade info")
	tokens, _ := doc.Section("upgrade info")
	if len(values) < 12 || len(values)%6 != 0 || len(values) != len(tokens) {
		return nil, cargo.ErrUpgradeTicket
	}
	tiers := make([]int64, 0, len(values)/6)
	for i := 0; i < len(values); i += 6 {
		tiers = append(tiers, values[i])
	}
	return tiers, nil
}

// Native senders: action45 -> op408/u16slot; action40 -> op430/u16slot;
// action148 -> op515/u16slot,u8list,0:u32,148:u32,0:u32,0:u32.
// The matched client's class1 allowlist must include 408/430. Native 408 ACK
// reads 01 or 00,13; 430 clears pending using the same dispatcher. The capacity
// itself is projected separately. Do not substitute enum value 431.
func (s *Service) handleCurrentContainerUpgradeTicket(p *gameSession, cmd byte, op uint16, body []byte) (bool, error) {
	action := uint32(0)
	switch op {
	case 408:
		action = cargo.UpgradeTicketPersonal
	case 430:
		action = cargo.UpgradeTicketInventory
	case 515:
		if len(body) < 11 || binary.LittleEndian.Uint32(body[7:11]) != cargo.UpgradeTicketAccount {
			return false, nil
		}
		action = cargo.UpgradeTicketAccount
	default:
		return false, nil
	}
	if p == nil {
		return true, fmt.Errorf("upgrade ticket session missing")
	}
	slot := int16(-1)
	list := byte(0)
	if len(body) >= 2 {
		slot = int16(binary.LittleEndian.Uint16(body))
	}
	if op == 515 && len(body) >= 3 {
		list = body[2]
	}
	reject := func(reason any) (bool, error) {
		s.logGameEvent(p, "container-upgrade-ticket-rejected", "op", op, "slot", slot, "reason", reason)
		b := []byte{0, 0x13}
		if op == 515 {
			b = inventory.BuildStackableActionFailure(slot, list, byte(action), 0x13)
		}
		return true, s.sendGameUpperRawClass(p, op, b, 1)
	}
	if cmd != 1 || slot < 0 || (op != 515 && len(body) != 2) || (op == 515 && (len(body) != 19 || list != 0 || binary.LittleEndian.Uint32(body[3:7]) != 0 || binary.LittleEndian.Uint32(body[11:15]) != 0 || binary.LittleEndian.Uint32(body[15:19]) != 0)) {
		return reject("invalid request")
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || identity.generation == 0 {
		return reject("unbound character")
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Inventory == nil || g.CeraShopAssets == nil {
		return reject("repositories unavailable")
	}
	ctx, cancel := s.currentGuildContext()
	defer cancel()
	release, err := s.reserveCurrentGuildParticipants(ctx, []boundGameSessionCharacter{identity})
	if err != nil {
		return reject(err)
	}
	defer release()
	id := strconv.Itoa(int(identity.character))
	bag, found, err := g.Inventory.Load(ctx, id)
	if err != nil || !found {
		return reject("inventory missing")
	}
	stack, found := bag.Slots["0:"+strconv.Itoa(int(slot))]
	if !found || stack.ItemID <= 0 || stack.ItemID > math.MaxUint32 || stack.Count <= 0 {
		return reject("ticket missing")
	}
	catalog, err := s.currentPVFItemCatalog()
	if err != nil || catalog == nil || catalog.source == nil {
		return reject("PVF unavailable")
	}
	item, err := catalog.ResolveItem(uint32(stack.ItemID))
	if err != nil || item.Kind != dungeonDropItemStackable {
		return reject("definition missing")
	}
	doc, err := parseDungeonCardPVFDocument(catalog.source, item.PVFPath)
	if err != nil {
		return reject(err)
	}
	d, err := currentContainerUpgradeTicketDefinition(stack.ItemID, item.PVFPath, doc)
	if err != nil || d.Action != action {
		return reject("item/action mismatch or unsupported conditions")
	}
	if action == cargo.UpgradeTicketAccount {
		d.AccountTiers, err = currentContainerUpgradeAccountTiers(catalog.source)
		if err != nil {
			return reject(err)
		}
	}
	if !p.townMu.TryLock() {
		return reject("town changing")
	}
	if !p.dungeon.mu.TryLock() {
		p.townMu.Unlock()
		return reject("dungeon changing")
	}
	if !s.pvpMu.TryLock() {
		p.dungeon.mu.Unlock()
		p.townMu.Unlock()
		return reject("pvp changing")
	}
	result, err := func() (cargo.UpgradeTicketResult, error) {
		defer s.pvpMu.Unlock()
		defer p.dungeon.mu.Unlock()
		defer p.townMu.Unlock()
		if p.pvpRoom != nil || p.dungeon.runtime != nil || p.townSceneReadyCharacterID != identity.character || !p.townPositionSnapshot.PositionValid {
			return cargo.UpgradeTicketResult{}, cargo.ErrUpgradeTicket
		}
		for _, queued := range s.pvpMatching {
			if queued != nil && queued.member != nil && queued.member.session == p {
				return cargo.UpgradeTicketResult{}, cargo.ErrUpgradeTicket
			}
		}
		area, known := s.townArea(int64(p.townPositionSnapshot.TownID), int64(p.townPositionSnapshot.AreaID))
		if !known || area.MapPath == "" {
			return cargo.UpgradeTicketResult{}, cargo.ErrUpgradeTicket
		}
		place := "[village]"
		if townMapPathLooksLikeSeria(area.MapPath) {
			place = "[seria room]"
		}
		if len(d.Places) > 0 {
			allow := false
			for _, v := range d.Places {
				allow = allow || v == place
			}
			if !allow {
				return cargo.UpgradeTicketResult{}, cargo.ErrUpgradeTicket
			}
		}
		now := s.gameplayNow()
		return cargo.ApplyUpgradeTicket(ctx, g, cargo.UpgradeTicketCommand{AccountID: s.accountIDForSession(p), CharacterID: id, Slot: slot, Definition: d, Now: now, InitialSettings: newCharacterContainerStateSettings(id, now), Expired: currentDungeonDiscardStackExpired, SetCount: currentPetFeedSetCount, Revalidate: func() bool { return s.boundGameSessionCharacterCurrent(identity) && p.dungeon.runtime == nil }})
	}()
	if err != nil {
		return reject(err)
	}
	s.logGameEvent(p, "container-upgrade-ticket-committed", "item_id", stack.ItemID, "action", action, "before", result.Before, "after", result.After, "remaining", result.Remaining)
	ack := []byte{1}
	if op == 515 {
		ack = inventory.BuildStackableActionAck(slot, 0, action)
	}
	if err = s.sendGameUpperRawClass(p, op, ack, 1); err != nil {
		return true, err
	}
	if err = s.sendSelectedIncrementalItemSlotRefreshes(p, "container_upgrade_ticket", []alignedcmd.ItemSlotRefresh{{ListType: 0, SlotIndex: slot}}); err != nil {
		return true, err
	}
	switch action {
	case cargo.UpgradeTicketInventory:
		b, _, _, ok := s.buildCurrentItemListBodyForSession(ctx, p, 0)
		if !ok {
			return true, errCurrentCeraShopOwnerUnavailable
		}
		return true, s.sendCurrentSceneItemList(p, 13, b)
	case cargo.UpgradeTicketPersonal:
		return true, s.sendCurrentCeraShopCargoRefresh(ctx, p, currentCeraShopCommitResult{PersonalCargoSlotCount: result.After})
	case cargo.UpgradeTicketAccount:
		return true, s.sendCurrentCeraShopCargoRefresh(ctx, p, currentCeraShopCommitResult{AccountCargoSelectionKey: result.After})
	}
	return true, nil
}
