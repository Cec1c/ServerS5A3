package dungeon

import (
	"context"
	"encoding/json"
	"time"

	"longheng.io/server/internal/modules/dnf/guild"
	r "longheng.io/server/internal/modules/dnf/repository"
)

// One record per accepted completion, outside the shared guild JSON. Keeping
// only the latest hash (the old settlement format) cannot reject older replays.
// Receipts also record zero for nonmembers; joining later cannot claim that run.
type guildSettlementReceipt struct {
	GuildRanking      guild.RankingClearResult
	GuildDungeon      guild.DungeonClearResult
	GuildDungeonItems []GuildDungeonGrantedItem
	Version           int
	Owner             r.GuildOwner
	ExperienceGain    uint32
	PotionBonus       uint32
	HonorExpertGain   uint32
	SPGain, TPGain    int
	LuckyStar         LuckyStarResult
	GuildMileage      guild.DungeonMileageResult
}

func readGuildSettlementReceipt(ctx context.Context, settings r.SettingsRepository, cmd SettlementCommand, result *SettlementResult) (bool, error) {
	owner := r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}
	record, found, err := settings.Load(ctx, r.GuildSettlementReceiptScope(owner, cmd.CompletionKey))
	if err != nil || !found {
		return false, err
	}
	var receipt guildSettlementReceipt
	if json.Unmarshal([]byte(record.Values["receipt"]), &receipt) != nil || receipt.Version != 1 || receipt.Owner != owner || receipt.ExperienceGain == 0 || receipt.PotionBonus > receipt.ExperienceGain || receipt.SPGain < 0 || receipt.TPGain < 0 ||
		(receipt.GuildMileage.GuildID == 0 && receipt.GuildMileage.Amount != 0) ||
		(receipt.GuildMileage.GuildID != 0 && receipt.GuildMileage.Amount != guild.OrdinaryDungeonMileage) {
		return false, ErrSettlementStateInvalid
	}
	result.ExperienceGain, result.HonorExpertGain = receipt.ExperienceGain, receipt.HonorExpertGain
	if cmd.GuildDungeon != nil {
		e := cmd.GuildDungeon.Entry
		if receipt.GuildDungeon.GuildID != e.GuildID || receipt.GuildDungeon.DungeonID != e.DungeonID || receipt.GuildDungeon.CycleKey != e.Cycle.Key || receipt.GuildDungeon.Units > e.Cycle.Units {
			return false, ErrSettlementStateInvalid
		}
		for _, item := range receipt.GuildDungeonItems {
			if item.ItemID == 0 || item.Quantity == 0 || item.Slot < 3 {
				return false, ErrSettlementStateInvalid
			}
		}
	} else if receipt.GuildDungeon.GuildID != 0 {
		return false, ErrSettlementStateInvalid
	}
	if receipt.GuildRanking.GuildID == 0 {
		if receipt.GuildRanking.Points != 0 || !receipt.GuildRanking.Month.IsZero() {
			return false, ErrSettlementStateInvalid
		}
	} else if receipt.GuildRanking.Points <= 0 || (guild.RankingActivity{Month: receipt.GuildRanking.Month, PartyClear: receipt.GuildRanking.Points}).Validate() != nil {
		return false, ErrSettlementStateInvalid
	}
	result.GuildRanking = receipt.GuildRanking
	result.GuildDungeon, result.GuildDungeonItems = receipt.GuildDungeon, receipt.GuildDungeonItems
	result.PotionBonus = receipt.PotionBonus
	result.SPGain, result.TPGain = receipt.SPGain, receipt.TPGain
	result.LuckyStar, result.GuildMileage, result.Replayed = receipt.LuckyStar, receipt.GuildMileage, true
	return true, nil
}

func saveGuildSettlementReceipt(ctx context.Context, settings r.SettingsRepository, cmd SettlementCommand, result SettlementResult, now time.Time) error {
	owner := r.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}
	value, err := json.Marshal(guildSettlementReceipt{Version: 1, Owner: owner,
		GuildRanking: result.GuildRanking,
		GuildDungeon: result.GuildDungeon, GuildDungeonItems: result.GuildDungeonItems,
		ExperienceGain: result.ExperienceGain, PotionBonus: result.PotionBonus, HonorExpertGain: result.HonorExpertGain,
		SPGain: result.SPGain, TPGain: result.TPGain, LuckyStar: result.LuckyStar, GuildMileage: result.GuildMileage})
	if err != nil {
		return err
	}
	return settings.Save(ctx, r.SettingsRecord{Scope: r.GuildSettlementReceiptScope(owner, cmd.CompletionKey), Values: map[string]string{"receipt": string(value)}, UpdatedAt: now})
}
