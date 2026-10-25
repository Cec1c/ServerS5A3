package guild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/pvf"
	r "longheng.io/server/internal/modules/dnf/repository"
)

const ContributionCatalogPath = "etc/chn_hongbao/chn_hongbao.etc"
const ContributionCoinID int64 = 490003262

var ErrContributionState = errors.New("invalid guild contribution state or rule")

type ContributionEvent string

const (
	ContributionAttendance ContributionEvent = "guild attendance"
	ContributionChat       ContributionEvent = "guild chat"
	ContributionPartyClear ContributionEvent = "party dungeon clear"
	ContributionGuildClear ContributionEvent = "guild dungeon clear"
)

type ContributionRule struct{ Threshold, Points uint32 }
type ContributionCatalog struct {
	rules map[ContributionEvent]ContributionRule
}

func LoadContributionCatalog(source pvf.Source) (*ContributionCatalog, error) {
	if source == nil {
		return nil, ErrRepositoryUnavailable
	}
	text, err := source.ReadText(ContributionCatalogPath)
	if err != nil {
		return nil, err
	}
	return ParseContributionCatalog(text)
}

func ParseContributionCatalog(text string) (*ContributionCatalog, error) {
	doc, err := pvf.Parse(ContributionCatalogPath, text)
	if err != nil {
		return nil, err
	}
	c := &ContributionCatalog{rules: map[ContributionEvent]ContributionRule{}}
	found := false
	for _, section := range doc.Sections {
		if strings.ToLower(strings.TrimSpace(section.Name)) != "hongbao point reward" {
			continue
		}
		if found {
			return nil, ErrContributionState
		}
		found = true
		tokens := doc.Tokens[section.Start:section.End]
		if len(tokens) != 12 {
			return nil, ErrContributionState
		}
		for i := 0; i < len(tokens); i += 3 {
			if tokens[i].Kind != pvf.TokenString {
				return nil, ErrContributionState
			}
			key := ContributionEvent(strings.TrimSpace(tokens[i].Value))
			switch key {
			case ContributionAttendance, ContributionChat, ContributionPartyClear, ContributionGuildClear:
			default:
				return nil, ErrContributionState
			}
			if _, exists := c.rules[key]; exists {
				return nil, ErrContributionState
			}
			a, b := tokens[i+1], tokens[i+2]
			if a.Kind != pvf.TokenInt || b.Kind != pvf.TokenInt || a.Int < 1 || a.Int > math.MaxInt32 || b.Int < 1 || b.Int > math.MaxInt32 {
				return nil, ErrContributionState
			}
			c.rules[key] = ContributionRule{uint32(a.Int), uint32(b.Int)}
		}
	}
	if !found || len(c.rules) != 4 || c.rules[ContributionAttendance].Threshold != 1 || c.rules[ContributionChat].Threshold != 1 || c.rules[ContributionPartyClear].Threshold < 2 || c.rules[ContributionPartyClear].Threshold > 4 {
		return nil, ErrContributionState
	}
	return c, nil
}

// Policy states the server's business-day rules separately from PVF amounts.
// Zero daily guild-clear awards means each threshold may earn again. Attendance
// and chat are once per character-day; party clears have no artificial cap.
type ContributionPolicy struct {
	GuildClearDailyAwards uint32
	SkipAttendanceJoinDay bool
}

type ContributionDelivery struct {
	Slot   int16
	MailID string
}
type ContributionIncome struct {
	GuildID  uint32
	Points   uint32
	Delivery ContributionDelivery
	Replayed bool
}

// Delivery runs only inside the caller's guild/character/mail transaction.
// It must either persist all coins or return an error; no nested transaction.
type ContributionDeliver func(context.Context, r.Group, *r.CharacterRecord, *r.InventoryRecord, uint32, string, time.Time) (ContributionDelivery, error)
type ContributionContract struct {
	Catalog *ContributionCatalog
	Policy  ContributionPolicy
	Deliver ContributionDeliver
}
type ContributionMembership struct {
	GuildID  uint32
	JoinedAt time.Time
}
type ContributionActivity struct {
	Event ContributionEvent
	// PartyMembers is frozen at the authoritative clear barrier, including self.
	PartyMembers []r.GuildOwner
	// FrozenGuilds records membership at the terminal barrier. Joining or
	// changing guild after the clear cannot make an old run eligible.
	FrozenGuilds  map[string]ContributionMembership
	CompletionKey string
}

const contributionDayEnd = "guild_contribution_day_end"
const contributionPrefix = "guild_contribution_"

// ApplyContribution runs after the durable completion check. All character
// counters are persisted by the caller with the registry, coins and receipt.
func ApplyContribution(ctx context.Context, tx r.Group, actor *r.CharacterRecord, inventory *r.InventoryRecord, contract *ContributionContract, activity ContributionActivity, now time.Time) (ContributionIncome, error) {
	if actor == nil || inventory == nil || inventory.CharacterID != actor.CharacterID || contract == nil || contract.Catalog == nil || contract.Deliver == nil || activity.CompletionKey == "" || now.IsZero() {
		return ContributionIncome{}, ErrContributionState
	}
	rule, exists := contract.Catalog.rules[activity.Event]
	if !exists {
		return ContributionIncome{}, ErrContributionState
	}
	reg, err := Load(ctx, tx.Settings)
	if err != nil {
		return ContributionIncome{}, err
	}
	owner := r.GuildOwner{AccountID: actor.AccountID, CharacterID: actor.CharacterID}
	g, member, found := reg.GuildForOwner(owner)
	if !found {
		return ContributionIncome{}, nil
	}
	result := ContributionIncome{GuildID: g.ID, Delivery: ContributionDelivery{Slot: -1}}
	end := itemexpiration.NextDailyDeleteAt(now).Unix()
	if activity.Event == ContributionAttendance && contract.Policy.SkipAttendanceJoinDay && (member.JoinedAt.IsZero() || itemexpiration.NextDailyDeleteAt(member.JoinedAt).Unix() >= end) {
		return result, nil
	}
	oldEnd := actor.Stats[contributionDayEnd]
	if oldEnd < 0 || oldEnd > end {
		return ContributionIncome{}, ErrContributionState
	}
	counts := make(map[ContributionEvent]int64, 3)
	for _, event := range []ContributionEvent{ContributionAttendance, ContributionChat, ContributionGuildClear} {
		v := actor.Stats[contributionPrefix+string(event)]
		if v < 0 || v > math.MaxInt32 || (oldEnd == 0 && v != 0) {
			return ContributionIncome{}, ErrContributionState
		}
		if event != ContributionGuildClear && v > 1 {
			return ContributionIncome{}, ErrContributionState
		}
		if oldEnd == end {
			counts[event] = v
		}
	}
	amount := rule.Points
	switch activity.Event {
	case ContributionAttendance, ContributionChat:
		if counts[activity.Event] != 0 {
			return result, nil
		}
		counts[activity.Event] = 1
	case ContributionGuildClear:
		count := counts[activity.Event]
		if count == math.MaxInt32 {
			return ContributionIncome{}, ErrContributionState
		}
		if contract.Policy.GuildClearDailyAwards != 0 && uint64(count) >= uint64(rule.Threshold)*uint64(contract.Policy.GuildClearDailyAwards) {
			return result, nil
		}
		count++
		counts[activity.Event] = count
		if count%int64(rule.Threshold) != 0 {
			amount = 0
		}
	case ContributionPartyClear:
		if len(activity.PartyMembers) < 2 || len(activity.PartyMembers) > 4 || len(activity.FrozenGuilds) != len(activity.PartyMembers) {
			return ContributionIncome{}, ErrContributionState
		}
		seen := map[string]bool{}
		sameGuild := uint32(0)
		self := false
		for _, peer := range activity.PartyMembers {
			if peer.CharacterID == "" || peer.AccountID == "" || seen[peer.CharacterID] {
				return ContributionIncome{}, ErrContributionState
			}
			seen[peer.CharacterID] = true
			self = self || peer == owner
			pg, pm, belongs := reg.GuildForOwner(peer)
			frozen := activity.FrozenGuilds[peer.CharacterID]
			if belongs && pg.ID == g.ID && frozen.GuildID == g.ID && frozen.JoinedAt.Equal(pm.JoinedAt) {
				sameGuild++
			}
		}
		if !self {
			return ContributionIncome{}, ErrContributionState
		}
		frozen := activity.FrozenGuilds[owner.CharacterID]
		if frozen.GuildID != g.ID || !frozen.JoinedAt.Equal(member.JoinedAt) {
			return result, nil
		}
		if sameGuild < rule.Threshold {
			return result, nil
		}
	}
	if actor.Stats == nil {
		actor.Stats = map[string]int64{}
	}
	actor.Stats[contributionDayEnd] = end
	for event, v := range counts {
		actor.Stats[contributionPrefix+string(event)] = v
	}
	if amount == 0 {
		return result, nil
	}
	if member.ContributionPoints > math.MaxUint32-amount {
		return ContributionIncome{}, ErrMileageOverflow
	}
	delivery, err := contract.Deliver(ctx, tx, actor, inventory, amount, activity.CompletionKey, now)
	if err != nil {
		return ContributionIncome{}, err
	}
	member.ContributionPoints += amount
	g.Members[owner.CharacterID] = member
	g.UpdatedAt = now
	if err := Save(ctx, tx.Settings, reg, now); err != nil {
		return ContributionIncome{}, err
	}
	result.Points, result.Delivery = amount, delivery
	return result, nil
}

// Daily events have a stable character/day/event receipt as well as counters.
// Reconnecting, a lost response, and leave/rejoin all read the same outcome.
func GrantDailyContribution(ctx context.Context, uow r.GuildSettlementUnitOfWork, owner r.GuildOwner, contract *ContributionContract, event ContributionEvent, now time.Time, current func() bool) (ContributionIncome, error) {
	if uow == nil || current == nil || now.IsZero() || contract == nil || contract.Catalog == nil || contract.Deliver == nil || (event != ContributionAttendance && event != ContributionChat) {
		return ContributionIncome{}, ErrContributionState
	}
	key := fmt.Sprintf("contribution:v1:%s:%d", event, itemexpiration.NextDailyDeleteAt(now).Unix())
	scope := r.GuildSettlementReceiptScope(owner, key)
	var result ContributionIncome
	err := uow.WithinGuildSettlement(ctx, owner, key, func(tx r.Group) error {
		if !current() {
			return ErrCharacterNotFound
		}
		receipt, exists, err := tx.Settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		if exists {
			if err := json.Unmarshal([]byte(receipt.Values["contribution"]), &result); err != nil {
				return ErrContributionState
			}
			if result.GuildID == 0 || result.Points != contract.Catalog.rules[event].Points || result.Points == 0 ||
				(result.Delivery.MailID == "" && result.Delivery.Slot < 0) || (result.Delivery.MailID != "" && result.Delivery.Slot != -1) {
				return ErrContributionState
			}
			result.Replayed = true
			return nil
		}
		actor, found, err := tx.Character.Load(ctx, owner.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.AccountID != owner.AccountID || actor.CharacterID != owner.CharacterID {
			return ErrCharacterNotFound
		}
		inventory, found, err := tx.Inventory.Load(ctx, owner.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			inventory = r.InventoryRecord{CharacterID: owner.CharacterID}
		}
		actor, inventory = r.CloneCharacter(actor), r.CloneInventory(inventory)
		result, err = ApplyContribution(ctx, tx, &actor, &inventory, contract, ContributionActivity{Event: event, CompletionKey: key}, now)
		if err != nil {
			return err
		}
		// No membership / joining-day reward does not consume today's event.
		if result.Points != 0 {
			actor.UpdatedAt = now
			if err := r.SaveCharacterFields(ctx, tx.Character, actor, r.CharacterFieldStats); err != nil {
				return err
			}
			value, err := json.Marshal(result)
			if err != nil {
				return err
			}
			if err := tx.Settings.Save(ctx, r.SettingsRecord{Scope: scope, Values: map[string]string{"contribution": string(value)}, UpdatedAt: now}); err != nil {
				return err
			}
		}
		if !current() {
			return ErrCharacterNotFound
		}
		return nil
	})
	if err != nil {
		return ContributionIncome{}, err
	}
	return result, nil
}
