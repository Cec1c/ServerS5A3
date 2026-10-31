// Package questmaterial owns durable, personal type-1 quest-material delivery.
// It changes neither ordinary ground loot nor the PVF probability algorithm.
package questmaterial

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

const generationKey = "quest_material_generation_v1"
const maxPendingRooms = 128
const maxRoomEvents = 4096

var ErrCapacity = errors.New("quest material journal capacity reached; pending receipts retained")
var ErrBagFull = errors.New("quest material inventory full; original decision retained")

type Attempt struct {
	QuestID    int64
	Generation string
	ItemID     uint32
	Count      int64 // already rolled independently for this character; zero is a durable miss
	MaxStack   int64
}

type Delivery struct {
	Attempts []Attempt
	Done     bool
	Notified bool
	Slots    []int16
}

type journal struct{ Events map[string]Delivery }
type pending struct{ Rooms []string }

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func scope(id, room string) string { return "character:" + id + ":quest_material:" + room }

func loadJSON(ctx context.Context, settings r.SettingsRepository, key string, out any) error {
	record, found, err := settings.Load(ctx, key)
	if err != nil || !found {
		return err
	}
	if record.Values["v1"] == "" {
		return errors.New("quest material journal has no versioned payload")
	}
	return json.Unmarshal([]byte(record.Values["v1"]), out)
}

func saveJSON(ctx context.Context, settings r.SettingsRepository, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return settings.Save(ctx, r.SettingsRecord{Scope: key, Values: map[string]string{"v1": string(b)}, UpdatedAt: time.Now().UTC()})
}

// Prepare runs once for a room/object event, including ineligible and missed
// decisions. All recipients are committed together; an error leaves the room
// death uncommitted. Retrying an unknown commit reads exactly the original plan.
func Prepare(ctx context.Context, repos r.Group, room, event string, ids []string,
	plan func(context.Context, string, r.QuestRecord) ([]Attempt, error)) error {
	return retry(ctx, func() error { return prepareOnce(ctx, repos, room, event, ids, plan) })
}

func prepareOnce(ctx context.Context, repos r.Group, room, event string, ids []string, plan func(context.Context, string, r.QuestRecord) ([]Attempt, error)) error {
	if repos.QuestMaterialPlan == nil || room == "" || room == "pending" || event == "" || plan == nil {
		return r.ErrCharacterSettlementTransactionUnavailable
	}
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	return repos.QuestMaterialPlan.WithinQuestMaterialPreparation(ctx, ids, func(tx r.Group) error {
		quests := map[string]r.QuestRecord{}
		for _, id := range ids {
			q, _, err := r.LoadQuestActivePlanning(ctx, tx.Quest, id)
			if err != nil {
				return err
			}
			quests[id] = q
		}
		for _, id := range ids {
			var j journal
			if err := loadJSON(ctx, tx.Settings, scope(id, room), &j); err != nil {
				return err
			}
			if _, found := j.Events[event]; found {
				continue
			}
			if len(j.Events) >= maxRoomEvents {
				return ErrCapacity
			}
			q := quests[id]
			before := r.CloneQuest(q)
			attempts, err := plan(ctx, id, q)
			if err != nil {
				return err
			}
			changed := false
			for i := range attempts {
				state, ok := q.States[attempts[i].QuestID]
				if !ok || state.Status != "active" {
					return errors.New("quest material planner returned inactive quest")
				}
				if state.Extra == nil {
					state.Extra = map[string]string{}
				}
				if state.Extra[generationKey] == "" {
					token, err := NewID()
					if err != nil {
						return err
					}
					state.Extra[generationKey] = token
					q.States[attempts[i].QuestID] = state
					changed = true
				}
				attempts[i].Generation = state.Extra[generationKey]
			}
			if changed {
				if err := r.SaveQuestChanges(ctx, tx.Quest, before, q, r.QuestFieldStates); err != nil {
					return err
				}
			}
			if j.Events == nil {
				j.Events = map[string]Delivery{}
			}
			d := Delivery{Attempts: attempts, Done: len(attempts) == 0, Notified: len(attempts) == 0}
			j.Events[event] = d
			if err := saveJSON(ctx, tx.Settings, scope(id, room), j); err != nil {
				return err
			}
			if !d.Done {
				var p pending
				if err := loadJSON(ctx, tx.Settings, scope(id, "pending"), &p); err != nil {
					return err
				}
				found := false
				for _, v := range p.Rooms {
					found = found || v == room
				}
				if !found {
					if len(p.Rooms) >= maxPendingRooms {
						return ErrCapacity
					}
					p.Rooms = append(p.Rooms, room)
				}
				if err := saveJSON(ctx, tx.Settings, scope(id, "pending"), p); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

type Notice struct{ Room, Event string }

type Result struct {
	Notices   []Notice
	Inventory r.InventoryRecord
	Slots     []int16
	Pending   bool
}

// Recover uses one character transaction for inventory plus receipts. Failed or
// unknown commits are retried using the same stored decisions, never a new roll.
// Completed room journals are immutable tombstones. The pending index contains
// only unresolved rooms and is bounded; no pending receipt is age-evicted.
func Recover(ctx context.Context, repos r.Group, accountID, characterID string,
	grant func(*r.InventoryRecord, Attempt) ([]int16, error)) (result Result, err error) {
	err = retry(ctx, func() error {
		var e error
		result, e = recoverOnce(ctx, repos, accountID, characterID, grant)
		return e
	})
	return
}

func recoverOnce(ctx context.Context, repos r.Group, accountID, characterID string, grant func(*r.InventoryRecord, Attempt) ([]int16, error)) (result Result, err error) {
	if repos.CharacterSettlement == nil || grant == nil {
		return result, r.ErrCharacterSettlementTransactionUnavailable
	}
	// Avoid opening the heavyweight character settlement for the usual empty inbox.
	var outer pending
	if repos.Settings == nil {
		return result, r.ErrCharacterSettlementTransactionUnavailable
	}
	if err = loadJSON(ctx, repos.Settings, scope(characterID, "pending"), &outer); err != nil || len(outer.Rooms) == 0 {
		return result, err
	}
	err = repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx r.Group) error {
		character, found, e := tx.Character.Load(ctx, characterID)
		if e != nil {
			return e
		}
		if !found || character.AccountID != accountID {
			return errors.New("quest material character ownership mismatch")
		}
		q, _, e := r.LoadQuestActivePlanning(ctx, tx.Quest, characterID)
		if e != nil {
			return e
		}
		var p pending
		if e = loadJSON(ctx, tx.Settings, scope(characterID, "pending"), &p); e != nil {
			return e
		}
		inv, _, e := tx.Inventory.Load(ctx, characterID)
		if e != nil {
			return e
		}
		inv.CharacterID = characterID
		left := make([]string, 0, len(p.Rooms))
		notVisited := []string{}
		changed := false
		for ri, room := range p.Rooms {
			if ri >= 4 {
				notVisited = append(notVisited, room)
				continue
			}
			var j journal
			if e = loadJSON(ctx, tx.Settings, scope(characterID, room), &j); e != nil {
				return e
			}
			if len(j.Events) == 0 {
				return errors.New("pending quest material journal missing; retained for recovery")
			}
			keys := make([]string, 0, len(j.Events))
			for key := range j.Events {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			unresolved := false
			for _, key := range keys {
				d := j.Events[key]
				if d.Done {
					if !d.Notified {
						result.Slots = append(result.Slots, d.Slots...)
						result.Notices = append(result.Notices, Notice{room, key})
						unresolved = true
					}
					continue
				}
				before := r.CloneInventory(inv)
				slots := []int16{}
				full := false
				for _, a := range d.Attempts {
					state, active := q.States[a.QuestID]
					if !active || state.Status != "active" || state.Extra[generationKey] != a.Generation || a.Count <= 0 {
						continue
					}
					remaining := a.Count
					if a.MaxStack > 0 {
						held := int64(0)
						for _, stack := range inv.Slots {
							if stack.ItemID == int64(a.ItemID) {
								held += int64(stack.Count)
							}
						}
						if remaining > a.MaxStack-held {
							remaining = a.MaxStack - held
						}
						if remaining <= 0 {
							continue
						}
					}
					a.Count = remaining
					grantedSlots, ge := grant(&inv, a)
					if errors.Is(ge, ErrBagFull) {
						full = true
						break
					}
					if ge != nil {
						return ge
					}
					slots = append(slots, grantedSlots...)
				}
				if full {
					inv = before
					unresolved = true
					continue
				}
				d.Done = true
				d.Slots = slots
				d.Notified = len(slots) == 0
				if !d.Notified {
					result.Notices = append(result.Notices, Notice{room, key})
					unresolved = true
				}
				j.Events[key] = d
				result.Slots = append(result.Slots, slots...)
				changed = changed || len(slots) > 0
			}
			if unresolved {
				left = append(left, room)
			}
			if e = saveJSON(ctx, tx.Settings, scope(characterID, room), j); e != nil {
				return e
			}
		}
		left = append(notVisited, left...)
		if changed {
			if e = tx.Inventory.Save(ctx, inv); e != nil {
				return e
			}
		}
		if e = saveJSON(ctx, tx.Settings, scope(characterID, "pending"), pending{Rooms: left}); e != nil {
			return e
		}
		result.Inventory = inv
		result.Pending = len(left) > 0
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func EventKey(objectKey uint32, monsterID int64) string {
	return fmt.Sprintf("%s/%d", strconv.FormatUint(uint64(objectKey), 10), monsterID)
}

// Acknowledge marks only the exact notices whose inventory snapshot was sent.
// A failed send/ack retains them for a later refresh; it never regrants items.
func Acknowledge(ctx context.Context, repos r.Group, characterID string, notices []Notice) error {
	if len(notices) == 0 {
		return nil
	}
	return repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx r.Group) error {
		// Same lock order as Recover/Prepare: quest row before Settings rows.
		if _, _, err := r.LoadQuestActivePlanning(ctx, tx.Quest, characterID); err != nil {
			return err
		}
		byRoom := map[string][]string{}
		for _, n := range notices {
			byRoom[n.Room] = append(byRoom[n.Room], n.Event)
		}
		rooms := []string{}
		for room := range byRoom {
			rooms = append(rooms, room)
		}
		sort.Strings(rooms)
		for _, room := range rooms {
			var j journal
			if err := loadJSON(ctx, tx.Settings, scope(characterID, room), &j); err != nil {
				return err
			}
			for _, event := range byRoom[room] {
				d, found := j.Events[event]
				if found && d.Done {
					d.Notified = true
					j.Events[event] = d
				}
			}
			if err := saveJSON(ctx, tx.Settings, scope(characterID, room), j); err != nil {
				return err
			}
		}
		return nil
	})
}

// Every attempt reloads the same durable key; even an ambiguous commit is safe
// to inspect again. Bound retries and leave unresolved work in the inbox.
func retry(ctx context.Context, apply func() error) error {
	var err error
	for i := 0; i < 8; i++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = apply(); err == nil {
			return nil
		}
		if errors.Is(err, ErrCapacity) || errors.Is(err, r.ErrCharacterSettlementTransactionUnavailable) {
			return err
		}
		if i < 7 {
			timer := time.NewTimer(time.Duration(i+1) * 5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}
