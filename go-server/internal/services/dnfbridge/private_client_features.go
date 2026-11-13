package dnfbridge

// The paired native client owns UI, damage measurement and native wear/pickup
// requests. This envelope only stores character-scoped presets and projects
// authoritative metadata. It never writes inventory/equipment or grants items.
import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"math"
	"strconv"
	"time"
	"unicode/utf16"
)

const privateClientFeaturesOpcode uint16 = 0x7f30

var privateFeatureSlots = []uint16{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25, 26, 27, 28, 29, 32}

type privateFeatureItem struct {
	Slot        uint16 `json:"slot"`
	ID          uint32 `json:"id"`
	Fingerprint string `json:"fingerprint"`
}
type privateFeaturePreset struct {
	Version int                  `json:"version"`
	Items   []privateFeatureItem `json:"items"`
}

func privateFeatureSlot(slot uint16) bool {
	for _, s := range privateFeatureSlots {
		if s == slot {
			return true
		}
	}
	return false
}
func privateFeatureFingerprint(e currentItemListEntry, avatar bool) string {
	b := e.data
	// Location, current durability, transient lock/use time and appearance rental
	// countdown do not identify a physical instance. All affix/socket/rune bytes
	// and the equipment quality/creature serial remain part of the identity.
	b[0] = 0
	b[1] = 0
	b[0x60] = 0
	if avatar {
		for i := 6; i < 10; i++ {
			b[i] = 0
		}
	} else {
		b[0x0b] = 0
		b[0x0c] = 0
	}
	sum := sha256.Sum256(b[:])
	return hex.EncodeToString(sum[:])
}
func privateFeatureWorn(r repo.EquipmentRecord, now time.Time) map[uint16]privateFeatureItem {
	out := map[uint16]privateFeatureItem{}
	for _, e := range r.Entries {
		slot, ok := currentEXEActorEquipmentSlot(e)
		if !ok || slot > 32 || !privateFeatureSlot(uint16(slot)) || e.ItemID <= 0 || (!e.ExpireAt.IsZero() && !e.ExpireAt.After(now)) {
			continue
		}
		row, ok := currentItemListEntryFromEquipment(e)
		if !ok {
			continue
		}
		out[uint16(slot)] = privateFeatureItem{uint16(slot), uint32(e.ItemID), privateFeatureFingerprint(row, slot <= 10)}
	}
	return out
}
func privateFeatureResolve(wanted privateFeatureItem, equipment repo.EquipmentRecord, inv repo.InventoryRecord, now time.Time) (status, list, slot uint16) {
	if got, ok := privateFeatureWorn(equipment, now)[wanted.Slot]; ok && got == wanted {
		return 0, 3, wanted.Slot
	}
	count := 0
	for k, item := range inv.Slots {
		l, s, ok := parseSceneInventorySlotKey(k)
		if !ok || s < 0 || (l != 0 && l != 1 && l != 7 && l != 38) || item.Count < 1 || uint32(item.ItemID) != wanted.ID || (!item.ExpireAt.IsZero() && !item.ExpireAt.After(now)) || !repo.CurrentInventoryItemProjects(k, item, l) {
			continue
		}
		// A raw-only current avatar row stores its ability at +0x0B. The
		// generic inventory projector also accepts legacy +0x0A rows; normalize
		// only this private identity view, without changing the owned instance.
		if wanted.Slot <= 10 && currentItemStackHasCurrentRaw77(item) {
			if _, explicit := currentItemListExtraUint16OK(item.Extra, "ext_data0", "ext0", "ability_no", "avatar_ability_no"); !explicit {
				extra := make(map[string]string, len(item.Extra)+1)
				for k, v := range item.Extra {
					extra[k] = v
				}
				extra["avatar_ability_no"] = strconv.Itoa(int(binary.LittleEndian.Uint16(item.RawEntry[0x0B:0x0D])))
				item.Extra = extra
			}
		}
		row := currentItemListEntryFromStack(l, s, item)
		if privateFeatureFingerprint(row, wanted.Slot <= 10) != wanted.Fingerprint {
			continue
		}
		count++
		list = uint16(l)
		slot = uint16(s)
	}
	if count == 1 {
		return 1, list, slot
	}
	if count > 1 {
		return 4, 0, 0
	}
	return 3, 0, 0
}
func privateFeatureSkyIDs(catalog *pvfDungeonDropCatalog, eq repo.EquipmentRecord, now time.Time) ([8]uint32, bool) {
	var ids [8]uint32
	worn := privateFeatureWorn(eq, now)
	complete := true
	for slot := uint16(0); slot < 8; slot++ {
		item, ok := worn[slot]
		if !ok {
			complete = false
			break
		}
		def, e := catalog.ResolveItem(item.ID)
		if e != nil || def.Grade != 3 || def.Rarity != 2 {
			complete = false
			break
		}
		text, readErr := catalog.source.ReadText(def.PVFPath)
		doc, parseErr := dnfpvf.Parse(def.PVFPath, text)
		if readErr != nil || parseErr != nil {
			complete = false
			break
		}
		setID, present := doc.Int("part set index")
		rule, valid := currentEquipmentPlacementRuleForPVFType(def.EquipmentType)
		if !present || setID != 3 || !valid || rule.targetSlot != int16(slot) {
			complete = false
			break
		}
		ids[slot] = item.ID
	}
	if !complete {
		return [8]uint32{}, false
	}
	return ids, true
}

func (s *Service) handlePrivateClientFeatures(p *gameSession, class byte, q []byte) error {
	if len(q) == 24 && q[6] == 6 {
		return s.handlePrivateCombatInspection(p, class, q)
	}
	if p == nil || class != 1 || len(q) != 24 || string(q[:4]) != "CFQ1" || (binary.LittleEndian.Uint16(q[4:]) != 1 && binary.LittleEndian.Uint16(q[4:]) != 2) || q[6] > 5 || q[7] >= 6 {
		return nil
	}
	version := binary.LittleEndian.Uint16(q[4:])
	if version == 1 && q[6] > 2 {
		return nil
	}
	tail := 18
	if q[6] == 5 {
		tail = 20
		if q[18] > 7 || q[19] != 0 {
			return nil
		}
	}
	for _, v := range q[tail:] {
		if v != 0 {
			return nil
		}
	}
	ordinal := binary.LittleEndian.Uint16(q[16:])
	if (q[6] != 2 && q[6] != 5 && ordinal != 0) || (q[6] == 2 && ordinal >= 29) || (q[6] == 5 && ordinal > 255) {
		return nil
	}
	identity, bound := s.boundGameSessionCharacterSnapshot(p)
	if !bound || !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Settings == nil || g.Equipment == nil || g.Inventory == nil || g.Character == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	id := strconv.Itoa(int(identity.character))
	now := s.gameplayNow()
	eq, _, err := g.Equipment.Load(ctx, id)
	if err != nil {
		return err
	}
	scope := "native-client-preset-v1/" + id
	settings, found, err := g.Settings.Load(ctx, scope)
	if err != nil {
		return err
	}
	if !found {
		settings = repo.SettingsRecord{Scope: scope, Values: map[string]string{}}
	}
	if settings.Values == nil {
		settings.Values = map[string]string{}
	}
	size := 256
	if version == 2 {
		size = 512
	}
	b := make([]byte, size)
	copy(b, "CFR1")
	binary.LittleEndian.PutUint16(b[4:], version)
	copy(b[6:16], q[6:16])
	binary.LittleEndian.PutUint32(b[16:], uint32(identity.character))
	copy(b[22:24], q[16:18])
	put16 := func(off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	status := uint16(0)
	if q[6] == 1 {
		if currentDungeonSceneActive(p) {
			status = 2
		} else {
			worn := privateFeatureWorn(eq, now)
			preset := privateFeaturePreset{Version: 1}
			for _, slot := range privateFeatureSlots {
				if x, ok := worn[slot]; ok {
					preset.Items = append(preset.Items, x)
				}
			}
			raw, _ := json.Marshal(preset)
			settings.Values[strconv.Itoa(int(q[7]))] = string(raw)
			settings.UpdatedAt = now
			if !s.boundGameSessionCharacterCurrent(identity) {
				return nil
			}
			if err = g.Settings.Save(ctx, settings); err != nil {
				return err
			}
		}
	}
	if q[6] == 3 || q[6] == 5 {
		key := strconv.Itoa(int(q[7]))
		if q[6] == 3 {
			delete(settings.Values, key)
			delete(settings.Values, "hotkey/"+key)
		}
		if q[6] == 5 {
			settings.Values["hotkey/"+key] = strconv.FormatUint(uint64(binary.LittleEndian.Uint32(q[16:])), 10)
		}
		settings.UpdatedAt = now
		if !s.boundGameSessionCharacterCurrent(identity) {
			return nil
		}
		if err = g.Settings.Save(ctx, settings); err != nil {
			return err
		}
	}
	if q[6] == 2 {
		var preset privateFeaturePreset
		if json.Unmarshal([]byte(settings.Values[strconv.Itoa(int(q[7]))]), &preset) != nil || preset.Version != 1 || int(ordinal) >= len(preset.Items) || len(preset.Items) > 29 {
			status = 2
		} else {
			wanted := preset.Items[ordinal]
			if !privateFeatureSlot(wanted.Slot) || len(wanted.Fingerprint) != 64 {
				status = 2
			} else {
				inv, _, e := g.Inventory.Load(ctx, id)
				if e != nil {
					return e
				}
				status, l, sl := privateFeatureResolve(wanted, eq, inv, now)
				put16(20, status)
				put16(24, l)
				put16(26, sl)
				put16(28, wanted.Slot)
				put32(32, wanted.ID)
				if !s.boundGameSessionCharacterCurrent(identity) {
					return nil
				}
				return s.sendGameUpperRawClass(p, privateClientFeaturesOpcode, b, 0)
			}
		}
	}
	put16(20, status)
	for i := 0; i < 6; i++ {
		var preset privateFeaturePreset
		if json.Unmarshal([]byte(settings.Values[strconv.Itoa(i)]), &preset) == nil && preset.Version == 1 && len(preset.Items) <= 29 {
			put16(36+i*2, uint16(len(preset.Items)))
		}
	}
	if version == 2 {
		var selected privateFeaturePreset
		if json.Unmarshal([]byte(settings.Values[strconv.Itoa(int(q[7]))]), &selected) == nil && selected.Version == 1 && len(selected.Items) <= 29 {
			for _, item := range selected.Items {
				for i, slot := range privateFeatureSlots {
					if slot == item.Slot {
						put32(256+i*4, item.ID)
					}
				}
			}
		}
		for i := 0; i < 6; i++ {
			v, _ := strconv.ParseUint(settings.Values["hotkey/"+strconv.Itoa(i)], 10, 32)
			put32(384+i*4, uint32(v))
		}
	}
	// Only the eight main costume parts count. Skin/aura/title/pet cannot supply
	// missing pieces. Mixed generations of genuine rare avatars are supported.
	if catalog, e := s.currentPVFItemCatalog(); e == nil {
		if ids, complete := privateFeatureSkyIDs(catalog, eq, now); complete {
			for i, id := range ids {
				put32(48+i*4, id)
			}
			put16(142, 1)
		}
	}
	if err := s.fillPrivateCombatSnapshot(ctx, p, id, eq, now, version, b); err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(identity) {
		return nil
	}
	return s.sendGameUpperRawClass(p, privateClientFeaturesOpcode, b, 0)
}

// The same authoritative stat projection is used for self and an explicitly
// inspected peer. Presets, inventory and saved hotkeys are never sent to peers.
func (s *Service) fillPrivateCombatSnapshot(ctx context.Context, p *gameSession, id string, eq repo.EquipmentRecord, now time.Time, version uint16, b []byte) error {
	g, ok := s.repositoryGroup()
	if !ok || g.Character == nil {
		return nil
	}
	put16 := func(off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	character, exists, e := g.Character.Load(ctx, id)
	if e != nil {
		return e
	}
	if currentDungeonSceneActive(p) {
		put16(142, binary.LittleEndian.Uint16(b[142:])|2)
	}
	p.dungeon.mu.Lock()
	put32(236, uint32(p.dungeon.runToken))
	p.dungeon.mu.Unlock()
	if exists {
		put32(144, uint32(character.Level))
		job, _ := strconv.Atoi(character.Job)
		put32(148, uint32(job))
		grow := characterGrowTypeByte(character)
		put32(152, uint32(grow&15))
		put32(156, uint32(grow>>4))
		if version == 2 {
			if profiles, _, err := s.currentProfessionResources(ctx); err == nil {
				if profession, ok := profiles.DisplayName(byte(job), grow); ok {
					for i, c := range utf16.Encode([]rune(profession)) {
						if i >= 31 {
							break
						}
						put16(416+i*2, c)
					}
				}
			}
		}
		name := utf16.Encode([]rune(character.Name))
		if len(name) > 31 {
			name = name[:31]
		}
		for i, c := range name {
			put16(160+i*2, c)
		}
		stats, have := s.characterPVFStatsForUserInfo(ctx, p, character, true)
		if have {
			ids := combatPowerEquippedItemIDs(eq, true, repo.PetRecord{}, false, now)
			for _, item := range ids {
				if v, ok := s.equipmentPVFStat(ctx, item, nil); ok {
					stats.Add(v)
				}
			}
			values := []int64{stats.HPMax, stats.MPMax, stats.Strength, stats.Intelligence, stats.Vitality, stats.Spirit, stats.PhysicalAttack, stats.MagicalAttack, stats.IndependentAttack}
			for i, v := range values {
				put32(104+i*4, clampUint32(v/10))
			}
			base := float64(stats.Strength+stats.Intelligence+stats.Vitality+stats.Spirit)/10*8 + float64(stats.HPMax+stats.MPMax)/200 + float64(stats.PhysicalAttack+stats.MagicalAttack+stats.IndependentAttack)/10
			equipmentScore := float64(0)
			combatSlots := uint32(0)
			for _, entry := range eq.Entries {
				slot, ok := currentEXEActorEquipmentSlot(entry)
				if !ok || !(slot >= 12 && slot <= 23 || slot == 25 || slot == 26) || (!entry.ExpireAt.IsZero() && !entry.ExpireAt.After(now)) {
					continue
				}
				if row, ok := currentItemListEntryFromEquipment(entry); ok {
					upgrade := float64(row.data[0xa])
					equipmentScore += 200 + upgrade*upgrade*10
					combatSlots++
					amp := row.data[0x13]
					if amp >= 1 && amp <= 4 {
						equipmentScore += float64(binary.LittleEndian.Uint16(row.data[0x14:])) * 20
					}
				}
			}
			put32(224, clampUint32(int64(equipmentScore)))
			put32(228, uint32(len(ids)))
			put32(232, combatSlots)
			if e := s.preloadCombatPowerAffixCatalog(ctx); e == nil {
				s.combatPowerMu.Lock()
				catalog := s.combatPowerCatalog
				s.combatPowerMu.Unlock()
				if result, e := catalog.Aggregate(ctx, ids); e == nil {
					a := result.Affixes
					affixes := []float64{a.WhiteDamage, a.YellowDamage, a.CriticalDamage, a.YellowAdditional, a.CriticalAdditional, a.AllAttack}
					for i, v := range affixes {
						put16(92+i*2, combatPowerPercentTenths(v))
					}
					// Upstream formula v6 uses occupied combat slots, enhancement and amplification.
					equipmentScore += base * ((1+a.WhiteDamage/100)*(1+(a.YellowDamage+a.YellowAdditional)/100)*(1+(a.CriticalDamage+a.CriticalAdditional)/100) - 1)
					score := base + equipmentScore
					if !math.IsNaN(score) && !math.IsInf(score, 0) {
						put32(80, clampUint32(int64(score)))
					}
				}
			}
			put32(84, clampUint32(int64(base)))
			put32(88, clampUint32(int64(equipmentScore)))
		}
	}
	return nil
}

func (s *Service) handlePrivateCombatInspection(p *gameSession, class byte, q []byte) error {
	if p == nil || class != 1 || len(q) != 24 || string(q[:4]) != "CFQ1" || binary.LittleEndian.Uint16(q[4:]) != 2 || q[6] != 6 || q[7] != 0 {
		return nil
	}
	for _, v := range q[18:] {
		if v != 0 {
			return nil
		}
	}
	viewer, ok := s.boundGameSessionCharacterSnapshot(p)
	if !ok {
		return nil
	}
	targetID := binary.LittleEndian.Uint16(q[16:])
	actor, ok := s.onlineGameSessionCharacterSnapshot(targetID)
	if !ok || targetID == 0 || actor.character == viewer.character {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	if !s.currentPeerProfileSceneAllowed(ctx, p, actor.session, viewer.character, targetID) &&
		!s.currentDungeonPeerUserInfoAllowed(ctx, p, actor.session, viewer.character, targetID) &&
		!s.currentCrossSceneProfileAllowed(ctx, p, actor.session, viewer.character, targetID) {
		return nil
	}
	g, ok := s.repositoryGroup()
	if !ok || g.Equipment == nil {
		return nil
	}
	id := strconv.Itoa(int(targetID))
	eq, _, err := g.Equipment.Load(ctx, id)
	if err != nil {
		return err
	}
	b := make([]byte, 512)
	copy(b, "CFR1")
	binary.LittleEndian.PutUint16(b[4:], 2)
	copy(b[6:16], q[6:16])
	binary.LittleEndian.PutUint32(b[16:], uint32(targetID))
	if err = s.fillPrivateCombatSnapshot(ctx, actor.session, id, eq, s.gameplayNow(), 2, b); err != nil {
		return err
	}
	if !s.boundGameSessionCharacterCurrent(viewer) || !s.boundGameSessionCharacterCurrent(actor) {
		return nil
	}
	return s.sendGameUpperRawClass(p, privateClientFeaturesOpcode, b, 0)
}
