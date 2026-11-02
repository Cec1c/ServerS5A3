package dnfbridge

// A login-scoped, read-only support feed. Credentials remain in memory and
// never appear in snapshots, game packets, or the ordinary packet log.
import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"
)

type launcherDiagnosticGrant struct {
	Version       int    `json:"version"`
	CorrelationID string `json:"correlation_id"`
	ReadToken     string `json:"read_token"`
	ExpiresUTC    string `json:"expires_utc"`
}
type launcherContextField struct {
	Value  *string `json:"value"`
	Status string  `json:"status"`
	Reason string  `json:"reason"`
}
type launcherContextSnapshot struct {
	CorrelationID        string                          `json:"correlation_id"`
	ServerInstance       string                          `json:"server_instance"`
	ConnectionGeneration uint64                          `json:"connection_generation"`
	ObservedUTC          string                          `json:"observed_at"`
	Source               string                          `json:"source"`
	Status               string                          `json:"status"`
	Fields               map[string]launcherContextField `json:"fields"`
}
type launcherContextEvent struct {
	Sequence uint64                  `json:"sequence"`
	Kind     string                  `json:"kind"`
	Snapshot launcherContextSnapshot `json:"snapshot"`
}
type launcherContextReply struct {
	Version           int                     `json:"version"`
	CorrelationID     string                  `json:"correlation_id"`
	ServerReceivedUTC string                  `json:"server_received_utc"`
	ServerSentUTC     string                  `json:"server_sent_utc"`
	LastSequence      uint64                  `json:"last_sequence"`
	EventsLost        bool                    `json:"events_lost"`
	Snapshot          launcherContextSnapshot `json:"snapshot"`
	Events            []launcherContextEvent  `json:"events"`
}
type launcherDiagnosticEntry struct {
	expires                  time.Time
	readHash                 [32]byte
	correlation              string
	sequence, nextConnection uint64
	current                  string
	snapshot                 launcherContextSnapshot
	events                   []launcherContextEvent
	lastPoll                 time.Time
	idleUntil                time.Time
}
type launcherDiagnosticStore struct {
	mu       sync.Mutex
	instance string
	launches map[[32]byte]*launcherDiagnosticEntry
	readers  map[[32]byte]*launcherDiagnosticEntry
}
type launcherDiagnosticBinding struct {
	entry      *launcherDiagnosticEntry
	generation uint64
}

var launcherContextKeys = []string{"character_id", "character_name", "character_generation", "connection_id", "channel_id", "party_id", "party_generation", "dungeon_id", "map_id", "room", "instance_id", "settlement_phase", "boss_dead", "business_state", "reason_code"}

func launcherField(value string) launcherContextField {
	return launcherContextField{&value, "current", ""}
}
func launcherUnavailable(reason string) launcherContextField {
	return launcherContextField{nil, "unavailable", reason}
}
func launcherNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
func validDiagnosticID(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 16 && len(value) == 32
}

func (d *launcherDiagnosticStore) issue(gameToken, correlation string, now time.Time) *launcherDiagnosticGrant {
	if !validDiagnosticID(correlation) || gameToken == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.launches == nil {
		d.launches = make(map[[32]byte]*launcherDiagnosticEntry)
		d.readers = make(map[[32]byte]*launcherDiagnosticEntry)
	}
	for key, e := range d.launches {
		if !now.Before(e.expires) || !now.Before(e.idleUntil) {
			delete(d.launches, key)
			delete(d.readers, e.readHash)
		}
	}
	if len(d.launches) >= 256 {
		return nil
	}
	if d.instance == "" {
		d.instance = launcherNonce()
	}
	secret := launcherNonce() + launcherNonce()
	if len(secret) != 64 || d.instance == "" {
		return nil
	}
	e := &launcherDiagnosticEntry{expires: now.Add(24 * time.Hour), idleUntil: now.Add(10 * time.Minute), readHash: sha256.Sum256([]byte(secret)), correlation: correlation}
	fields := make(map[string]launcherContextField)
	for _, k := range launcherContextKeys {
		fields[k] = launcherUnavailable("not_connected")
	}
	e.snapshot = launcherContextSnapshot{CorrelationID: correlation, ServerInstance: d.instance, ObservedUTC: now.UTC().Format(time.RFC3339Nano), Source: "authenticated_game_session", Status: "waiting", Fields: fields}
	d.launches[sha256.Sum256([]byte(gameToken))] = e
	d.readers[e.readHash] = e
	return &launcherDiagnosticGrant{1, correlation, secret, e.expires.UTC().Format(time.RFC3339Nano)}
}
func (s *Service) bindLauncherDiagnostics(session *gameSession, gameToken string) {
	d := &s.launcherDiagnostics
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.launches[sha256.Sum256([]byte(gameToken))]
	if e == nil || !time.Now().Before(e.expires) || !time.Now().Before(e.idleUntil) {
		return
	}
	e.nextConnection++
	session.launcherDiagnostic = &launcherDiagnosticBinding{e, e.nextConnection}
}

// Called after an event on the serial session owner. No database reads or IO.
// Optional fields are taken under their existing owner locks, never guessed
// from a client executable, cached roster, IP address, or another account.
func (s *Service) captureLauncherContext(session *gameSession, disconnected bool) {
	b := session.launcherDiagnostic
	if b == nil {
		return
	}
	d := &s.launcherDiagnostics
	d.mu.Lock()
	active := time.Now().Before(b.entry.expires) && time.Now().Before(b.entry.idleUntil)
	d.mu.Unlock()
	if !active {
		return
	}
	f := make(map[string]launcherContextField)
	for _, k := range launcherContextKeys {
		f[k] = launcherUnavailable("not_in_scene")
	}
	session.townMu.Lock()
	s.mu.Lock()
	id, generation := session.selectedCharacterID, session.characterGeneration
	canonical := id != 0 && s.gameSessions[id] == session
	f["channel_id"] = launcherField(strconv.Itoa(session.channel.ID))
	s.mu.Unlock()
	if id != 0 {
		f["character_id"] = launcherField(strconv.Itoa(int(id)))
		f["character_generation"] = launcherField(strconv.FormatUint(generation, 10))
		f["character_name"] = launcherUnavailable("identity_unavailable")
		if session.launcherCharacterID == id {
			f["character_name"] = launcherField(session.launcherCharacterName)
		}
	} else {
		for _, k := range []string{"character_id", "character_name", "character_generation"} {
			f[k] = launcherUnavailable("not_selected")
		}
	}
	f["connection_id"] = launcherField(session.connID)
	f["business_state"] = launcherField("connected")
	f["reason_code"] = launcherField("unknown")
	f["party_generation"] = launcherUnavailable("not_exposed")
	party := runtimePartyStateSnapshot(session)
	if id != 0 && party.PartyID > 0 {
		f["party_id"] = launcherField(strconv.Itoa(party.PartyID))
	}
	session.dungeon.mu.Lock()
	if r := session.dungeon.runtime; r != nil && id != 0 && dungeonRuntimeOwnsCharacter(r, id) {
		f["dungeon_id"] = launcherField(strconv.FormatInt(r.Dungeon.ID, 10))
		f["settlement_phase"] = launcherField(r.settlementPhase.String())
		f["boss_dead"] = launcherField(strconv.FormatBool(r.bossDieCheckAccepted))
		if r.Session != nil {
			coordinate, mapID := r.Session.Location()
			f["map_id"] = launcherField(strconv.FormatInt(mapID, 10))
			f["room"] = launcherField(coordinate.String())
		}
		if r.partyInstance != nil {
			f["instance_id"] = launcherField("party-" + strconv.FormatUint(r.partyInstance.id, 10))
		} else if !r.startedAt.IsZero() {
			f["instance_id"] = launcherField(session.connID + "/" + strconv.FormatInt(r.startedAt.UnixNano(), 10))
		}
	}
	session.dungeon.mu.Unlock()
	session.townMu.Unlock()
	status := "current"
	if disconnected {
		status = "last_known"
		for k, v := range f {
			if v.Value != nil {
				v.Status = "last_known"
				f[k] = v
			}
		}
		f["business_state"] = launcherField("disconnected")
		if session.loginRevoked.Load() {
			f["reason_code"] = launcherField("login_replaced")
		} else {
			f["reason_code"] = launcherField("connection_closed_unknown")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e := b.entry
	if !time.Now().Before(e.expires) || !time.Now().Before(e.idleUntil) {
		return
	}
	// Auxiliary sockets and a retiring generation cannot overwrite the active
	// character connection. The canonical owner may legitimately change.
	if e.current != "" && e.current != session.connID && (!canonical || b.generation < e.snapshot.ConnectionGeneration) {
		priorGeneration := uint64(0)
		if previous := e.snapshot.Fields["character_generation"].Value; previous != nil {
			priorGeneration, _ = strconv.ParseUint(*previous, 10, 64)
		}
		if !canonical || generation <= priorGeneration {
			return
		}
		e.nextConnection++
		b.generation = e.nextConnection
	}
	if e.current == session.connID && e.snapshot.Status == "last_known" && !disconnected {
		return
	}
	next := launcherContextSnapshot{e.correlation, d.instance, b.generation, time.Now().UTC().Format(time.RFC3339Nano), "authenticated_game_session", status, f}
	kind := "context_changed"
	if disconnected {
		kind = "business_disconnect"
	} else if e.current != session.connID {
		kind = "connection_changed"
	} else if !reflect.DeepEqual(e.snapshot.Fields["character_id"], f["character_id"]) || !reflect.DeepEqual(e.snapshot.Fields["character_generation"], f["character_generation"]) {
		kind = "character_changed"
	} else if !reflect.DeepEqual(e.snapshot.Fields["settlement_phase"], f["settlement_phase"]) {
		kind = "settlement_changed"
	}
	changed := e.current != session.connID || e.snapshot.Status != status || !reflect.DeepEqual(e.snapshot.Fields, f)
	e.current = session.connID
	e.snapshot = next
	if changed {
		e.sequence++
		e.events = append(e.events, launcherContextEvent{e.sequence, kind, next})
		if len(e.events) > 64 {
			e.events = append([]launcherContextEvent(nil), e.events[len(e.events)-64:]...)
		}
	}
}

func (a *accountAPIServer) handleLauncherDiagnostics(w http.ResponseWriter, r *http.Request) {
	received := time.Now().UTC()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || a.service.options.playerStrict && a.clientIP(r) == "" {
		http.NotFound(w, r)
		return
	}
	var request struct {
		ReadToken     string `json:"read_token"`
		CorrelationID string `json:"correlation_id"`
		After         uint64 `json:"after"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || len(request.ReadToken) != 64 || !validDiagnosticID(request.CorrelationID) {
		http.Error(w, "invalid request", 400)
		return
	}
	d := &a.service.launcherDiagnostics
	d.mu.Lock()
	e := d.readers[sha256.Sum256([]byte(request.ReadToken))]
	if e == nil || e.correlation != request.CorrelationID || !received.Before(e.expires) || !received.Before(e.idleUntil) {
		d.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if request.After > e.sequence {
		d.mu.Unlock()
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	if received.Sub(e.lastPoll) < time.Second {
		d.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "retry later", 429)
		return
	}
	e.lastPoll = received
	e.idleUntil = received.Add(10 * time.Minute)
	out := launcherContextReply{Version: 1, CorrelationID: e.correlation, ServerReceivedUTC: received.Format(time.RFC3339Nano), LastSequence: e.sequence, Snapshot: e.snapshot, Events: []launcherContextEvent{}}
	out.EventsLost = len(e.events) > 0 && request.After < e.events[0].Sequence-1
	for _, event := range e.events {
		if event.Sequence > request.After {
			out.Events = append(out.Events, event)
		}
	}
	d.mu.Unlock()
	out.ServerSentUTC = time.Now().UTC().Format(time.RFC3339Nano)
	_ = json.NewEncoder(w).Encode(out)
}
