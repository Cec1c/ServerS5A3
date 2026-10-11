// Package accountfriend owns reciprocal account friendships and invitations.
// It has no native packet or online-session dependencies.
package accountfriend

import (
	"context"
	"encoding/json"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

var (
	ErrUnavailable   = errors.New("account friend transaction unavailable")
	ErrIdentity      = errors.New("account friend identity invalid")
	ErrMissing       = errors.New("account friend owner missing")
	ErrAlready       = errors.New("account friend relation already exists")
	ErrAlreadyFriend = errors.New("account friendship already accepted")
	ErrInvitation    = errors.New("account friend invitation invalid")
	ErrCapacity      = errors.New("account friend pending list exceeds native count capacity")
)

const prefix = "account_friend_v1:"
const (
	Outgoing = "outgoing"
	Incoming = "incoming"
	Friend   = "friend"
	Removed  = "removed"
)

// MaximumPending is the verified u8 pending-list wire capacity. It is not an
// assertion about the publisher's separate account friend business limit.
const MaximumPending = 255

// InvitationLifetime is this server's explicit persistence policy. The native
// pending row carries an expiration timestamp, not a creation timestamp. This
// duration is not claimed to be recovered from the historical publisher.
const InvitationLifetime = 7 * 24 * time.Hour

type Relation struct {
	State           string    `json:"state"`
	Message         string    `json:"message,omitempty"`
	Note            string    `json:"note,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	OwnCharacterID  string    `json:"own_character_id,omitempty"`
	PeerCharacterID string    `json:"peer_character_id,omitempty"`
}
type Entry struct {
	Account  repo.AccountRecord
	Relation Relation
}
type Command struct {
	AccountID, CharacterID, TargetAccountID, TargetName, Text string
}
type Owner struct {
	repositories repo.Group
	now          func() time.Time
}

func NewOwner(repositories repo.Group) *Owner {
	return &Owner{repositories: repositories, now: time.Now}
}

func (o *Owner) Request(ctx context.Context, command Command) error {
	if command.Text == "" {
		return ErrIdentity
	}
	return o.change(ctx, command, func(a, b repo.AccountRecord, ar, br Relation) (Relation, Relation, error) {
		if ar.State == Outgoing && br.State == Incoming {
			return ar, br, nil
		}
		if active(ar) || active(br) {
			if ar.State == Friend && br.State == Friend {
				return ar, br, errors.Join(ErrAlready, ErrAlreadyFriend)
			}
			return ar, br, ErrAlready
		}
		if pendingCount(a, o.now()) >= MaximumPending || pendingCount(b, o.now()) >= MaximumPending {
			return ar, br, ErrCapacity
		}
		at := o.now().UTC()
		return Relation{State: Outgoing, Message: command.Text, CreatedAt: at, ExpiresAt: at.Add(InvitationLifetime), OwnCharacterID: command.CharacterID}, Relation{State: Incoming, Message: command.Text, CreatedAt: at, ExpiresAt: at.Add(InvitationLifetime), PeerCharacterID: command.CharacterID}, nil
	})
}
func (o *Owner) Accept(ctx context.Context, command Command) error {
	return o.change(ctx, command, func(a, b repo.AccountRecord, ar, br Relation) (Relation, Relation, error) {
		if ar.State == Friend && br.State == Friend {
			return ar, br, nil
		}
		if ar.State != Incoming || br.State != Outgoing || ar.PeerCharacterID == "" || ar.PeerCharacterID != br.OwnCharacterID {
			return ar, br, ErrInvitation
		}
		ar.State = Friend
		br.State = Friend
		ar.OwnCharacterID = command.CharacterID
		br.PeerCharacterID = command.CharacterID
		return ar, br, nil
	})
}
func (o *Owner) RefuseCancel(ctx context.Context, command Command, action byte) error {
	if action != 4 && action != 5 {
		return ErrInvitation
	}
	return o.change(ctx, command, func(a, b repo.AccountRecord, ar, br Relation) (Relation, Relation, error) {
		if ar.State == Removed && br.State == Removed {
			return ar, br, nil
		}
		if !(action == 4 && ar.State == Incoming && br.State == Outgoing || action == 5 && ar.State == Outgoing && br.State == Incoming) {
			return ar, br, ErrInvitation
		}
		ar.State = Removed
		br.State = Removed
		return ar, br, nil
	})
}
func (o *Owner) Delete(ctx context.Context, command Command) error {
	return o.change(ctx, command, func(a, b repo.AccountRecord, ar, br Relation) (Relation, Relation, error) {
		if ar.State == Removed && br.State == Removed {
			return ar, br, nil
		}
		if ar.State != Friend || br.State != Friend {
			return ar, br, ErrInvitation
		}
		ar.State = Removed
		br.State = Removed
		return ar, br, nil
	})
}
func (o *Owner) UpdateNote(ctx context.Context, command Command) error {
	return o.change(ctx, command, func(a, b repo.AccountRecord, ar, br Relation) (Relation, Relation, error) {
		if ar.State != Friend || br.State != Friend {
			return ar, br, ErrInvitation
		}
		ar.Note = command.Text
		return ar, br, nil
	})
}

func (o *Owner) change(ctx context.Context, cmd Command, mutation func(repo.AccountRecord, repo.AccountRecord, Relation, Relation) (Relation, Relation, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if o == nil || o.repositories.Account == nil || o.repositories.Character == nil {
		return ErrUnavailable
	}
	writer, ok := o.repositories.Account.(repo.AccountMetadataPairUpdater)
	if !ok {
		return ErrUnavailable
	}
	if strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.TargetAccountID) == "" || cmd.AccountID == cmd.TargetAccountID || strings.TrimSpace(cmd.TargetName) == "" || len(utf16.Encode([]rune(cmd.Text))) > 40 || strings.ContainsAny(cmd.Text, "\x00\r\n") {
		return ErrIdentity
	}
	character, found, err := o.repositories.Character.Load(ctx, cmd.CharacterID)
	if err != nil {
		return err
	}
	if !found || character.AccountID != cmd.AccountID {
		return ErrIdentity
	}
	return writer.UpdateMetadataPair(ctx, cmd.AccountID, cmd.TargetAccountID, func(a, b repo.AccountRecord) (map[string]string, map[string]string, error) {
		if strings.TrimSpace(a.RepresentAccountName) == "" || b.RepresentAccountName != cmd.TargetName {
			return nil, nil, ErrMissing
		}
		ar, aok := decode(a.Metadata[prefix+b.AccountID])
		br, bok := decode(b.Metadata[prefix+a.AccountID])
		// A malformed owned row is not absence and must never reset a relationship.
		if !aok || !bok {
			return nil, nil, ErrInvitation
		}
		if expired(ar, o.now()) {
			ar.State = Removed
		}
		if expired(br, o.now()) {
			br.State = Removed
		}
		nextA, nextB, err := mutation(a, b, ar, br)
		if err != nil {
			return nil, nil, err
		}
		av, err := json.Marshal(nextA)
		if err != nil {
			return nil, nil, err
		}
		bv, err := json.Marshal(nextB)
		if err != nil {
			return nil, nil, err
		}
		return map[string]string{prefix + b.AccountID: string(av)}, map[string]string{prefix + a.AccountID: string(bv)}, nil
	}, o.now().UTC())
}

// List reloads peer identities and excludes removed, corrupt, and one-sided
// relations. Accepted links require reciprocal friendship; invitations require
// the matching opposite direction. Character lists are read by the bridge.
func (o *Owner) List(ctx context.Context, accountID string) ([]Entry, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if o == nil || o.repositories.Account == nil {
		return nil, ErrUnavailable
	}
	account, found, err := o.repositories.Account.Load(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrMissing
	}
	ids := make([]string, 0)
	for key := range account.Metadata {
		if strings.HasPrefix(key, prefix) {
			ids = append(ids, strings.TrimPrefix(key, prefix))
		}
	}
	sort.Strings(ids)
	rows := make([]Entry, 0, len(ids))
	for _, id := range ids {
		relation, ok := decode(account.Metadata[prefix+id])
		if !ok || !active(relation) || expired(relation, o.now()) || id == accountID || id == "" {
			continue
		}
		peer, found, err := o.repositories.Account.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found || peer.RepresentAccountName == "" {
			continue
		}
		reverse, ok := decode(peer.Metadata[prefix+accountID])
		if !ok || expired(reverse, o.now()) || !compatible(relation, reverse) {
			continue
		}
		rows = append(rows, Entry{Account: peer, Relation: relation})
	}
	return rows, nil
}
func compatible(a, b Relation) bool {
	return a.State == Friend && b.State == Friend || a.State == Outgoing && b.State == Incoming || a.State == Incoming && b.State == Outgoing
}
func active(r Relation) bool { return r.State == Friend || r.State == Incoming || r.State == Outgoing }
func expired(r Relation, now time.Time) bool {
	return (r.State == Incoming || r.State == Outgoing) && (r.ExpiresAt.IsZero() || !r.ExpiresAt.After(now))
}

func pendingCount(account repo.AccountRecord, now time.Time) int {
	n := 0
	for key, value := range account.Metadata {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		r, ok := decode(value)
		if ok && !expired(r, now) && (r.State == Incoming || r.State == Outgoing) {
			n++
		}
	}
	return n
}
func decode(value string) (Relation, bool) {
	if value == "" {
		return Relation{}, true
	}
	var r Relation
	if json.Unmarshal([]byte(value), &r) != nil {
		return Relation{}, false
	}
	if !active(r) && r.State != Removed {
		return Relation{}, false
	}
	return r, true
}
