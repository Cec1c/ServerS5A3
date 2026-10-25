package dungeon

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/adventuregroup"
	"longheng.io/server/internal/modules/dnf/experiencebuff"
	"longheng.io/server/internal/modules/dnf/guild"
	"longheng.io/server/internal/modules/dnf/honor"
	"longheng.io/server/internal/modules/dnf/onlineevent"
	"longheng.io/server/internal/modules/dnf/progression"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

const (
	settlementReceiptPrefix  = "dungeon_settlement_receipt_"
	settlementReceiptVersion = int64(1)
)

var ErrSettlementStateInvalid = errors.New("dungeon settlement persisted state is invalid")

type WeeklyClearContract struct {
	Catalog *onlineevent.WeeklyCatalog
	Season  onlineevent.WeeklySeason
}

type SettlementCommand struct {
	GuildRanking           *guild.RankingPartyClear
	GuildEssence           *guild.EssenceCatalog
	GuildEssenceRevalidate func() bool
	GuildDungeon           *GuildDungeonClear
	DespairClearedFloor    int
	DespairRewards         *DespairRewards
	GriefClearedFloor      int
	GriefRewards           *GriefRewards
	AccountID              string
	CharacterID            string
	CompletionKey          string
	Tables                 *progression.Tables
	Experience             uint32
	// ClearPotionBase is the clear EXP before rank, contracts and other bonuses.
	ClearPotionBase         uint32
	RecommendedDungeonClear bool
	// GuildOrdinaryClear is set only by the server after ordinary-mode and entry-level checks.
	GuildOrdinaryClear    bool
	LuckyStar             *LuckyStarPolicy
	MonthlyAttendance     bool
	WeeklyAttendance      *WeeklyClearContract
	AdventureRuntime      *adventuregroup.RuntimeConfig
	HonorExpertTables     *honor.Tables
	MaximumCharacterLevel int
	UpdatedAt             time.Time
}

type SettlementResult struct {
	GuildRanking      guild.RankingClearResult
	GuildEssence      guild.EssenceIncome
	GuildDungeon      guild.DungeonClearResult
	GuildDungeonItems []GuildDungeonGrantedItem
	GuildMileage      guild.DungeonMileageResult
	Character         dnfrepo.CharacterRecord
	Skill             dnfrepo.SkillRecord
	Inventory         dnfrepo.InventoryRecord
	ExperienceGain    uint32
	PotionBonus       uint32
	HonorExpertGain   uint32
	SPGain            int
	TPGain            int
	Replayed          bool
	LuckyStar         LuckyStarResult
	DespairItems      []DespairGrantedItem
	GriefItems        []GriefGrantedItem
}

// CommitSettlement atomically advances experience and the SP/TP ledger and
// writes an idempotency receipt tied to the accepted dungeon completion.
func (o *Owner) CommitSettlement(ctx context.Context, cmd SettlementCommand) (SettlementResult, error) {
	if o == nil || o.settlements == nil || strings.TrimSpace(cmd.CharacterID) == "" ||
		strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.CompletionKey) == "" ||
		cmd.Tables == nil || cmd.Experience == 0 {
		return SettlementResult{}, ErrOwnerUnavailable
	}
	if (cmd.LuckyStar != nil || cmd.GuildOrdinaryClear) && !cmd.RecommendedDungeonClear {
		return SettlementResult{}, ErrSettlementStateInvalid
	}
	if cmd.GriefClearedFloor < 0 || cmd.GriefClearedFloor > griefMaximumFloor ||
		(cmd.GriefClearedFloor != 0) != (cmd.GriefRewards != nil) ||
		(cmd.GriefClearedFloor != 0 && (cmd.DespairClearedFloor != 0 || cmd.DespairRewards != nil)) {
		return SettlementResult{}, ErrSettlementStateInvalid
	}
	if cmd.DespairRewards != nil && (cmd.DespairClearedFloor < 1 || cmd.DespairClearedFloor > 100) {
		return SettlementResult{}, ErrSettlementStateInvalid
	}
	ctx = contextOrBackground(ctx)
	if cmd.GuildEssence != nil && (cmd.GuildEssenceRevalidate == nil || (!cmd.RecommendedDungeonClear && cmd.GuildDungeon == nil) || (cmd.GuildDungeon != nil && (cmd.GuildDungeon.PartySize < 1 || cmd.GuildDungeon.PartySize > 4))) {
		return SettlementResult{}, ErrSettlementStateInvalid
	}
	if cmd.GuildDungeon != nil {
		entry := cmd.GuildDungeon.Entry
		if cmd.GuildOrdinaryClear || cmd.DespairRewards != nil || cmd.GriefRewards != nil ||
			entry.Owner.AccountID != cmd.AccountID || entry.Owner.CharacterID != cmd.CharacterID ||
			entry.GuildID == 0 || cmd.GuildDungeon.Catalog == nil || cmd.CompletionKey != entry.CompletionKey() {
			return SettlementResult{}, ErrSettlementStateInvalid
		}
	}
	wantHash := sha256.Sum256([]byte(cmd.CompletionKey))
	now := updatedAtOrNow(cmd.UpdatedAt)
	useGuildSettlement := cmd.GuildOrdinaryClear || cmd.GuildDungeon != nil || cmd.GuildRanking != nil || cmd.GuildEssence != nil

	var result SettlementResult
	within := func(apply func(dnfrepo.Group) error) error {
		return o.settlements.WithinCharacterSettlement(ctx, cmd.CharacterID, apply)
	}
	if useGuildSettlement {
		uow, ok := o.settlements.(dnfrepo.GuildSettlementUnitOfWork)
		if !ok {
			return SettlementResult{}, ErrOwnerUnavailable
		}
		within = func(apply func(dnfrepo.Group) error) error {
			return uow.WithinGuildSettlement(ctx, dnfrepo.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}, cmd.CompletionKey, apply)
		}
	}
	err := within(func(tx dnfrepo.Group) error {
		if cmd.GuildEssence != nil && !cmd.GuildEssenceRevalidate() {
			return ErrSettlementStateInvalid
		}
		character, found, err := tx.Character.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || character.CharacterID != cmd.CharacterID ||
			strings.TrimSpace(character.AccountID) != strings.TrimSpace(cmd.AccountID) {
			return ErrSettlementStateInvalid
		}
		skill, found, err := tx.Skill.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || skill.CharacterID != cmd.CharacterID {
			return ErrSettlementStateInvalid
		}
		inventory, found, err := tx.Inventory.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != cmd.CharacterID {
			return ErrSettlementStateInvalid
		}
		character = dnfrepo.CloneCharacter(character)
		skill = dnfrepo.CloneSkill(skill)
		inventory = dnfrepo.CloneInventory(inventory)
		if useGuildSettlement {
			result = SettlementResult{Character: character, Skill: skill, Inventory: inventory}
			found, err := readGuildSettlementReceipt(ctx, tx.Settings, cmd, &result)
			if err != nil || found {
				return err
			}
		}
		if settlementReceiptMatches(character.Stats, wantHash) {
			if cmd.GuildDungeon != nil {
				return ErrSettlementStateInvalid
			}
			griefItems, err := readGriefRewardReceipt(character.Stats, cmd.GriefClearedFloor, cmd.GriefRewards != nil)
			if err != nil {
				return err
			}
			despairItems, err := readDespairRewardReceipt(character.Stats, cmd.DespairClearedFloor, cmd.DespairRewards != nil)
			if err != nil {
				return err
			}
			receiptGain, receiptSP, err := settlementReceiptValues(character, skill)
			if err != nil {
				return err
			}
			lucky, err := readSettlementLuckyStarReceipt(character.Stats)
			if err != nil {
				return err
			}
			result = SettlementResult{
				DespairItems: despairItems,
				GriefItems:   griefItems,
				LuckyStar:    lucky,
				Character:    character, Skill: skill, Inventory: inventory,
				ExperienceGain: receiptGain, PotionBonus: uint32(character.Stats[settlementReceiptPrefix+"potion_bonus"]), SPGain: receiptSP, Replayed: true,
			}
			// A pre-upgrade settlement is not a new clear: migrate its receipt without a retroactive award.
			if useGuildSettlement {
				return saveGuildSettlementReceipt(ctx, tx.Settings, cmd, result, now)
			}
			return nil
		}
		if cmd.DespairClearedFloor != 0 {
			if err := ApplyDespairClear(character.Stats, cmd.DespairClearedFloor); err != nil {
				return err
			}
		}
		if cmd.GriefClearedFloor != 0 {
			if err := ApplyGriefClear(character.Stats, cmd.GriefClearedFloor); err != nil {
				return err
			}
		}
		// 仅新完成的第100层计一次；位于既有结算回执重放检查之后，
		// 与经验、物品及楼层进度同事务提交，任一步失败均不保存计数。
		for _, tower := range []struct {
			floor int
			key   string
		}{{cmd.DespairClearedFloor, adventuregroup.DespairHundredClearStatKey}, {cmd.GriefClearedFloor, adventuregroup.GriefHundredClearStatKey}} {
			if tower.floor != 100 {
				continue
			}
			count := character.Stats[tower.key]
			if count < 0 {
				return ErrSettlementStateInvalid
			}
			if count < math.MaxUint32 {
				character.Stats[tower.key] = count + 1
			}
		}
		potionBonus := experiencebuff.Bonus(character, experiencebuff.Clear, cmd.ClearPotionBase, now)
		potionBonus = min(potionBonus, math.MaxUint32-cmd.Experience)
		gain := cmd.Experience + potionBonus
		experience, present := character.Stats["exp"]
		if !present || experience < 0 || uint64(experience) > math.MaxUint32 ||
			character.Level <= 0 || skill.Points.SyncedLevel != character.Level {
			return ErrSettlementStateInvalid
		}
		maximumLevel := cmd.MaximumCharacterLevel
		if maximumLevel <= 0 {
			maximumLevel = math.MaxInt
		}
		planned, err := PlanSettlementProgressionAtCap(
			cmd.Tables,
			character.Level,
			uint32(experience),
			gain,
			skill.Points,
			maximumLevel,
		)
		if err != nil {
			return err
		}
		maximumLevelExperience := maximumLevelExperienceGain(
			character.Level,
			uint32(experience),
			gain,
			cmd.MaximumCharacterLevel,
			cmd.Tables,
		)
		if err := progression.ApplyCharacterExperience(&character, &skill, planned.Experience.NewLevel, planned.Experience.NewExperience, planned.SkillPoints.New, now); err != nil {
			return err
		}
		appliedHonorExpertGain := uint32(0)
		if maximumLevelExperience > 0 && cmd.HonorExpertTables != nil {
			currentExpert, err := honor.ExpertProgressFromStats(character.Stats)
			if err != nil {
				return err
			}
			nextExpert, err := cmd.HonorExpertTables.AdvanceExpert(currentExpert, uint64(maximumLevelExperience))
			if err != nil {
				return err
			}
			expertStats, err := honor.ExpertStats(nextExpert)
			if err != nil {
				return err
			}
			for key, value := range expertStats {
				character.Stats[key] = value
			}
			appliedHonorExpertGain = maximumLevelExperience
		}
		if cmd.RecommendedDungeonClear {
			count := character.Stats[adventuregroup.RecommendedDungeonClearStatKey]
			if count < 0 {
				return ErrSettlementStateInvalid
			}
			if count < math.MaxUint32 {
				character.Stats[adventuregroup.RecommendedDungeonClearStatKey] = count + 1
			}
		}
		lucky, err := commitSettlementLuckyStar(ctx, tx, &character, cmd.LuckyStar, now)
		if err != nil {
			return err
		}
		despairItems, err := commitDespairRewards(ctx, o.settings, tx, &character, &inventory, cmd.DespairClearedFloor, cmd.DespairRewards, now)
		if err != nil {
			return err
		}
		griefItems, err := commitGriefRewards(ctx, o.settings, tx, &character, &inventory, cmd.GriefClearedFloor, cmd.GriefRewards, now)
		if err != nil {
			return err
		}
		guildClear, guildItems, err := commitGuildDungeonClear(ctx, o.settings, tx, &character, &inventory, cmd.GuildDungeon, now)
		if err != nil {
			return err
		}
		writeSettlementReceipt(
			character.Stats,
			wantHash,
			gain,
			planned.SkillPoints.SPGain,
			planned.Experience.NewLevel,
			planned.Experience.NewExperience,
		)
		character.Stats[settlementReceiptPrefix+"potion_bonus"] = int64(potionBonus)
		// This is inside the accepted completion's existing receipt/UOW. A
		// replay returned above and cannot increment an account-day twice.
		if cmd.MonthlyAttendance && cmd.RecommendedDungeonClear {
			if tx.Account == nil {
				return ErrSettlementStateInvalid
			}
			account, found, err := tx.Account.Load(ctx, cmd.AccountID)
			if err != nil {
				return err
			}
			if !found || account.AccountID != cmd.AccountID {
				return ErrSettlementStateInvalid
			}
			account = dnfrepo.CloneAccount(account)
			if err := onlineevent.RecordMonthlyClear(&account, now); err != nil {
				return err
			}
			if err := dnfrepo.SaveAccountMetadataEntry(ctx, tx.Account, account, onlineevent.MonthlyMetadataKey, account.Metadata[onlineevent.MonthlyMetadataKey], now); err != nil {
				return err
			}
		}
		if cmd.WeeklyAttendance != nil && cmd.RecommendedDungeonClear {
			if tx.Account == nil {
				return ErrSettlementStateInvalid
			}
			account, found, err := tx.Account.Load(ctx, cmd.AccountID)
			if err != nil {
				return err
			}
			if !found || account.AccountID != cmd.AccountID {
				return ErrSettlementStateInvalid
			}
			account = dnfrepo.CloneAccount(account)
			contract := cmd.WeeklyAttendance
			if err := onlineevent.RecordWeeklyClear(&account, contract.Catalog, contract.Season, now); err != nil {
				return err
			}
			key := contract.Season.Key()
			if raw := account.Metadata[key]; raw != "" {
				if err := dnfrepo.SaveAccountMetadataEntry(ctx, tx.Account, account, key, raw, now); err != nil {
					return err
				}
			}
		}
		var essence guild.EssenceIncome
		if cmd.GuildEssence != nil {
			event := guild.EssenceRecommendedClear
			if cmd.GuildDungeon != nil {
				event = guild.EssenceGuildClear
				if cmd.GuildDungeon.PartySize > 1 {
					event = guild.EssencePartyGuildClear
				}
			}
			essence, err = guild.ApplyEssenceIncome(ctx, tx.Settings, &character, cmd.GuildEssence, event, now)
			if err != nil {
				return err
			}
		}
		character.UpdatedAt = now
		skill.Points = planned.SkillPoints.New
		skill.UpdatedAt = now
		if err := dnfrepo.SaveCharacterFields(ctx, tx.Character, character, dnfrepo.CharacterFieldBase, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		if err := dnfrepo.SaveSkillFields(ctx, tx.Skill, skill, dnfrepo.SkillFieldPoints); err != nil {
			return err
		}
		if maximumLevelExperience > 0 && cmd.AdventureRuntime != nil {
			if tx.Account == nil {
				return ErrSettlementStateInvalid
			}
			account, accountFound, err := tx.Account.Load(ctx, cmd.AccountID)
			if err != nil {
				return err
			}
			if !accountFound || strings.TrimSpace(account.AccountID) != strings.TrimSpace(cmd.AccountID) {
				return ErrSettlementStateInvalid
			}
			account = dnfrepo.CloneAccount(account)
			runtimeState, err := adventuregroup.ParseRuntimeState(account, *cmd.AdventureRuntime, now)
			if err != nil {
				return err
			}
			runtimeState.AddMaximumLevelExperience(*cmd.AdventureRuntime, maximumLevelExperience)
			encoded, err := runtimeState.Marshal()
			if err != nil {
				return err
			}
			if account.Metadata == nil {
				account.Metadata = make(map[string]string)
			}
			account.Metadata[adventuregroup.RuntimeStateMetadataKey] = encoded
			if account.HonorExp > math.MaxUint64-uint64(maximumLevelExperience) {
				account.HonorExp = math.MaxUint64
			} else {
				account.HonorExp += uint64(maximumLevelExperience)
			}
			account.UpdatedAt = now
			if err := tx.Account.Save(ctx, account); err != nil {
				return err
			}
		}
		result = SettlementResult{
			GuildEssence: essence,
			GuildDungeon: guildClear, GuildDungeonItems: guildItems,
			DespairItems: despairItems,
			GriefItems:   griefItems,
			LuckyStar:    lucky,
			Character:    character, Skill: skill, Inventory: inventory,
			ExperienceGain:  gain,
			PotionBonus:     potionBonus,
			HonorExpertGain: appliedHonorExpertGain,
			SPGain:          planned.SkillPoints.SPGain,
			TPGain:          planned.SkillPoints.TPGain,
		}
		if cmd.GuildOrdinaryClear {
			mileage, err := guild.GrantDungeonMileage(ctx, tx.Settings, dnfrepo.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}, now)
			if err != nil {
				return err
			}
			result.GuildMileage = mileage
		}
		if cmd.GuildRanking != nil {
			ranking, err := guild.ApplyRankingPartyClear(ctx, tx.Settings, dnfrepo.GuildOwner{AccountID: cmd.AccountID, CharacterID: cmd.CharacterID}, *cmd.GuildRanking, now)
			if err != nil {
				return err
			}
			result.GuildRanking = ranking
		}
		if useGuildSettlement {
			if err := saveGuildSettlementReceipt(ctx, tx.Settings, cmd, result, now); err != nil {
				return err
			}
		}
		if cmd.GuildEssence != nil && !cmd.GuildEssenceRevalidate() {
			return ErrSettlementStateInvalid
		}
		return nil
	})
	if err != nil {
		return SettlementResult{}, err
	}
	return result, nil
}

func maximumLevelExperienceGain(
	previousLevel int,
	previousExperience uint32,
	gain uint32,
	maximumLevel int,
	tables *progression.Tables,
) uint32 {
	if maximumLevel <= 0 || previousLevel <= 0 || previousLevel > maximumLevel || gain == 0 || tables == nil {
		return 0
	}
	if previousLevel >= maximumLevel {
		return gain
	}
	entryExperience, err := tables.ThresholdToNext(maximumLevel - 1)
	if err != nil {
		return 0
	}
	total := uint64(previousExperience) + uint64(gain)
	if total <= uint64(entryExperience) {
		return 0
	}
	excess := total - uint64(entryExperience)
	if excess > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(excess)
}

func PlanSettlementProgression(
	tables *progression.Tables,
	level int,
	total uint32,
	gain uint32,
	points dnfrepo.SkillPointState,
) (progression.ExperienceSkillPointPlan, error) {
	return PlanSettlementProgressionAtCap(tables, level, total, gain, points, math.MaxInt)
}

func PlanSettlementProgressionAtCap(
	tables *progression.Tables,
	level int,
	total uint32,
	gain uint32,
	points dnfrepo.SkillPointState,
	maximumLevel int,
) (progression.ExperienceSkillPointPlan, error) {
	if tables == nil || level <= 0 || points.SyncedLevel != level || maximumLevel <= 0 || level > maximumLevel {
		return progression.ExperienceSkillPointPlan{}, ErrSettlementStateInvalid
	}
	newTotal := uint64(total) + uint64(gain)
	if newTotal > math.MaxUint32 {
		newTotal = math.MaxUint32
	}
	result := progression.ExperienceResult{
		PreviousLevel:      level,
		PreviousExperience: total,
		Gain:               gain,
		NewLevel:           level,
		NewExperience:      uint32(newTotal),
	}
	for result.NewLevel < maximumLevel {
		threshold, err := tables.ThresholdToNext(result.NewLevel)
		if err != nil {
			return progression.ExperienceSkillPointPlan{}, err
		}
		if result.NewExperience < threshold {
			break
		}
		if _, ok := tables.SkillPointsAtLevel(result.NewLevel + 1); !ok {
			return progression.ExperienceSkillPointPlan{}, ErrSettlementStateInvalid
		}
		result.NewLevel++
	}
	result.LevelsGained = result.NewLevel - result.PreviousLevel
	advance, err := tables.AdvanceSkillPoints(points, result.NewLevel)
	if err != nil {
		return progression.ExperienceSkillPointPlan{}, err
	}
	return progression.ExperienceSkillPointPlan{Experience: result, SkillPoints: advance}, nil
}

func settlementReceiptMatches(stats map[string]int64, want [sha256.Size]byte) bool {
	if stats == nil || stats[settlementReceiptPrefix+"version"] != settlementReceiptVersion {
		return false
	}
	for index := 0; index < 4; index++ {
		if uint64(stats[fmt.Sprintf("%shash_%d", settlementReceiptPrefix, index)]) !=
			binary.LittleEndian.Uint64(want[index*8:(index+1)*8]) {
			return false
		}
	}
	return true
}

func writeSettlementReceipt(
	stats map[string]int64,
	hash [sha256.Size]byte,
	gain uint32,
	spGain int,
	newLevel int,
	newExperience uint32,
) {
	stats[settlementReceiptPrefix+"version"] = settlementReceiptVersion
	for index := 0; index < 4; index++ {
		stats[fmt.Sprintf("%shash_%d", settlementReceiptPrefix, index)] =
			int64(binary.LittleEndian.Uint64(hash[index*8 : (index+1)*8]))
	}
	stats[settlementReceiptPrefix+"experience_gain"] = int64(gain)
	stats[settlementReceiptPrefix+"sp_gain"] = int64(spGain)
	stats[settlementReceiptPrefix+"new_level"] = int64(newLevel)
	stats[settlementReceiptPrefix+"new_experience"] = int64(newExperience)
}

func settlementReceiptValues(
	character dnfrepo.CharacterRecord,
	skill dnfrepo.SkillRecord,
) (uint32, int, error) {
	stats := character.Stats
	gain := stats[settlementReceiptPrefix+"experience_gain"]
	spGain := stats[settlementReceiptPrefix+"sp_gain"]
	if stats[settlementReceiptPrefix+"potion_bonus"] < 0 || stats[settlementReceiptPrefix+"potion_bonus"] > gain || gain <= 0 || gain > math.MaxUint32 || spGain < 0 || spGain > math.MaxInt ||
		stats[settlementReceiptPrefix+"new_level"] != int64(character.Level) ||
		stats[settlementReceiptPrefix+"new_experience"] != stats["exp"] ||
		skill.Points.SyncedLevel != character.Level {
		return 0, 0, ErrSettlementStateInvalid
	}
	return uint32(gain), int(spGain), nil
}
