package onlineevent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	r "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strings"
	"time"
)

const cumulativeKeyPrefix = "online_event_cumulative_v1:"

type CumulativeStage struct {
	ID           string
	RequiredDays uint32
	Items        []ItemReward
}
type CumulativeDefinition struct {
	Daily  Definition
	Stages []CumulativeStage
}
type CumulativeSnapshot struct {
	EventID         string
	QualifiedDates  []string
	ClaimedStageIDs []string
}
type CumulativeClaimCommand struct {
	AccountID, CharacterID, StageID string
	Definition                      CumulativeDefinition
	ClaimedAt                       time.Time
	Allocate                        ItemAllocator
}
type CumulativeStatusCommand struct {
	AccountID, CharacterID string
	Definition             CumulativeDefinition
	ObservedAt             time.Time
}
type cumulativeReceipt struct {
	StageID        string          `json:"stage_id"`
	CharacterID    string          `json:"character_id"`
	DefinitionHash string          `json:"definition_hash"`
	RequiredDays   uint32          `json:"required_days"`
	Items          []CommittedItem `json:"items"`
	ClaimedAt      time.Time       `json:"claimed_at"`
}
type cumulativeLedger struct {
	Version           int                          `json:"version"`
	EventID           string                       `json:"event_id"`
	QualificationHash string                       `json:"qualification_hash"`
	QualifiedDates    []string                     `json:"qualified_dates"`
	Claims            map[string]cumulativeReceipt `json:"claims,omitempty"`
}

func definitionHash(value any) string {
	b, _ := json.Marshal(value)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func qualificationHash(d Definition) string {
	_, winterOffset := time.Date(2026, 1, 1, 0, 0, 0, 0, d.calendar()).Zone()
	_, summerOffset := time.Date(2026, 7, 1, 0, 0, 0, 0, d.calendar()).Zone()
	return definitionHash(struct {
		ID, StageID, Calendar      string
		WinterOffset, SummerOffset int
		Boundary                   DailyBoundary
		Stages                     []Stage
		From, Until                time.Time
	}{d.ID, d.QualificationStageID, d.calendar().String(), winterOffset, summerOffset, d.boundary(), d.Stages, d.ActiveFrom, d.ActiveUntil})
}
func (d CumulativeDefinition) Validate() error {
	if e := d.Daily.Validate(); e != nil {
		return e
	}
	if d.Daily.QualificationStageID == "" || len(d.Stages) == 0 {
		return ErrDefinitionInvalid
	}
	seen := map[string]bool{}
	var previous uint32
	for _, s := range d.Stages {
		if !validIdentifier(s.ID) || seen[s.ID] || s.RequiredDays == 0 || s.RequiredDays <= previous || len(s.Items) == 0 {
			return ErrDefinitionInvalid
		}
		seen[s.ID] = true
		previous = s.RequiredDays
		for _, i := range s.Items {
			if i.ItemID <= 0 || i.Count <= 0 {
				return ErrRewardInvalid
			}
		}
	}
	return nil
}
func (d CumulativeDefinition) stage(id string) (CumulativeStage, bool) {
	for _, s := range d.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return CumulativeStage{}, false
}
func cumulativeStageHash(d CumulativeDefinition, s CumulativeStage) string {
	return definitionHash(struct {
		Qualification string
		Stage         CumulativeStage
	}{qualificationHash(d.Daily), s})
}

// AttendanceCumulativeDefinition is the explicitly approved daily-completion
// design, not a claim that the EXE's raw PVF sum field means calendar days.
// The four real ordinary rewards must match the configured daily stages;
// every real stage must have a valid same-day committed receipt to qualify.
func AttendanceCumulativeDefinition(c *AttendancePVFCatalog, daily Definition) (CumulativeDefinition, error) {
	if err := daily.Validate(); err != nil {
		return CumulativeDefinition{}, err
	}
	if c == nil {
		return CumulativeDefinition{}, ErrDefinitionInvalid
	}
	raw := c.Snapshot()
	if len(daily.Stages) != len(raw.RewardItems) || len(raw.ProcessDurationsSeconds) != len(raw.RewardItems) || len(raw.SumThresholds) != len(raw.RewardItemsForSum) {
		return CumulativeDefinition{}, ErrDefinitionInvalid
	}
	var elapsed uint64
	for n, s := range daily.Stages {
		elapsed += uint64(raw.ProcessDurationsSeconds[n])
		reward := raw.RewardItems[n]
		if s.RequiredSeconds != elapsed || len(s.Items) != 1 || s.Items[0] != (ItemReward{ItemID: reward.ItemID, Count: reward.Count}) {
			return CumulativeDefinition{}, ErrDefinitionInvalid
		}
	}
	daily.QualificationStageID = daily.Stages[len(daily.Stages)-1].ID
	result := CumulativeDefinition{Daily: daily}
	for n, reward := range raw.RewardItemsForSum {
		days := raw.SumThresholds[n]
		if days <= 0 || days > int64(^uint32(0)) {
			return CumulativeDefinition{}, ErrDefinitionInvalid
		}
		result.Stages = append(result.Stages, CumulativeStage{ID: fmt.Sprintf("sum-%d", n+1), RequiredDays: uint32(days), Items: []ItemReward{{ItemID: reward.ItemID, Count: reward.Count}}})
	}
	return result, result.Validate()
}

func parseCumulativeLedger(raw string, d Definition) (cumulativeLedger, error) {
	result := cumulativeLedger{Version: 1, EventID: d.ID, QualificationHash: qualificationHash(d), Claims: map[string]cumulativeReceipt{}}
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&result); e != nil {
		return result, fmt.Errorf("%w: cumulative decode %v", ErrStateInvalid, e)
	}
	if e := rejectTrailingJSON(dec); e != nil {
		return result, e
	}
	if result.Version != 1 || result.EventID != d.ID || result.QualificationHash != qualificationHash(d) {
		return result, ErrStateInvalid
	}
	var previous string
	for _, date := range result.QualifiedDates {
		if _, e := time.ParseInLocation(time.DateOnly, date, d.calendar()); e != nil || date <= previous {
			return result, ErrStateInvalid
		}
		previous = date
	}
	if result.Claims == nil {
		result.Claims = map[string]cumulativeReceipt{}
	}
	for key, claim := range result.Claims {
		if !validIdentifier(key) || claim.StageID != key || strings.TrimSpace(claim.CharacterID) == "" || claim.ClaimedAt.IsZero() || len(claim.DefinitionHash) != 64 || claim.RequiredDays == 0 || int(claim.RequiredDays) > len(result.QualifiedDates) || len(claim.Items) == 0 {
			return result, ErrStateInvalid
		}
		date, _ := calendarDay(d.calendar(), d.boundary(), claim.ClaimedAt)
		if date < result.QualifiedDates[claim.RequiredDays-1] {
			return result, ErrStateInvalid
		}
	}
	return result, nil
}
func (l cumulativeLedger) snapshot() CumulativeSnapshot {
	ids := make([]string, 0, len(l.Claims))
	for id := range l.Claims {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return CumulativeSnapshot{EventID: l.EventID, QualifiedDates: append([]string(nil), l.QualifiedDates...), ClaimedStageIDs: ids}
}
func putCumulativeLedger(account *r.AccountRecord, l cumulativeLedger) error {
	b, e := json.Marshal(l)
	if e != nil {
		return e
	}
	if account.Metadata == nil {
		account.Metadata = map[string]string{}
	}
	account.Metadata[cumulativeKeyPrefix+l.EventID] = string(b)
	return nil
}
func recordQualifiedDay(account *r.AccountRecord, d Definition, state persistedState) error {
	if d.QualificationStageID == "" {
		return nil
	}
	// Missing receipts are normal: claiming the last unlocked stage first
	// must not qualify a day. Reevaluate after every claim, regardless of order.
	for _, stage := range d.Stages {
		if _, found := state.Claims[stage.ID]; !found {
			return nil
		}
	}
	// Reuse the daily contract checks for every receipt's reward, gold,
	// timestamp and identity, not merely the final stage's presence.
	if err := validateState(state, d); err != nil {
		return err
	}
	// All receipts come from this locked account's authoritative daily state.
	// Their claims were committed only after loadAccountAndCharacter verified
	// ownership. Do not read other characters through the current-character
	// scoped settlement or reject valid claims made by another account character.
	for _, stage := range d.Stages {
		if state.OnlineSeconds < stage.RequiredSeconds {
			return ErrStateInvalid
		}
	}
	date := state.CalendarDate
	ledger, e := parseCumulativeLedger(account.Metadata[cumulativeKeyPrefix+d.ID], d)
	if e != nil {
		return e
	}
	n := sort.SearchStrings(ledger.QualifiedDates, date)
	if n < len(ledger.QualifiedDates) && ledger.QualifiedDates[n] == date {
		return nil
	}
	if n != len(ledger.QualifiedDates) {
		return ErrObservationStale
	}
	ledger.QualifiedDates = append(ledger.QualifiedDates, date)
	return putCumulativeLedger(account, ledger)
}

func checkCumulativeTime(l cumulativeLedger, d Definition, at time.Time) error {
	if at.IsZero() {
		return ErrObservationInvalid
	}
	if !d.activeAt(at) {
		return ErrEventInactive
	}
	date, _ := calendarDay(d.calendar(), d.boundary(), at)
	if len(l.QualifiedDates) > 0 && date < l.QualifiedDates[len(l.QualifiedDates)-1] {
		return ErrObservationStale
	}
	for _, receipt := range l.Claims {
		if at.Before(receipt.ClaimedAt) {
			return ErrObservationStale
		}
	}
	return nil
}
func (o *Owner) CumulativeStatus(ctx context.Context, c CumulativeStatusCommand) (CumulativeSnapshot, error) {
	if o == nil || o.settlements == nil {
		return CumulativeSnapshot{}, ErrOwnerUnavailable
	}
	if e := c.Definition.Validate(); e != nil {
		return CumulativeSnapshot{}, e
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var snapshot CumulativeSnapshot
	e := o.settlements.WithinCharacterSettlement(ctx, c.CharacterID, func(tx r.Group) error {
		a, _, e := loadAccountAndCharacter(ctx, tx, c.AccountID, c.CharacterID)
		if e != nil {
			return e
		}
		l, e := parseCumulativeLedger(a.Metadata[cumulativeKeyPrefix+c.Definition.Daily.ID], c.Definition.Daily)
		if e != nil {
			return e
		}
		if e = checkCumulativeTime(l, c.Definition.Daily, c.ObservedAt); e != nil {
			return e
		}
		if e = validateCumulativeReceipts(l, c.Definition); e != nil {
			return e
		}
		snapshot = l.snapshot()
		return nil
	})
	return snapshot, e
}
func validateCumulativeReceipts(l cumulativeLedger, d CumulativeDefinition) error {
	for id, receipt := range l.Claims {
		s, ok := d.stage(id)
		if !ok || receipt.DefinitionHash != cumulativeStageHash(d, s) || receipt.RequiredDays != s.RequiredDays || len(receipt.Items) != len(s.Items) {
			return ErrStateInvalid
		}
		for n, item := range receipt.Items {
			if e := validateCommittedItem(item, s.Items[n]); e != nil {
				return e
			}
		}
	}
	return nil
}
func (o *Owner) ClaimCumulative(ctx context.Context, c CumulativeClaimCommand) (ClaimResult, error) {
	if o == nil || o.settlements == nil {
		return ClaimResult{}, ErrOwnerUnavailable
	}
	if e := c.Definition.Validate(); e != nil {
		return ClaimResult{}, e
	}
	stage, ok := c.Definition.stage(c.StageID)
	if !ok {
		return ClaimResult{}, ErrStageNotFound
	}
	if c.Allocate == nil {
		return ClaimResult{}, ErrAllocatorRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result ClaimResult
	e := o.settlements.WithinCharacterSettlement(ctx, c.CharacterID, func(tx r.Group) error {
		account, _, e := loadAccountAndCharacter(ctx, tx, c.AccountID, c.CharacterID)
		if e != nil {
			return e
		}
		l, e := parseCumulativeLedger(account.Metadata[cumulativeKeyPrefix+c.Definition.Daily.ID], c.Definition.Daily)
		if e != nil {
			return e
		}
		if e = checkCumulativeTime(l, c.Definition.Daily, c.ClaimedAt); e != nil {
			return e
		}
		if e = validateCumulativeReceipts(l, c.Definition); e != nil {
			return e
		}
		if receipt, replay := l.Claims[c.StageID]; replay {
			date, _ := calendarDay(c.Definition.Daily.calendar(), c.Definition.Daily.boundary(), receipt.ClaimedAt)
			result = ClaimResult{EventID: l.EventID, CalendarDate: date, StageID: receipt.StageID, CharacterID: receipt.CharacterID, Replayed: true, Items: cloneCommittedItems(receipt.Items), ClaimedAt: receipt.ClaimedAt}
			return nil
		}
		if uint64(len(l.QualifiedDates)) < uint64(stage.RequiredDays) {
			return ErrStageLocked
		}
		if tx.Inventory == nil {
			return ErrOwnerUnavailable
		}
		inventory, found, e := tx.Inventory.Load(ctx, c.CharacterID)
		if e != nil {
			return e
		}
		if !found || inventory.CharacterID != c.CharacterID {
			return ErrInventoryNotFound
		}
		inventory = r.CloneInventory(inventory)
		if inventory.Slots == nil {
			inventory.Slots = map[string]r.ItemStack{}
		}
		items := make([]CommittedItem, 0, len(stage.Items))
		for _, reward := range stage.Items {
			item, e := c.Allocate(&inventory, reward)
			if e != nil {
				return e
			}
			if e = validateCommittedItem(item, reward); e != nil {
				return e
			}
			items = append(items, item)
		}
		inventory.UpdatedAt = c.ClaimedAt.UTC()
		if e = r.SaveInventoryFields(ctx, tx.Inventory, inventory, r.InventoryFieldSlots); e != nil {
			return e
		}
		receipt := cumulativeReceipt{StageID: stage.ID, CharacterID: c.CharacterID, DefinitionHash: cumulativeStageHash(c.Definition, stage), RequiredDays: stage.RequiredDays, Items: cloneCommittedItems(items), ClaimedAt: c.ClaimedAt.UTC()}
		l.Claims[stage.ID] = receipt
		if e = putCumulativeLedger(&account, l); e != nil {
			return e
		}
		account.UpdatedAt = c.ClaimedAt.UTC()
		if e = tx.Account.Save(ctx, account); e != nil {
			return e
		}
		date, _ := calendarDay(c.Definition.Daily.calendar(), c.Definition.Daily.boundary(), c.ClaimedAt)
		result = ClaimResult{EventID: l.EventID, CalendarDate: date, StageID: stage.ID, CharacterID: c.CharacterID, Items: cloneCommittedItems(items), ClaimedAt: receipt.ClaimedAt}
		return nil
	})
	if e != nil {
		return ClaimResult{}, e
	}
	result.Items = cloneCommittedItems(result.Items)
	return result, nil
}
