package dnfbridge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// A one-way, pickup-only capability derived before the game token leaves the
// launcher. Possessing this value does not reveal the game/login/admin token.
func helperCredential(token string) string {
	h := sha256.Sum256([]byte("Arad-Helper-Loot/1\n" + token))
	return hex.EncodeToString(h[:])
}
func helperBinding(id boundGameSessionCharacter) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", id.session.connID, id.character, id.generation)))
	return hex.EncodeToString(h[:16])
}

type helperLootRequest struct {
	Version   int                   `json:"version"`
	Action    string                `json:"action"`
	Binding   string                `json:"binding"`
	Owner     string                `json:"owner"`
	Lease     string                `json:"lease"`
	Revision  uint64                `json:"revision"`
	Sequence  uint64                `json:"sequence"`
	Policy    helperLootPolicy      `json:"policy"`
	Operation *helperUtilityRequest `json:"operation,omitempty"`
}
type helperLootReply struct {
	OK            bool             `json:"ok"`
	Available     bool             `json:"available"`
	Binding       string           `json:"binding"`
	CharacterID   string           `json:"characterId"`
	CharacterName string           `json:"characterName"`
	Revision      uint64           `json:"revision"`
	Enabled       bool             `json:"enabled"`
	Paused        bool             `json:"paused"`
	Policy        helperLootPolicy `json:"policy"`
	Lease         string           `json:"lease,omitempty"`
	ExpiresUTC    string           `json:"expiresUtc,omitempty"`
	CurrentLease  bool             `json:"currentLease,omitempty"`
	Recoverable   bool             `json:"recoverable,omitempty"`
	Message       string           `json:"message,omitempty"`
}

var errHelperChanged = errors.New("helper binding or lease changed")

// This reference integration requires an explicit operator opt-in.
func playerHelperReferenceEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("DNFBRIDGE_PLAYER_HELPER_REFERENCE")), "on")
}

func (s *Service) helperCaller(secret string) (boundGameSessionCharacter, bool) {
	if len(secret) != 64 {
		return boundGameSessionCharacter{}, false
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return boundGameSessionCharacter{}, false
	}
	digest := sha256.Sum256([]byte(secret))
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.gameSessions {
		if p != nil && id != 0 && p.selectedCharacterID == id && p.accountFromToken && p.loginPacketSeen && !p.loginRevoked.Load() && (p.ctx == nil || p.ctx.Err() == nil) && subtle.ConstantTimeCompare(digest[:], p.helperAccessDigest[:]) == 1 {
			return boundGameSessionCharacter{p, id, p.characterGeneration}, true
		}
	}
	return boundGameSessionCharacter{}, false
}
func (s *Service) applyHelperLoot(p *gameSession, id boundGameSessionCharacter, q helperLootRequest) (helperLootReply, error) {
	if !s.boundGameSessionCharacterCurrent(id) || p.loginRevoked.Load() {
		return helperLootReply{}, errHelperChanged
	}
	if err := s.startCurrentPlayerHelper(p); err != nil {
		return helperLootReply{}, err
	}
	binding := helperBinding(id)
	if q.Action != "status" && q.Binding != binding {
		return helperLootReply{}, errHelperChanged
	}
	now := s.gameplayNow()
	h := &p.helper
	h.mu.Lock()
	defer h.mu.Unlock()
	expireCurrentHelperLeaseLocked(h, now)
	reply := func(includeLease bool) helperLootReply {
		r := helperLootReply{OK: true, Available: helperAutoPickupAvailable(), Binding: binding, CharacterID: strconv.Itoa(int(id.character)), CharacterName: p.launcherCharacterName, Revision: h.revision, Enabled: h.enabled, Paused: h.paused, Policy: h.policy, Recoverable: h.recoverable && !h.enabled && !h.paused, CurrentLease: h.enabled && validDiagnosticID(q.Lease) && h.owner == q.Owner && h.lease == q.Lease}
		if includeLease {
			r.Lease = h.lease
			r.ExpiresUTC = h.leaseUntil.UTC().Format(time.RFC3339Nano)
		}
		return r
	}
	if q.Action == "status" {
		return reply(false), nil
	}
	if !helperAutoPickupAvailable() || h.character != id.character {
		return helperLootReply{}, errHelperChanged
	}
	if q.Action == "apply" {
		if !validDiagnosticID(q.Owner) || !q.Policy.valid() {
			return helperLootReply{}, errors.New("invalid pickup filter")
		}
		if h.enabled && h.owner == q.Owner && h.policy == q.Policy {
			return reply(true), nil
		} // lost response replay, no extension
		if q.Revision != h.revision {
			return helperLootReply{}, errHelperChanged
		}
		lease := launcherNonce()
		if lease == "" {
			return helperLootReply{}, errors.New("lease unavailable")
		}
		h.revision++
		h.owner = q.Owner
		h.lease = lease
		h.leaseSequence = 0
		h.leaseUntil = now.Add(helperLeaseDuration)
		h.policy = q.Policy
		h.recoverable = false
		h.enabled = true
		h.paused = false
		if err := s.armCurrentPlayerHelperLocked(p); err != nil {
			h.enabled = false
			h.recoverable = false
			return helperLootReply{}, err
		}
		return reply(true), nil
	}
	if h.owner != q.Owner || !validDiagnosticID(q.Lease) || h.lease != q.Lease || !h.enabled || !now.Before(h.leaseUntil) {
		return helperLootReply{}, errHelperChanged
	}
	switch q.Action {
	case "renew":
		if q.Sequence < h.leaseSequence {
			return helperLootReply{}, errHelperChanged
		}
		if q.Sequence > h.leaseSequence {
			h.leaseSequence = q.Sequence
			h.leaseUntil = now.Add(helperLeaseDuration)
		}
		return reply(false), nil
	case "release":
		h.enabled = false
		h.recoverable = false
		h.pending = false
		h.scheduleSerial++
		h.owner = ""
		h.lease = ""
		h.revision++
		if h.queue != nil && h.timer != "" {
			h.queue.Cancel(h.timer)
		}
		return reply(false), nil
	default:
		return helperLootReply{}, errors.New("invalid helper action")
	}
}
func (a *accountAPIServer) handlePlayerHelper(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !playerHelperReferenceEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var q helperLootRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF || !((q.Version == 1 && (q.Action == "status" || q.Action == "apply" || q.Action == "renew" || q.Action == "release")) || (q.Version == 2 && q.Action == "utility" && q.Operation != nil)) {
		http.Error(w, "invalid helper request", 400)
		return
	}
	if q.Version == 2 {
		a.handleHelperUtility(w, r, q)
		return
	}
	id, ok := a.service.helperCaller(r.Header.Get("X-DNF-Helper"))
	if !ok {
		http.Error(w, "game session unavailable", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var out helperLootReply
	err := a.service.callGameSession(ctx, id.session, "player-helper-policy", func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, ok := a.service.helperCaller(r.Header.Get("X-DNF-Helper"))
		if !ok || current != id {
			return errHelperChanged
		}
		var err error
		out, err = a.service.applyHelperLoot(id.session, id, q)
		return err
	})
	if err != nil {
		http.Error(w, "pickup session changed or request unavailable", 409)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_ = json.NewEncoder(w).Encode(out)
}
