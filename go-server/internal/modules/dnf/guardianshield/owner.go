// Package guardianshield owns the Guardian's current-client five-slot shield
// deck and its main-shield actor projection. The catalog itself remains PVF
// owned and is supplied through a typed resolver by dnfbridge.
package guardianshield

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/itemquality"
	dnfquest "longheng.io/server/internal/modules/dnf/quest"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	DeckSlotCount = 5
	MainDeckSlot  = 0
	ActorSlot     = int16(24)

	WireListEquipped byte = 33
	WireListCatalog  byte = 34

	guardianJob             = 12
	currentRawItemEntrySize = 0x77
)

const projectionMarker = "guardian_shield_deck_projection"

var (
	ErrOwnerUnavailable             = errors.New("guardian shield owner unavailable")
	ErrCharacterRequired            = errors.New("guardian shield selected character is required")
	ErrCharacterNotFound            = errors.New("guardian shield character not found")
	ErrOwnerMismatch                = errors.New("guardian shield character owner mismatch")
	ErrCharacterIneligible          = errors.New("guardian shield character job or grow type is ineligible")
	ErrResolverRequired             = errors.New("guardian shield PVF resolver is required")
	ErrQuestRepositoryRequired      = errors.New("guardian shield quest repository is required")
	ErrClearQuestRequired           = errors.New("guardian shield catalog clear quest is required")
	ErrClearQuestIncomplete         = errors.New("guardian shield catalog clear quest is not completed")
	ErrUnlockKindInvalid            = errors.New("guardian shield catalog unlock kind is invalid")
	ErrMinimumLevelRequired         = errors.New("guardian shield catalog minimum level is required")
	ErrLevelInsufficient            = errors.New("guardian shield character level is below the catalog minimum")
	ErrMoveUnsupported              = errors.New("guardian shield move endpoints are unsupported")
	ErrDeckSlotInvalid              = errors.New("guardian shield deck slot is outside the current client range")
	ErrDeckSlotEmpty                = errors.New("guardian shield deck source slot is empty")
	ErrDeckDuplicate                = errors.New("guardian shield deck contains a duplicate item")
	ErrMainDeckMutationRequiresMove = errors.New("guardian shield main slot can change only through move-itemspace")
	ErrProjectionConflict           = errors.New("guardian shield main projection conflicts with an existing slot-24 item")
)

var deckStatKeys = [DeckSlotCount]string{
	"guardian_shield_deck_0",
	"guardian_shield_deck_1",
	"guardian_shield_deck_2",
	"guardian_shield_deck_3",
	"guardian_shield_deck_4",
}

type Deck [DeckSlotCount]uint32

type MoveCommand struct {
	CharacterID          uint16
	AccountID            string
	SourceListType       byte
	SourceSlotIndex      int16
	SourceShieldItemID   uint32
	DestinationListType  byte
	DestinationSlotIndex int16
}

type Result struct {
	Deck        Deck
	Changed     bool
	MainChanged bool
}

type Owner struct {
	assets   dnfrepo.CharacterAssetUnitOfWork
	quests   dnfrepo.QuestRepository
	resolver alignedcmd.GuardianShieldResolver
}

func NewOwner(repositories dnfrepo.Group, resolver alignedcmd.GuardianShieldResolver) (*Owner, error) {
	if repositories.CharacterAssets == nil {
		return nil, ErrOwnerUnavailable
	}
	if resolver == nil {
		return nil, ErrResolverRequired
	}
	return &Owner{assets: repositories.CharacterAssets, quests: repositories.Quest, resolver: resolver}, nil
}

func IsWireList(listType byte) bool {
	return listType == WireListEquipped || listType == WireListCatalog
}

func DeckFromCharacter(character dnfrepo.CharacterRecord) Deck {
	var deck Deck
	for index, key := range deckStatKeys {
		if value := character.Stats[key]; value > 0 && value <= int64(^uint32(0)) {
			deck[index] = uint32(value)
		}
	}
	return deck
}

func LoadDeck(character dnfrepo.CharacterRecord) (Deck, error) {
	var deck Deck
	for index, key := range deckStatKeys {
		value := character.Stats[key]
		if value < 0 || value > int64(^uint32(0)) {
			return Deck{}, fmt.Errorf("guardian shield persisted slot=%d value=%d is outside u32", index, value)
		}
		deck[index] = uint32(value)
	}
	return deck, nil
}

func EligibleGrowType(character dnfrepo.CharacterRecord) (byte, error) {
	return eligibleGrowType(character)
}

func (o *Owner) Move(ctx context.Context, command MoveCommand) (Result, error) {
	if command.CharacterID == 0 {
		return Result{}, ErrCharacterRequired
	}
	if !IsWireList(command.SourceListType) || !IsWireList(command.DestinationListType) {
		return Result{}, fmt.Errorf("%w: source=%d destination=%d", ErrMoveUnsupported, command.SourceListType, command.DestinationListType)
	}
	return o.mutate(ctx, command.CharacterID, command.AccountID, func(deck Deck) (Deck, error) {
		switch {
		case command.SourceListType == WireListCatalog && command.DestinationListType == WireListEquipped:
			if command.SourceSlotIndex != 0 || command.DestinationSlotIndex != MainDeckSlot || command.SourceShieldItemID == 0 {
				return Deck{}, fmt.Errorf("%w: catalog source=(%d,item=%d) destination=%d", ErrMoveUnsupported, command.SourceSlotIndex, command.SourceShieldItemID, command.DestinationSlotIndex)
			}
			deck[MainDeckSlot] = command.SourceShieldItemID
			for index := 1; index < DeckSlotCount; index++ {
				if deck[index] == command.SourceShieldItemID {
					deck[index] = 0
				}
			}
			return deck, nil
		case command.SourceListType == WireListEquipped && command.DestinationListType == WireListCatalog:
			if command.SourceSlotIndex != MainDeckSlot || command.DestinationSlotIndex != DeckSlotCount {
				return Deck{}, fmt.Errorf("%w: deck source=%d catalog destination=%d", ErrMoveUnsupported, command.SourceSlotIndex, command.DestinationSlotIndex)
			}
			if deck[command.SourceSlotIndex] == 0 {
				return Deck{}, fmt.Errorf("%w: source=%d", ErrDeckSlotEmpty, command.SourceSlotIndex)
			}
			deck[command.SourceSlotIndex] = 0
			return deck, nil
		case command.SourceListType == WireListEquipped && command.DestinationListType == WireListEquipped:
			if !validDeckSlot(command.SourceSlotIndex) || !validDeckSlot(command.DestinationSlotIndex) {
				return Deck{}, fmt.Errorf("%w: source=%d destination=%d", ErrDeckSlotInvalid, command.SourceSlotIndex, command.DestinationSlotIndex)
			}
			if command.SourceSlotIndex != MainDeckSlot && command.DestinationSlotIndex != MainDeckSlot {
				return Deck{}, fmt.Errorf("%w: backup-only source=%d destination=%d must use op657", ErrMoveUnsupported, command.SourceSlotIndex, command.DestinationSlotIndex)
			}
			if deck[command.SourceSlotIndex] == 0 {
				return Deck{}, fmt.Errorf("%w: source=%d", ErrDeckSlotEmpty, command.SourceSlotIndex)
			}
			deck[command.SourceSlotIndex], deck[command.DestinationSlotIndex] = deck[command.DestinationSlotIndex], deck[command.SourceSlotIndex]
			return deck, nil
		default:
			return Deck{}, fmt.Errorf("%w: source=%d destination=%d", ErrMoveUnsupported, command.SourceListType, command.DestinationListType)
		}
	})
}

func (o *Owner) SaveDeck(ctx context.Context, characterID uint16, accountID string, requested Deck) (Result, error) {
	if characterID == 0 {
		return Result{}, ErrCharacterRequired
	}
	return o.mutate(ctx, characterID, accountID, func(before Deck) (Deck, error) {
		if requested[MainDeckSlot] != before[MainDeckSlot] {
			return Deck{}, fmt.Errorf(
				"%w: persisted=%d requested=%d",
				ErrMainDeckMutationRequiresMove,
				before[MainDeckSlot],
				requested[MainDeckSlot],
			)
		}
		return requested, nil
	})
}

func (o *Owner) mutate(
	ctx context.Context,
	selectedCharacterID uint16,
	accountID string,
	change func(Deck) (Deck, error),
) (Result, error) {
	if o == nil || o.assets == nil {
		return Result{}, ErrOwnerUnavailable
	}
	if o.resolver == nil {
		return Result{}, ErrResolverRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	characterID := strconv.FormatUint(uint64(selectedCharacterID), 10)
	accountID = strings.TrimSpace(accountID)
	var result Result
	err := o.assets.WithinCharacterAssets(ctx, characterID, func(
		characters dnfrepo.CharacterRepository,
		_ dnfrepo.InventoryRepository,
		equipment dnfrepo.EquipmentRepository,
	) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return fmt.Errorf("load guardian shield character: %w", err)
		}
		if !found {
			return fmt.Errorf("%w: character=%s", ErrCharacterNotFound, characterID)
		}
		if strings.TrimSpace(character.CharacterID) != characterID ||
			(accountID != "" && strings.TrimSpace(character.AccountID) != accountID) {
			return fmt.Errorf("%w: account=%q character=%s", ErrOwnerMismatch, accountID, characterID)
		}
		growType, err := eligibleGrowType(character)
		if err != nil {
			return err
		}
		before, err := LoadDeck(character)
		if err != nil {
			return err
		}
		after, err := change(before)
		if err != nil {
			return err
		}
		resolutions, err := o.validateDeck(ctx, after, growType)
		if err != nil {
			return err
		}
		// Quest completion is monotonic. When any catalog row is quest-gated,
		// read its authorization snapshot while the character-asset UOW is open,
		// before either deck Stats or slot24 is saved. Level-only rows do not
		// require a quest repository or record.
		if err := o.validateUnlocks(ctx, character, resolutions); err != nil {
			return err
		}

		worn, found, err := equipment.Load(ctx, characterID)
		if err != nil {
			return fmt.Errorf("load guardian shield equipment: %w", err)
		}
		if !found {
			worn = dnfrepo.EquipmentRecord{CharacterID: characterID}
		}
		worn = dnfrepo.CloneEquipment(worn)
		worn.CharacterID = characterID
		if worn.Entries == nil {
			worn.Entries = make(map[string]dnfrepo.EquipmentEntry)
		}
		projectionChanged, err := applyMainProjection(&worn, after[MainDeckSlot], resolutions[MainDeckSlot])
		if err != nil {
			return err
		}

		changed := before != after
		mainChanged := before[MainDeckSlot] != after[MainDeckSlot]
		if changed {
			character = dnfrepo.CloneCharacter(character)
			if character.Stats == nil {
				character.Stats = make(map[string]int64, DeckSlotCount)
			}
			for index, key := range deckStatKeys {
				character.Stats[key] = int64(after[index])
			}
			character.UpdatedAt = time.Now()
			if err := dnfrepo.SaveCharacterFields(ctx, characters, character, dnfrepo.CharacterFieldStats); err != nil {
				return fmt.Errorf("save guardian shield deck: %w", err)
			}
		}
		if projectionChanged {
			worn.UpdatedAt = time.Now()
			if err := dnfrepo.SaveEquipmentFields(ctx, equipment, worn, dnfrepo.EquipmentFieldEntries); err != nil {
				return fmt.Errorf("save guardian shield main projection: %w", err)
			}
		}
		result = Result{Deck: after, Changed: changed, MainChanged: mainChanged || projectionChanged}
		return nil
	})
	return result, err
}

func (o *Owner) validateUnlocks(
	ctx context.Context,
	character dnfrepo.CharacterRecord,
	resolutions [DeckSlotCount]alignedcmd.GuardianShieldResolution,
) error {
	needsQuest := false
	for _, resolution := range resolutions {
		if resolution.ItemID != 0 && resolution.UnlockKind == alignedcmd.GuardianShieldUnlockQuest {
			needsQuest = true
			break
		}
	}
	var record dnfrepo.QuestRecord
	if needsQuest {
		if o == nil || o.quests == nil {
			return ErrQuestRepositoryRequired
		}
		characterID := strings.TrimSpace(character.CharacterID)
		var found bool
		var err error
		record, found, err = o.quests.Load(ctx, characterID)
		if err != nil {
			return fmt.Errorf("load guardian shield clear quests: %w", err)
		}
		if !found {
			record = dnfrepo.QuestRecord{CharacterID: characterID}
		}
		if strings.TrimSpace(record.CharacterID) != characterID {
			return fmt.Errorf("%w: quest character=%q selected=%q", ErrOwnerMismatch, record.CharacterID, characterID)
		}
	}
	for slot, resolution := range resolutions {
		if resolution.ItemID == 0 {
			continue
		}
		if err := ValidateUnlock(character, record, resolution); err != nil {
			return fmt.Errorf("validate guardian shield slot=%d item=%d unlock: %w", slot, resolution.ItemID, err)
		}
	}
	return nil
}

// ValidateUnlock applies one shieldwindownewdata authorization to the selected
// character. The UI catalog is not an authorization boundary: forged list34,
// op19 and op657 packets must still satisfy either the PVF quest or level gate.
func ValidateUnlock(
	character dnfrepo.CharacterRecord,
	record dnfrepo.QuestRecord,
	resolution alignedcmd.GuardianShieldResolution,
) error {
	switch resolution.UnlockKind {
	case alignedcmd.GuardianShieldUnlockQuest:
		if resolution.ClearQuestID <= 0 {
			return fmt.Errorf("%w: item=%d clear_quest=%d", ErrClearQuestRequired, resolution.ItemID, resolution.ClearQuestID)
		}
		if !dnfquest.IsCompleted(record, resolution.ClearQuestID) {
			state, statesFound := record.States[resolution.ClearQuestID]
			progress, progressFound := record.Progress[resolution.ClearQuestID]
			return fmt.Errorf(
				"%w: item=%d clear_quest=%d states_status=%q states_present=%t progress_status=%q progress_present=%t",
				ErrClearQuestIncomplete,
				resolution.ItemID,
				resolution.ClearQuestID,
				state.Status,
				statesFound,
				progress.Status,
				progressFound,
			)
		}
		return nil
	case alignedcmd.GuardianShieldUnlockLevel:
		if resolution.MinimumLevel <= 0 {
			return fmt.Errorf("%w: item=%d minimum_level=%d", ErrMinimumLevelRequired, resolution.ItemID, resolution.MinimumLevel)
		}
		if int64(character.Level) < resolution.MinimumLevel {
			return fmt.Errorf(
				"%w: item=%d character_level=%d minimum_level=%d",
				ErrLevelInsufficient,
				resolution.ItemID,
				character.Level,
				resolution.MinimumLevel,
			)
		}
		return nil
	default:
		return fmt.Errorf("%w: item=%d kind=%d", ErrUnlockKindInvalid, resolution.ItemID, resolution.UnlockKind)
	}
}

func (o *Owner) validateDeck(ctx context.Context, deck Deck, growType byte) ([DeckSlotCount]alignedcmd.GuardianShieldResolution, error) {
	var resolutions [DeckSlotCount]alignedcmd.GuardianShieldResolution
	seen := make(map[uint32]int, DeckSlotCount)
	for index, itemID := range deck {
		if itemID == 0 {
			continue
		}
		if previous, exists := seen[itemID]; exists {
			return resolutions, fmt.Errorf("%w: item=%d slots=%d,%d", ErrDeckDuplicate, itemID, previous, index)
		}
		seen[itemID] = index
		resolution, err := o.resolver(ctx, itemID, growType)
		if err != nil {
			return resolutions, fmt.Errorf("validate guardian shield slot=%d item=%d: %w", index, itemID, err)
		}
		if resolution.ItemID != itemID {
			return resolutions, fmt.Errorf("validate guardian shield slot=%d item=%d resolved=%d", index, itemID, resolution.ItemID)
		}
		resolutions[index] = resolution
	}
	return resolutions, nil
}

func eligibleGrowType(character dnfrepo.CharacterRecord) (byte, error) {
	job, err := strconv.ParseInt(strings.TrimSpace(character.Job), 10, 16)
	if err != nil || job != guardianJob {
		return 0, fmt.Errorf("%w: job=%q", ErrCharacterIneligible, character.Job)
	}
	growValue, found := character.Stats["grow_type"]
	if !found || growValue < 0 || growValue > 0xff {
		return 0, fmt.Errorf("%w: grow_type=%d present=%t", ErrCharacterIneligible, growValue, found)
	}
	growType := byte(growValue) & 0x0f
	if growType != 1 && growType != 2 {
		return 0, fmt.Errorf("%w: grow_type=%d normalized=%d", ErrCharacterIneligible, growValue, growType)
	}
	return growType, nil
}

func validDeckSlot(slot int16) bool {
	return slot >= 0 && slot < DeckSlotCount
}

// MainProjectionItemID returns the Guardian-owned visible shield in actor slot
// 24. A generic support-weapon or another profession's offhand must not be
// mistaken for this projection: only the row written by this owner's atomic
// deck transaction carries projectionMarker.
func MainProjectionItemID(record dnfrepo.EquipmentRecord) (uint32, bool) {
	entry, found := record.Entries[strconv.Itoa(int(ActorSlot))]
	if !found || entry.SlotIndex != ActorSlot || entry.ItemID <= 0 ||
		entry.ItemID > int64(^uint32(0)) || entry.Extra[projectionMarker] != "1" {
		return 0, false
	}
	return uint32(entry.ItemID), true
}

func applyMainProjection(record *dnfrepo.EquipmentRecord, itemID uint32, resolution alignedcmd.GuardianShieldResolution) (bool, error) {
	key := strconv.Itoa(int(ActorSlot))
	current, occupied := record.Entries[key]
	if itemID == 0 {
		if occupied && current.Extra[projectionMarker] == "1" {
			delete(record.Entries, key)
			return true, nil
		}
		return false, nil
	}
	if occupied && current.ItemID != int64(itemID) && current.Extra[projectionMarker] != "1" {
		return false, fmt.Errorf("%w: existing=%d requested=%d", ErrProjectionConflict, current.ItemID, itemID)
	}
	if occupied && current.ItemID == int64(itemID) && current.Extra[projectionMarker] == "1" {
		return false, nil
	}
	record.Entries[key] = BuildMainProjectionEntry(itemID, resolution)
	return true, nil
}

// DeckStatKey returns the character Stats key that persists deck slot's item.
func DeckStatKey(slot int) string {
	if slot < 0 || slot >= DeckSlotCount {
		slot = 0
	}
	return deckStatKeys[slot]
}

// IsMainProjectionEntry reports whether the equipment row is the Guardian
// deck's actor-slot-24 projection. The worn slot is also used by the Blade
// Dancer off-hand weapon, so callers that render a projection-specific visual
// (for example the mode0 scene-object appearance table) must gate on this
// marker instead of the bare slot number.
func IsMainProjectionEntry(entry dnfrepo.EquipmentEntry) bool {
	if entry.SlotIndex != ActorSlot {
		return false
	}
	return strings.TrimSpace(entry.Extra[projectionMarker]) == "1"
}

// BuildMainProjectionEntry constructs the durable actor-slot-24 projection
// record for the deck's visible main shield. It is the same record the atomic
// deck transaction persists, exported so the bridge login pre-sync can repair
// legacy guardians whose deck[0] never reached the equipment table (or whose
// equipment row was left behind after a deck reset) without an active command
// owner. SlotIndex, the minimal raw entry and current_exe_equipment_type=24
// keep the row visible in the list3 body wire and the mode0 scene-object
// appearance table; projectionMarker distinguishes it from a generic
// support-weapon or another profession's slot-24 offhand.
func BuildMainProjectionEntry(itemID uint32, resolution alignedcmd.GuardianShieldResolution) dnfrepo.EquipmentEntry {
	raw := make([]byte, currentRawItemEntrySize)
	binary.LittleEndian.PutUint16(raw[0:2], uint16(ActorSlot))
	binary.LittleEndian.PutUint32(raw[2:6], itemID)
	binary.LittleEndian.PutUint32(raw[6:10], itemquality.TopSeed)
	return dnfrepo.EquipmentEntry{
		SlotIndex: ActorSlot,
		ItemID:    int64(itemID),
		RawEntry:  raw,
		Extra: map[string]string{
			projectionMarker:             "1",
			"source":                     "guardian_shield_deck",
			"item_kind":                  "equipment",
			"pvf_path":                   strings.TrimSpace(resolution.PVFPath),
			"equipment_type":             strings.TrimSpace(resolution.EquipmentType),
			"current_exe_equipment_type": strconv.Itoa(int(ActorSlot)),
			"current_exe_runtime_move":   "1",
			"quality_seed":               strconv.FormatUint(uint64(itemquality.TopSeed), 10),
		},
	}
}
