package epicbook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const ShopConfigPath = "etc/epicpieceshop.etc"

var ErrShop = errors.New("epic piece shop unavailable or invalid")
var ErrShopGold = errors.New("epic piece shop insufficient gold")
var ErrShopUsed = errors.New("epic piece shop exchange already completed")

type ShopConfig struct {
	NPC, Provide, Received          uint32
	AppearancePercent, RefreshLimit int
	RefreshGold                     int64
}

func LoadShopConfig(source pvf.Source) (ShopConfig, error) {
	if source == nil {
		return ShopConfig{}, ErrShop
	}
	text, err := source.ReadText(ShopConfigPath)
	if err != nil {
		return ShopConfig{}, err
	}
	doc, err := pvf.Parse(ShopConfigPath, text)
	if err != nil {
		return ShopConfig{}, err
	}
	read := func(tag string, low, high int64) (int64, error) {
		v, ok := doc.Int(tag)
		if !ok || v < low || v > high {
			return 0, fmt.Errorf("%w: [%s]", ErrShop, tag)
		}
		return v, nil
	}
	values := make([]int64, 9)
	tags := []string{"npcIndex", "epic piece provide", "epic piece received", "gabriel appearance probability", "list change chance", "list change cost", "kind of epic piece", "same type", "normal type"}
	limits := [][2]int64{{1, 2147483647}, {1, 2147483647}, {1, 2147483647}, {0, 100}, {0, 100}, {0, 2147483647}, {5, 5}, {0, 0}, {5, 5}}
	for i, tag := range tags {
		values[i], err = read(tag, limits[i][0], limits[i][1])
		if err != nil {
			return ShopConfig{}, err
		}
	}
	// The current native panel has five slots and a literal ten-piece material
	// label. Reject incompatible PVFs instead of charging an invisible amount.
	if values[2] != 10 {
		return ShopConfig{}, ErrShop
	}
	return ShopConfig{NPC: uint32(values[0]), Provide: uint32(values[1]), Received: uint32(values[2]), AppearancePercent: int(values[3]), RefreshLimit: int(values[4]), RefreshGold: values[5]}, nil
}

type ShopCommand struct {
	AccountID, CharacterID, Encounter string
	Config                            ShopConfig
	Target                            uint32
	Materials                         [5]uint32
	Refresh                           bool
	Now                               time.Time
}

type ShopResult struct {
	Gold     int64
	Balances map[uint32]uint32
}

// ApplyShop keeps every debit and credit in one account/character transaction.
// Encounter admission and quote ownership are held by the transport runtime.
// The last successful exchange receipt also prevents a repeated commit before
// the runtime is marked complete. It occupies one bounded key per character.
func ApplyShop(ctx context.Context, repos r.Group, command ShopCommand) (ShopResult, error) {
	unit, ok := repos.AccountAssets.(r.AccountCharacterLedgerAssetUnitOfWork)
	if !ok {
		return ShopResult{}, r.ErrEpicBookTransactionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(command.AccountID) == "" || strings.TrimSpace(command.CharacterID) == "" || command.Encounter == "" || len(command.Encounter) > 128 || command.Config.Provide == 0 || command.Config.Received == 0 || command.Config.RefreshGold < 0 {
		return ShopResult{}, ErrShop
	}
	if !command.Refresh {
		if command.Target == 0 {
			return ShopResult{}, ErrShop
		}
		seen := map[uint32]bool{command.Target: true}
		for _, id := range command.Materials {
			if id == 0 || seen[id] {
				return ShopResult{}, ErrShop
			}
			seen[id] = true
		}
	}
	now := command.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var result ShopResult
	err := unit.WithinAccountCharacterLedgerAssets(ctx, command.AccountID, command.CharacterID, func(accounts r.AccountRepository, _ r.AccountInventoryRepository, characters r.CharacterRepository, _ r.InventoryRepository, _ r.EquipmentRepository) error {
		a, found, err := accounts.Load(ctx, command.AccountID)
		if err != nil {
			return err
		}
		if !found || a.AccountID != command.AccountID {
			return r.ErrEpicBookOwnership
		}
		c, found, err := characters.Load(ctx, command.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != command.CharacterID || c.AccountID != command.AccountID {
			return r.ErrEpicBookOwnership
		}
		a = r.CloneAccount(a)
		c = r.CloneCharacter(c)
		if a.Metadata == nil {
			a.Metadata = map[string]string{}
		}
		if c.Stats == nil {
			c.Stats = map[string]int64{}
		}
		receipt := "epic_shop_exchange:" + command.CharacterID
		if a.Metadata[receipt] == command.Encounter {
			return ErrShopUsed
		}
		result.Gold = c.Stats["gold"]
		if result.Gold < 0 || result.Gold > 2147483647 {
			return ErrShopGold
		}
		if command.Refresh {
			if result.Gold < command.Config.RefreshGold {
				return ErrShopGold
			}
			result.Gold -= command.Config.RefreshGold
			c.Stats["gold"] = result.Gold
			c.UpdatedAt = now
			return r.SaveCharacterFields(ctx, characters, c, r.CharacterFieldStats)
		}
		result.Balances = make(map[uint32]uint32, 6)
		balance := r.ParseAccountEpicPieceBalance(a.Metadata[r.AccountEpicPieceMetadataKey(command.Target)])
		if command.Config.Provide > r.AccountEpicPieceBalanceMax-balance {
			return ErrMaterial
		}
		for _, id := range command.Materials {
			key := r.AccountEpicPieceMetadataKey(id)
			n := r.ParseAccountEpicPieceBalance(a.Metadata[key])
			if n < command.Config.Received {
				return ErrMaterial
			}
			n -= command.Config.Received
			a.Metadata[key] = r.FormatAccountEpicPieceBalance(n)
			result.Balances[id] = n
		}
		balance += command.Config.Provide
		a.Metadata[r.AccountEpicPieceMetadataKey(command.Target)] = r.FormatAccountEpicPieceBalance(balance)
		result.Balances[command.Target] = balance
		a.Metadata[receipt] = command.Encounter
		a.UpdatedAt = now
		return accounts.Save(ctx, a)
	})
	if err != nil {
		return ShopResult{}, err
	}
	return result, nil
}
