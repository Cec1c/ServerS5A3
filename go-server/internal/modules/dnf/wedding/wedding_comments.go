package wedding

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	r "longheng.io/server/internal/modules/dnf/repository"
)

type CommentRequest struct {
	Actor      Actor
	Key        uint32
	Now        time.Time
	Revalidate func() bool
}
type GuestComment struct {
	ID        uint32 `json:"id"`
	AuthorID  string `json:"author_id"`
	Name      string `json:"name"`
	Text      string `json:"text"`
	UpdatedAt uint32 `json:"updated_at"`
}
type commentBook struct {
	Version int                     `json:"version"`
	Key     uint32                  `json:"key"`
	Next    uint32                  `json:"next"`
	Rows    map[uint32]GuestComment `json:"rows"`
}

func commentBookKey(key uint32) string { return "comments_v1:" + strconv.FormatUint(uint64(key), 10) }

// Every write/read is scoped to the exact current host run. Hosts manage their
// own book; an invited guest additionally owns a committed Church admission.
func (o Owner) commentMutation(ctx context.Context, req CommentRequest, apply func(r.Group, map[string]r.CharacterRecord, r.SettingsRecord, *commentBook, bool) error) error {
	if req.Now.IsZero() || req.Now.Unix() <= 0 || req.Now.Unix() > 0xffffffff {
		return ErrState
	}
	ref, err := o.ResolveGuestWedding(ctx, req.Key)
	if err != nil {
		return ErrState
	}
	var guests []string
	host := req.Actor.CharacterID == ref.Hosts[0] || req.Actor.CharacterID == ref.Hosts[1]
	if !host && req.Revalidate == nil {
		return ErrOwnership
	}
	if !host {
		guests = []string{req.Actor.CharacterID}
	}
	return o.guestMutation(ctx, ref.Hosts[:], guests, req.Now, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord) error {
		author := chars[req.Actor.CharacterID]
		if author.AccountID != req.Actor.AccountID || req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		state, _, err := ValidatePair(chars[ref.Hosts[0]], chars[ref.Hosts[1]])
		if err != nil || state.Phase == Single || state.RelationToken != int64(req.Key) || state.CeremonyAt != ref.At {
			return ErrState
		}
		ledger, found, err := decodeGuestLedger(record, ref.At)
		if err != nil {
			return err
		}
		if !found || ledger.Key != req.Key || ledger.Refunded {
			return ErrState
		}
		if !host {
			if _, invited := ledger.Invitations[req.Actor.CharacterID]; !invited {
				return ErrOwnership
			}
			if author.Stats["wedding_v1_guest_key"] != int64(req.Key) || author.Stats["wedding_v1_guest_at"] != ref.At || author.Stats["town_id"] != 39 || author.Stats["area_id"] != int64(ledger.Area) || ReservationExpired(state, req.Now.Unix()) {
				return ErrOwnership
			}
		}
		book := commentBook{Version: 1, Key: req.Key, Rows: map[uint32]GuestComment{}}
		if raw := record.Values[commentBookKey(req.Key)]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &book); err != nil {
				return err
			}
		}
		if book.Version != 1 || book.Key != req.Key || book.Rows == nil {
			return ErrState
		}
		seen := map[string]bool{}
		for id, row := range book.Rows {
			if id == 0 || row.ID != id || id > book.Next || seen[row.AuthorID] || row.AuthorID == "" {
				return ErrState
			}
			seen[row.AuthorID] = true
		}
		if err := apply(g, chars, record, &book, host); err != nil {
			return err
		}
		if req.Revalidate != nil && !req.Revalidate() {
			return ErrOwnership
		}
		return nil
	})
}

func saveCommentBook(ctx context.Context, g r.Group, record r.SettingsRecord, book commentBook, now time.Time) error {
	raw, err := json.Marshal(book)
	if err != nil {
		return err
	}
	record = r.CloneSettings(record)
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	record.Values[commentBookKey(book.Key)], record.UpdatedAt = string(raw), now
	return g.Settings.Save(ctx, record)
}

func upsertGiftComment(record r.SettingsRecord, key uint32, author r.CharacterRecord, text string, now time.Time) (r.SettingsRecord, error) {
	book := commentBook{Version: 1, Key: key, Rows: map[uint32]GuestComment{}}
	if raw := record.Values[commentBookKey(key)]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &book); err != nil {
			return record, err
		}
	}
	if book.Version != 1 || book.Key != key || book.Rows == nil {
		return record, ErrState
	}
	var row GuestComment
	for _, existing := range book.Rows {
		if existing.AuthorID == author.CharacterID {
			row = existing
			break
		}
	}
	if row.ID == 0 {
		if book.Next == 0xffffffff || len(book.Rows) >= 200 {
			return record, ErrUnavailable
		}
		book.Next++
		row.ID = book.Next
	}
	row.AuthorID, row.Name, row.Text, row.UpdatedAt = author.CharacterID, author.Name, text, uint32(now.Unix())
	book.Rows[row.ID] = row
	raw, err := json.Marshal(book)
	if err != nil {
		return record, err
	}
	record = r.CloneSettings(record)
	if record.Values == nil {
		record.Values = map[string]string{}
	}
	record.Values[commentBookKey(key)] = string(raw)
	return record, nil
}

func (o Owner) UpdateGuestComment(ctx context.Context, req CommentRequest, text string) (GuestComment, error) {
	var result GuestComment
	if len(text) == 0 || len(text) > 1020 {
		return result, ErrState
	}
	err := o.commentMutation(ctx, req, func(g r.Group, chars map[string]r.CharacterRecord, record r.SettingsRecord, book *commentBook, _ bool) error {
		for _, row := range book.Rows {
			if row.AuthorID == req.Actor.CharacterID {
				result = row
				break
			}
		}
		if result.ID == 0 {
			if book.Next == 0xffffffff || len(book.Rows) >= 200 {
				return ErrUnavailable
			}
			book.Next++
			result.ID = book.Next
		}
		result.AuthorID, result.Name, result.Text, result.UpdatedAt = req.Actor.CharacterID, chars[req.Actor.CharacterID].Name, text, uint32(req.Now.Unix())
		book.Rows[result.ID] = result
		return saveCommentBook(ctx, g, record, *book, req.Now)
	})
	return result, err
}

func (o Owner) DeleteGuestComment(ctx context.Context, req CommentRequest, id uint32) error {
	if id == 0 {
		return ErrState
	}
	return o.commentMutation(ctx, req, func(g r.Group, _ map[string]r.CharacterRecord, record r.SettingsRecord, book *commentBook, host bool) error {
		if !host {
			return ErrOwnership
		}
		delete(book.Rows, id) // Repeated deletion is an idempotent no-op.
		return saveCommentBook(ctx, g, record, *book, req.Now)
	})
}

func (o Owner) LoadGuestComments(ctx context.Context, req CommentRequest) ([]GuestComment, error) {
	var result []GuestComment
	err := o.commentMutation(ctx, req, func(_ r.Group, _ map[string]r.CharacterRecord, _ r.SettingsRecord, book *commentBook, _ bool) error {
		for _, row := range book.Rows {
			result = append(result, row)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return nil
	})
	return result, err
}
