package raid

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	ErrRewardOwnerUnavailable = errors.New("raid reward asset transaction unavailable")
	ErrRewardOwnership        = errors.New("raid reward character/account mismatch")
	ErrRewardReceipt          = errors.New("raid reward receipt unavailable or inconsistent")
	ErrRewardIneligible       = errors.New("raid reward minimum dungeon clear count not met")
)

// RewardAssets exposes the two inventories and optional transaction-scoped
// mailbox to the placement projector. The owner alone writes the receipt in
// character stats, in the same transaction.
type RewardAssets struct {
	AccountInventory *repo.AccountInventoryRecord
	Inventory        *repo.InventoryRecord
	Mailbox          repo.MailboxRepository
}

type RewardChanges struct {
	AccountInventory bool
	Inventory        bool
	Mailbox          bool
}

type RewardCommand struct {
	AccountID     string
	CharacterID   string
	InstanceID    string
	Mode          byte
	Phase         byte
	Kind          byte
	Card          byte
	MinimumClears uint32
	OwnedClears   uint32
	// SHA256 of the frozen, fully expanded reward including card and metadata.
	Fingerprint [32]byte
	CompletedAt time.Time
	Project     func(*RewardAssets) (RewardChanges, error)
}

type RewardResult struct{ Replayed bool }

type RewardOwner struct {
	settings repo.SettingsRepository
	assets   repo.AccountCharacterAssetUnitOfWork
}

func NewRewardOwner(group repo.Group) (*RewardOwner, error) {
	if group.AccountAssets == nil {
		return nil, ErrRewardOwnerUnavailable
	}
	return &RewardOwner{assets: group.AccountAssets, settings: group.Settings}, nil
}

// Grant receives only a server-frozen entitlement. It does not roll rewards,
// consume a bag container, infer a phase clear, or commit a weekly clear.
// Each character has one receipt per instance/phase/kind, independent of card;
// changing the card or outcome cannot obtain a second reward of that kind.
func (o *RewardOwner) Grant(ctx context.Context, cmd RewardCommand) (RewardResult, error) {
	if o == nil || o.assets == nil {
		return RewardResult{}, ErrRewardOwnerUnavailable
	}
	instance, err := hex.DecodeString(cmd.InstanceID)
	if err != nil || len(instance) != 16 || (cmd.Mode != 1 && cmd.Mode != 2) || cmd.Phase > 1 || cmd.Kind < 1 || cmd.Kind > 3 || cmd.Card > 19 ||
		strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.CharacterID) == "" ||
		cmd.AccountID != strings.TrimSpace(cmd.AccountID) || cmd.CharacterID != strings.TrimSpace(cmd.CharacterID) ||
		cmd.Fingerprint == [32]byte{} || cmd.CompletedAt.IsZero() || cmd.CompletedAt.Unix() <= 0 || cmd.Project == nil {
		return RewardResult{}, ErrRewardReceipt
	}
	if cmd.MinimumClears == 0 && !(cmd.Mode == 2 && cmd.Phase == 0) || cmd.OwnedClears < cmd.MinimumClears {
		return RewardResult{}, ErrRewardIneligible
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return RewardResult{}, err
	}
	marker := fmt.Sprintf("raid_reward_v1_%d_%s_%d_%d", cmd.Mode, hex.EncodeToString(instance), cmd.Phase, cmd.Kind)
	result := RewardResult{}
	apply := func(
		accounts repo.AccountInventoryRepository, chars repo.CharacterRepository,
		inventories repo.InventoryRepository, _ repo.EquipmentRepository, mailboxes repo.MailboxRepository,
	) error {
		if accounts == nil || chars == nil || inventories == nil {
			return ErrRewardOwnerUnavailable
		}
		character, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != cmd.CharacterID || character.AccountID != cmd.AccountID {
			return ErrRewardOwnership
		}
		stamp, marked := character.Stats[marker]
		card, hasCard := character.Stats[marker+"_card"]
		parts := [4]int64{}
		have := 0
		for i := range parts {
			var exists bool
			parts[i], exists = character.Stats[fmt.Sprintf("%s_hash%d", marker, i)]
			if exists {
				have++
			}
		}
		if marked || hasCard || have != 0 {
			if !marked || !hasCard || have != 4 || stamp <= 0 || stamp > cmd.CompletedAt.Unix() || card != int64(cmd.Card)+1 {
				return ErrRewardReceipt
			}
			for i, part := range parts {
				if uint64(part) != binary.LittleEndian.Uint64(cmd.Fingerprint[i*8:(i+1)*8]) {
					return ErrRewardReceipt
				}
			}
			result.Replayed = true
			return nil
		}
		inventory, found, err := inventories.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != cmd.CharacterID || inventory.Slots == nil {
			return ErrRewardOwnerUnavailable
		}
		account, _, err := accounts.Load(ctx, cmd.AccountID)
		if err != nil {
			return err
		}
		account = repo.CloneAccountInventory(account)
		account.AccountID = cmd.AccountID
		if account.Slots == nil {
			account.Slots = map[string]repo.ItemStack{}
		}
		if err := repo.BindInventoryContainerState(ctx, o.settings, &inventory); err != nil {
			return err
		}
		inventory = repo.CloneInventory(inventory)
		character = repo.CloneCharacter(character)
		changes, err := cmd.Project(&RewardAssets{AccountInventory: &account, Inventory: &inventory, Mailbox: mailboxes})
		if err != nil {
			return err
		}
		if changes.Mailbox && mailboxes == nil {
			return ErrRewardOwnerUnavailable
		}
		if !changes.Inventory && !changes.AccountInventory && !changes.Mailbox {
			return ErrRewardReceipt
		}
		if account.AccountID != cmd.AccountID || inventory.CharacterID != cmd.CharacterID || account.Slots == nil || inventory.Slots == nil {
			return ErrRewardOwnership
		}
		if character.Stats == nil {
			character.Stats = map[string]int64{}
		}
		character.Stats[marker] = cmd.CompletedAt.Unix()
		character.Stats[marker+"_card"] = int64(cmd.Card) + 1
		for i := 0; i < 4; i++ {
			character.Stats[fmt.Sprintf("%s_hash%d", marker, i)] = int64(binary.LittleEndian.Uint64(cmd.Fingerprint[i*8 : (i+1)*8]))
		}
		character.UpdatedAt = cmd.CompletedAt.UTC()
		if changes.Inventory {
			inventory.UpdatedAt = cmd.CompletedAt.UTC()
			if err := repo.SaveInventoryFields(ctx, inventories, inventory, repo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if changes.AccountInventory {
			account.UpdatedAt = cmd.CompletedAt.UTC()
			if err := accounts.Save(ctx, account); err != nil {
				return err
			}
		}
		return repo.SaveCharacterFields(ctx, chars, character, repo.CharacterFieldStats)
	}
	if withMail, ok := o.assets.(repo.AccountCharacterMailboxAssetUnitOfWork); ok {
		err = withMail.WithinAccountCharacterMailboxAssets(ctx, cmd.AccountID, cmd.CharacterID, apply)
	} else {
		// Existing custom UOWs still support normal inventory rewards. A full bag
		// cannot be acknowledged as mail unless this transaction supplies it.
		err = o.assets.WithinAccountCharacterAssets(ctx, cmd.AccountID, cmd.CharacterID, func(a repo.AccountInventoryRepository, c repo.CharacterRepository, i repo.InventoryRepository, e repo.EquipmentRepository) error {
			return apply(a, c, i, e, nil)
		})
	}
	if errors.Is(err, repo.ErrAccountCharacterMailboxOwnership) {
		return RewardResult{}, errors.Join(ErrRewardOwnership, err)
	}
	if err != nil {
		return RewardResult{}, err
	}
	return result, nil
}
