package equip

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrDurabilityTargetInvalid = errors.New("durability target or owner is invalid")

// DurabilityOwner commits wear and an eligible auto-repair together. The
// account contract and character's daily usage are read under the same lock.
type DurabilityOwner struct {
	assets dnfrepo.AccountCharacterLedgerAssetUnitOfWork
}

type DecreaseDurabilityCommand struct {
	AccountID   string
	CharacterID uint16
	Slot        int16
	Now         time.Time
}

type DecreaseDurabilityResult struct {
	Entry        dnfrepo.EquipmentEntry
	Previous     uint16
	Current      uint16
	Changed      bool
	AutoRepaired bool
	Gold         int64
}

func NewDurabilityOwner(repos dnfrepo.Group) (*DurabilityOwner, error) {
	assets, ok := repos.AccountAssets.(dnfrepo.AccountCharacterLedgerAssetUnitOfWork)
	if !ok {
		return nil, ErrOwnerUnavailable
	}
	return &DurabilityOwner{assets: assets}, nil
}

func (o *DurabilityOwner) Decrease(ctx context.Context, cmd DecreaseDurabilityCommand, resolve alignedcmd.RepairCostResolver) (DecreaseDurabilityResult, error) {
	if o == nil || o.assets == nil || resolve == nil {
		return DecreaseDurabilityResult{}, ErrOwnerUnavailable
	}
	if strings.TrimSpace(cmd.AccountID) == "" || cmd.CharacterID == 0 || cmd.Slot < 0 || cmd.Slot > 32 {
		return DecreaseDurabilityResult{}, ErrDurabilityTargetInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := cmd.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	characterID := strconv.Itoa(int(cmd.CharacterID))
	var result DecreaseDurabilityResult
	err := o.assets.WithinAccountCharacterLedgerAssets(ctx, cmd.AccountID, characterID, func(accounts dnfrepo.AccountRepository, _ dnfrepo.AccountInventoryRepository, characters dnfrepo.CharacterRepository, _ dnfrepo.InventoryRepository, equipment dnfrepo.EquipmentRepository) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != characterID || character.AccountID != cmd.AccountID {
			return ErrDurabilityTargetInvalid
		}
		record, found, err := equipment.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || record.CharacterID != characterID {
			return ErrEquipmentNotFound
		}
		record = dnfrepo.CloneEquipment(record)
		key := entryKey(cmd.Slot)
		entry, found := record.Entries[key]
		if !found || entry.SlotIndex != cmd.Slot || entry.ItemID <= 0 {
			return ErrDurabilityTargetInvalid
		}
		expiryValid, err := durabilityEntryExpiryValid(entry, now)
		if err != nil {
			return err
		}
		if !expiryValid {
			return ErrDurabilityTargetInvalid
		}
		// Do not reinterpret a malformed current row as a legacy summary.
		offset := 10
		if len(entry.RawEntry) == currentEquipmentRawEntryWireSize {
			// A worn row keeps the original inventory slot in raw[0:2] after
			// op19. The equipment key and SlotIndex are authoritative for the
			// worn position; the raw item ID still has to match this same item.
			if !RepairDurabilityRawEntryIsCurrentRaw77(entry.RawEntry, entry.ItemID) {
				return ErrDurabilityInvalid
			}
			offset = RepairDurabilityOffsetCurrentRaw77
		} else if (len(entry.RawEntry) != 43 && len(entry.RawEntry) != 82) || int16(entry.RawEntry[0]) != cmd.Slot || int64(binary.LittleEndian.Uint32(entry.RawEntry[1:5])) != entry.ItemID {
			return ErrDurabilityInvalid
		}
		evidence, err := resolve(entry.ItemID)
		if err != nil {
			return err
		}
		if !durabilitySlotMatchesForCharacter(cmd.Slot, evidence.EquipmentType, character) || evidence.MaxDurability <= 0 || evidence.MaxDurability > 65535 {
			return ErrRepairNotRepairable
		}
		rawOld := binary.LittleEndian.Uint16(entry.RawEntry[offset : offset+2])
		old, err := durabilityEntryCurrent(entry, rawOld)
		if err != nil {
			return err
		}
		if int64(rawOld) > evidence.MaxDurability || int64(old) > evidence.MaxDurability {
			return ErrDurabilityInvalid
		}
		current := old
		if current > 0 {
			current--
		}
		character = dnfrepo.CloneCharacter(character)
		auto := false
		if current == 0 {
			account, exists, loadErr := accounts.Load(ctx, cmd.AccountID)
			if loadErr != nil {
				return loadErr
			}
			if !exists || account.AccountID != cmd.AccountID {
				return ErrDurabilityTargetInvalid
			}
			if premium.Active(account, premium.DevilSlotType(premium.DevilSlotAutoRepair), now) && premium.TryConsumeDaily(&character, premium.DevilSlotAutoRepair, now) {
				current = uint16(evidence.MaxDurability)
				auto = true
				character.UpdatedAt = now
				if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
					return err
				}
			}
		}
		changed := old != current || auto || rawOld != old
		if changed {
			binary.LittleEndian.PutUint16(entry.RawEntry[offset:offset+2], current)
			if entry.Extra == nil {
				entry.Extra = make(map[string]string)
			}
			entry.Extra["durability"] = strconv.Itoa(int(current))
			for _, alias := range []string{"current_durability", "value_b_word"} {
				if _, exists := entry.Extra[alias]; exists {
					entry.Extra[alias] = strconv.Itoa(int(current))
				}
			}
			for _, alias := range []string{"raw_entry_hex", "equipment_raw_entry_hex"} {
				if _, exists := entry.Extra[alias]; exists {
					entry.Extra[alias] = hex.EncodeToString(entry.RawEntry)
				}
			}
			record.Entries[key] = entry
			record.UpdatedAt = now
			if err := dnfrepo.SaveEquipmentFields(ctx, equipment, record, dnfrepo.EquipmentFieldEntries); err != nil {
				return err
			}
		}
		result = DecreaseDurabilityResult{Entry: entry, Previous: old, Current: current, Changed: changed, AutoRepaired: auto, Gold: character.Stats["gold"]}
		return nil
	})
	if err != nil {
		return DecreaseDurabilityResult{}, err
	}
	return result, nil
}

func durabilityEntryExpiryValid(entry dnfrepo.EquipmentEntry, now time.Time) (bool, error) {
	for _, key := range []string{"expire_time", "expire_unix"} {
		raw := strings.TrimSpace(entry.Extra[key])
		if raw == "" {
			continue
		}
		unix, err := strconv.ParseUint(raw, 0, 32)
		if err != nil {
			return false, ErrDurabilityInvalid
		}
		if unix != 0 && !time.Unix(int64(unix), 0).UTC().After(now) {
			return false, nil
		}
	}
	if !entry.ExpireAt.IsZero() && !entry.ExpireAt.After(now) {
		return false, nil
	}
	return true, nil
}

func durabilityEntryCurrent(entry dnfrepo.EquipmentEntry, rawCurrent uint16) (uint16, error) {
	for _, key := range []string{"durability", "current_durability"} {
		raw := strings.TrimSpace(entry.Extra[key])
		if raw == "" {
			continue
		}
		value, err := strconv.ParseUint(raw, 0, 16)
		if err != nil {
			return 0, ErrDurabilityInvalid
		}
		return uint16(value), nil
	}
	return rawCurrent, nil
}

func durabilitySlotMatchesForCharacter(slot int16, equipmentType string, character dnfrepo.CharacterRecord) bool {
	if durabilitySlotMatches(slot, equipmentType) {
		return true
	}
	if slot != 24 || !strings.EqualFold(strings.TrimSpace(equipmentType), "[weapon]") {
		return false
	}
	job, err := strconv.Atoi(strings.TrimSpace(character.Job))
	if err != nil || job != 11 {
		return false
	}
	packed, inDomain := dnfrepo.CharacterGrowTypeInDomain(character)
	if !inDomain {
		return false
	}
	branch, _ := dnfrepo.DecodeCharacterGrowType(packed)
	return branch == 4
}

func durabilitySlotMatches(slot int16, equipmentType string) bool {
	switch strings.ToLower(strings.TrimSpace(equipmentType)) {
	case "[weapon]":
		return slot == 12
	case "[coat]":
		return slot == 14
	case "[shoulder]":
		return slot == 15
	case "[pants]":
		return slot == 16
	case "[shoes]":
		return slot == 17
	case "[waist]":
		return slot == 18
	case "[support weapon]":
		return slot == 24
	default:
		return false
	}
}
