package wedding

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

func (o Owner) guestMutation(ctx context.Context, hosts, guests []string, now time.Time, apply func(r.Group, map[string]r.CharacterRecord, r.SettingsRecord) error) error {
	u, ok := o.Repositories.CharacterTrade.(r.WeddingGuestLifecycleUnitOfWork)
	if !ok || now.IsZero() || now.Unix() <= 0 || apply == nil {
		return ErrUnavailable
	}
	scope, err := r.WeddingPairScope(hosts)
	if err != nil {
		return err
	}
	ids, err := r.WeddingLifecycleIDs(hosts, guests, scope)
	if err != nil {
		return err
	}
	return u.WithinWeddingGuestLifecycle(ctx, hosts, guests, scope, func(g r.Group) error {
		if g.Character == nil || g.Settings == nil || g.Mailbox == nil {
			return ErrUnavailable
		}
		characters := map[string]r.CharacterRecord{}
		for _, id := range ids {
			c, found, err := g.Character.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || c.CharacterID != id || c.AccountID == "" {
				return ErrOwnership
			}
			characters[id] = r.CloneCharacter(c)
		}
		record, _, err := g.Settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		record.Scope = scope
		return apply(g, characters, record)
	})
}

func guestHosts(req PairRequest, characters map[string]r.CharacterRecord) (r.CharacterRecord, r.CharacterRecord, State, error) {
	a, b := characters[req.Actor.CharacterID], characters[req.Partner.CharacterID]
	if a.AccountID != req.Actor.AccountID || b.AccountID != req.Partner.AccountID || req.Revalidate != nil && !req.Revalidate() {
		return a, b, State{}, ErrOwnership
	}
	state, _, err := ValidatePair(a, b)
	return a, b, state, err
}

// Reservation charges, ticket consumption, run-key allocation and both host
// tokens commit in one GuestLifecycle transaction. The inner pair adapter is
// scoped to this transaction and never falls back to the base repositories.
func (o Owner) ReserveWithGuests(ctx context.Context, req PairRequest, kind, duration byte, day int64, rules *GuestRules) (PairResult, error) {
	var result PairResult
	hosts := []string{req.Actor.CharacterID, req.Partner.CharacterID}
	err := o.guestMutation(ctx, hosts, nil, req.Now, func(g r.Group, _ map[string]r.CharacterRecord, record r.SettingsRecord) error {
		inventoryBefore, found, err := g.Inventory.Load(ctx, req.Actor.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrItem
		}
		inventoryBefore = r.CloneInventory(inventoryBefore)
		inner := o
		inner.Repositories = g
		inner.Repositories.CharacterTrade = pairInTransaction{ids: [2]string{hosts[0], hosts[1]}, chars: g.Character, inventory: g.Inventory}
		result, err = inner.Reserve(ctx, req, kind, duration, day)
		if err != nil {
			return err
		}
		state, _, err := ValidatePair(result.Actor, result.Partner)
		if err != nil {
			return err
		}
		ledger, err := ensureGuestLedger(ctx, g, record, hosts, state, rules, req.Now)
		if err != nil {
			return err
		}
		paid := o.Rules.Ceremonies[kind]
		payment := ReservationPayment{PayerID: req.Actor.CharacterID, Gold: paid.Gold, TicketID: paid.TicketID, TicketCount: paid.TicketCount}
		var removed int64
		for slot, stack := range inventoryBefore.Slots {
			if paid.TicketCount == 0 || stack.ItemID != paid.TicketID {
				continue
			}
			after := result.Inventories[req.Actor.CharacterID].Slots[slot]
			take := stack.Count
			if after.ItemID == stack.ItemID {
				take -= after.Count
			}
			if take <= 0 {
				continue
			}
			attachment := r.MailAttachment{ItemID: stack.ItemID, Count: take, Bind: stack.Bind, ExpireAt: stack.ExpireAt, RawEntry: append([]byte(nil), stack.RawEntry...), Extra: map[string]string{}}
			for key, value := range stack.Extra {
				attachment.Extra[key] = value
			}
			if take != stack.Count {
				attachment.RawEntry = nil
				for _, key := range []string{"raw_entry_hex", "current_raw_entry_77", "raw_entry_77"} {
					delete(attachment.Extra, key)
				}
			}
			payment.Tickets = append(payment.Tickets, attachment)
			removed += take
		}
		if removed != paid.TicketCount {
			return ErrItem
		}
		ledger.ReservationPayment = payment
		chars := map[string]r.CharacterRecord{result.Actor.CharacterID: result.Actor, result.Partner.CharacterID: result.Partner}
		if err := bindGuestWeddingToken(ctx, g, chars, ledger, req.Now); err != nil {
			return err
		}
		if err := saveGuestLedger(ctx, g.Settings, record, ledger, req.Now); err != nil {
			return err
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		result.Actor, result.Partner = chars[result.Actor.CharacterID], chars[result.Partner.CharacterID]
		return nil
	})
	if err != nil {
		return PairResult{}, err
	}
	return result, nil
}

func ensureGuestLedger(ctx context.Context, g r.Group, record r.SettingsRecord, hosts []string, state State, rules *GuestRules, now time.Time) (GuestLedger, error) {
	ledger, found, err := decodeGuestLedger(record, state.CeremonyAt)
	if err != nil {
		return ledger, err
	}
	if found {
		if ledger.At != state.CeremonyAt || ledger.Kind != state.Kind || int64(ledger.Channel) != state.CeremonyChannel || int64(ledger.Area) != state.CeremonyArea || ledger.Settled || ledger.Refunded {
			return ledger, ErrState
		}
		return ledger, nil
	}
	// Bootstrap old durable reservations as metadata only, including a due
	// reservation awaiting expiry and an already-admitted ceremony. This never
	// infers a payment, guest, gift or a new reservation time from legacy rows.
	if rules == nil || state.CeremonyAt <= 0 || !((CeremonyRoute{Channel: byte(state.CeremonyChannel), Area: byte(state.CeremonyArea)}).Valid()) || rules.Capacity[state.Kind] <= 0 ||
		(state.Phase != Reserved && (state.Phase != InCeremony || state.CeremonyReadyAt == 0)) {
		return ledger, GuestError(-1)
	}
	key, err := AllocateWeddingToken(ctx, g.Settings, now)
	if err != nil {
		return ledger, err
	}
	indexRecord, index, err := loadGuestIndex(ctx, g.Settings)
	if err != nil {
		return ledger, err
	}
	if _, exists := index.Runs[key]; exists {
		return ledger, ErrState
	}
	ordered := append([]string(nil), hosts...)
	sort.Strings(ordered)
	ledger = GuestLedger{Version: 1, Key: key, Hosts: [2]string{ordered[0], ordered[1]}, At: state.CeremonyAt, Channel: byte(state.CeremonyChannel), Area: byte(state.CeremonyArea), Kind: state.Kind, Capacity: rules.Capacity[state.Kind], Invitations: map[string]GuestInvitation{}}
	index.Runs[key] = GuestRunReference{Hosts: ledger.Hosts, At: ledger.At}
	raw, err := json.Marshal(index)
	if err != nil {
		return ledger, err
	}
	indexRecord = r.CloneSettings(indexRecord)
	if indexRecord.Values == nil {
		indexRecord.Values = map[string]string{}
	}
	indexRecord.Values["index"], indexRecord.UpdatedAt = string(raw), now
	if err := g.Settings.Save(ctx, indexRecord); err != nil {
		return ledger, err
	}
	return ledger, nil
}

func (o Owner) EnsureGuestWedding(ctx context.Context, req PairRequest, rules *GuestRules) (GuestLedger, error) {
	var result GuestLedger
	err := o.guestMutation(ctx, []string{req.Actor.CharacterID, req.Partner.CharacterID}, nil, req.Now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		_, _, state, err := guestHosts(req, chars)
		if err != nil {
			return err
		}
		if state.Phase != Reserved && state.Phase != InCeremony {
			return GuestError(-1)
		}
		result, err = ensureGuestLedger(ctx, g, record, []string{req.Actor.CharacterID, req.Partner.CharacterID}, state, rules, req.Now)
		if err != nil {
			return err
		}
		if err := bindGuestWeddingToken(ctx, g, chars, result, req.Now); err != nil {
			return err
		}
		return saveGuestLedger(ctx, g.Settings, record, result, req.Now)
	})
	return result, err
}

type GuestInvitationResult struct {
	Ledger    GuestLedger
	Guests    []r.CharacterRecord
	Remaining uint16
}

func (o Owner) InviteWeddingGuests(ctx context.Context, req PairRequest, rules *GuestRules, guestIDs []string) (GuestInvitationResult, error) {
	var result GuestInvitationResult
	if len(guestIDs) == 0 {
		return result, GuestError(-7)
	}
	seen := map[string]bool{}
	for _, id := range guestIDs {
		if seen[id] {
			return result, GuestError(-5)
		}
		seen[id] = true
		if id == req.Actor.CharacterID || id == req.Partner.CharacterID {
			return result, GuestError(-6)
		}
	}
	err := o.guestMutation(ctx, []string{req.Actor.CharacterID, req.Partner.CharacterID}, guestIDs, req.Now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		_, _, state, err := guestHosts(req, chars)
		if err != nil {
			return err
		}
		if state.Phase != Reserved || state.CeremonyAt <= req.Now.Unix() {
			return GuestError(-1)
		}
		ledger, err := ensureGuestLedger(ctx, g, record, []string{req.Actor.CharacterID, req.Partner.CharacterID}, state, rules, req.Now)
		if err != nil {
			return err
		}
		if len(guestIDs) > ledger.RemainingFor(req.Actor.CharacterID) {
			return GuestError(-4)
		}
		for _, id := range guestIDs {
			if _, exists := ledger.Invitations[id]; exists {
				return GuestError(-5)
			}
			ledger.Invitations[id] = GuestInvitation{GuestID: id, InviterID: req.Actor.CharacterID, InvitedAt: req.Now.Unix()}
			result.Guests = append(result.Guests, chars[id])
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		if err := bindGuestWeddingToken(ctx, g, chars, ledger, req.Now); err != nil {
			return err
		}
		if err := saveGuestLedger(ctx, g.Settings, record, ledger, req.Now); err != nil {
			return err
		}
		result.Ledger, result.Remaining = ledger, uint16(ledger.RemainingFor(req.Actor.CharacterID))
		return nil
	})
	if err != nil {
		return GuestInvitationResult{}, err
	}
	return result, nil
}

type GuestGiftRequest struct {
	Guest      Actor
	Key        uint32
	Gold       int64
	Wish       string
	Now        time.Time
	Revalidate func() bool
}
type GuestGiftResult struct {
	Guest  r.CharacterRecord
	Ledger GuestLedger
	Replay bool
}

func (o Owner) GiveWeddingGold(ctx context.Context, rules *GuestRules, req GuestGiftRequest) (GuestGiftResult, error) {
	var result GuestGiftResult
	if rules == nil {
		return result, ErrUnavailable
	}
	if req.Gold < rules.MinimumGold {
		return result, GuestError(-6)
	}
	if req.Gold > rules.MaximumGold {
		return result, GuestError(-7)
	}
	if len(req.Wish) > 1020 {
		return result, GuestError(-5)
	}
	ref, err := o.ResolveGuestWedding(ctx, req.Key)
	if err != nil {
		return result, GuestError(-1)
	}
	if req.Guest.CharacterID == ref.Hosts[0] || req.Guest.CharacterID == ref.Hosts[1] {
		return result, GuestError(-1)
	}
	err = o.guestMutation(ctx, ref.Hosts[:], []string{req.Guest.CharacterID}, req.Now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		guest := chars[req.Guest.CharacterID]
		if guest.AccountID != req.Guest.AccountID || req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		state, _, err := ValidatePair(chars[ref.Hosts[0]], chars[ref.Hosts[1]])
		if err != nil || state.CeremonyAt != ref.At {
			return GuestError(-1)
		}
		ledger, found, err := decodeGuestLedger(record, ref.At)
		if err != nil {
			return err
		}
		if !found || ledger.Key != req.Key {
			return GuestError(-1)
		}
		inv, invited := ledger.Invitations[req.Guest.CharacterID]
		if !invited {
			return GuestError(-1)
		}
		if inv.Gold > 0 {
			return GuestError(-3)
		}
		if state.Phase != Reserved || req.Now.Unix() >= ledger.At || ledger.Settled || ledger.Refunded {
			return GuestError(-4)
		}
		if guest.Stats["gold"] < req.Gold {
			return GuestError(-8)
		}
		guest.Stats["gold"] -= req.Gold
		guest.UpdatedAt = req.Now
		if err := r.SaveCharacterFields(ctx, g.Character, guest, r.CharacterFieldStats); err != nil {
			return err
		}
		inv.Gold, inv.Wish, inv.PaidAt = req.Gold, req.Wish, req.Now.Unix()
		ledger.Invitations[req.Guest.CharacterID] = inv
		if req.Wish != "" {
			var err error
			record, err = upsertGiftComment(record, req.Key, guest, req.Wish, req.Now)
			if err != nil {
				return err
			}
		}
		if err := saveGuestLedger(ctx, g.Settings, record, ledger, req.Now); err != nil {
			return err
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		result = GuestGiftResult{Guest: guest, Ledger: ledger}
		return nil
	})
	return result, err
}

type GuestEntryResult struct {
	Guest  r.CharacterRecord
	Hosts  [2]r.CharacterRecord
	Ledger GuestLedger
}

func (o Owner) EnterGuestChurch(ctx context.Context, guest Actor, key uint32, now time.Time, position CeremonyPosition, revalidate func() bool) (GuestEntryResult, error) {
	var result GuestEntryResult
	ref, err := o.ResolveGuestWedding(ctx, key)
	if err != nil {
		return result, ErrState
	}
	if guest.CharacterID == ref.Hosts[0] || guest.CharacterID == ref.Hosts[1] || position.X < 0 || position.Y < 0 {
		return result, ErrOwnership
	}
	err = o.guestMutation(ctx, ref.Hosts[:], []string{guest.CharacterID}, now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		c := chars[guest.CharacterID]
		if c.AccountID != guest.AccountID || revalidate != nil && !revalidate() {
			return ErrOwnership
		}
		a, b := chars[ref.Hosts[0]], chars[ref.Hosts[1]]
		state, _, err := ValidatePair(a, b)
		if err != nil || state.CeremonyAt != ref.At || (state.Phase != Reserved && state.Phase != InCeremony) || ReservationExpired(state, now.Unix()) {
			return ErrState
		}
		ledger, found, err := decodeGuestLedger(record, ref.At)
		if err != nil {
			return err
		}
		if !found || ledger.Key != key || ledger.Refunded {
			return ErrState
		}
		if _, invited := ledger.Invitations[guest.CharacterID]; !invited {
			return ErrOwnership
		}
		c.Stats["town_id"], c.Stats["area_id"] = 39, int64(ledger.Area)
		c.Stats["pos_x"], c.Stats["pos_y"], c.Stats["direction"] = int64(position.X), int64(position.Y), int64(position.Direction)
		c.Stats["wedding_v1_guest_key"], c.Stats["wedding_v1_guest_at"] = int64(key), ref.At
		c.UpdatedAt = now
		if err := r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldStats, r.CharacterFieldWedding); err != nil {
			return err
		}
		if revalidate != nil && !revalidate() {
			return ErrOwnership
		}
		result = GuestEntryResult{Guest: c, Hosts: [2]r.CharacterRecord{a, b}, Ledger: ledger}
		return nil
	})
	return result, err
}

func (o Owner) GuestChurchProof(ctx context.Context, guest r.CharacterRecord, now time.Time) (GuestLedger, [2]r.CharacterRecord, error) {
	var hosts [2]r.CharacterRecord
	key := guest.Stats["wedding_v1_guest_key"]
	if key <= 0 || key > 0xffffffff || guest.Stats["town_id"] != 39 {
		return GuestLedger{}, hosts, ErrState
	}
	ref, err := o.ResolveGuestWedding(ctx, uint32(key))
	if err != nil || ref.At != guest.Stats["wedding_v1_guest_at"] {
		return GuestLedger{}, hosts, ErrState
	}
	for i, id := range ref.Hosts {
		c, found, e := o.Repositories.Character.Load(ctx, id)
		if e != nil {
			return GuestLedger{}, hosts, e
		}
		if !found {
			return GuestLedger{}, hosts, ErrState
		}
		hosts[i] = c
	}
	state, _, err := ValidatePair(hosts[0], hosts[1])
	if err != nil || now.IsZero() || state.CeremonyAt != ref.At || (state.Phase != Reserved && state.Phase != InCeremony && state.Phase != Married) || ReservationExpired(state, now.Unix()) {
		return GuestLedger{}, hosts, ErrState
	}
	ledger, found, err := o.ReadGuestLedger(ctx, ref.Hosts[:], ref.At)
	if err != nil || !found || ledger.Key != uint32(key) || ledger.Refunded || int64(ledger.Area) != guest.Stats["area_id"] {
		return ledger, hosts, ErrState
	}
	if _, invited := ledger.Invitations[guest.CharacterID]; !invited {
		return ledger, hosts, ErrOwnership
	}
	return ledger, hosts, nil
}

func guestIDs(ledger GuestLedger) []string {
	ids := make([]string, 0, len(ledger.Invitations))
	for id := range ledger.Invitations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func sameGuestIDs(a, b GuestLedger) bool {
	x, y := guestIDs(a), guestIDs(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
func guestCompletionKey(key uint32, kind, id string) string {
	return "wedding-guest:" + strconv.FormatUint(uint64(key), 10) + ":" + kind + ":" + id
}

func bindGuestWeddingToken(ctx context.Context, g r.Group, chars map[string]r.CharacterRecord, ledger GuestLedger, now time.Time) error {
	for _, id := range ledger.Hosts {
		c := chars[id]
		if c.Stats["wedding_v1_relation_token"] == int64(ledger.Key) {
			continue
		}
		c.Stats["wedding_v1_relation_token"], c.UpdatedAt = int64(ledger.Key), now
		if err := r.SaveCharacterFields(ctx, g.Character, c, r.CharacterFieldWedding); err != nil {
			return err
		}
		chars[id] = c
	}
	return nil
}

type GuestInvitationView struct {
	Ledger GuestLedger
	Hosts  [2]r.CharacterRecord
}

func (o Owner) GuestInvitations(ctx context.Context, guestID string, now time.Time) ([]GuestInvitationView, error) {
	_, index, err := loadGuestIndex(ctx, o.Repositories.Settings)
	if err != nil {
		return nil, err
	}
	var views []GuestInvitationView
	keys := make([]uint32, 0, len(index.Runs))
	for key := range index.Runs {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, key := range keys {
		ref := index.Runs[key]
		if ref.Closed {
			continue
		}
		ledger, found, err := o.ReadGuestLedger(ctx, ref.Hosts[:], ref.At)
		if err != nil {
			return nil, err
		}
		if !found || ledger.Key != key || ledger.Refunded || ledger.Settled {
			continue
		}
		if _, invited := ledger.Invitations[guestID]; !invited {
			continue
		}
		var hosts [2]r.CharacterRecord
		valid := true
		for i, id := range ref.Hosts {
			c, found, e := o.Repositories.Character.Load(ctx, id)
			if e != nil {
				return nil, e
			}
			if !found {
				valid = false
				break
			}
			hosts[i] = c
		}
		if !valid {
			continue
		}
		state, _, e := ValidatePair(hosts[0], hosts[1])
		if e != nil || state.CeremonyAt != ref.At || (state.Phase != Reserved && state.Phase != InCeremony) || ReservationExpired(state, now.Unix()) {
			continue
		}
		views = append(views, GuestInvitationView{Ledger: ledger, Hosts: hosts})
	}
	return views, nil
}
