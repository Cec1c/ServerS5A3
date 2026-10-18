package imagecommunication

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// PermitLifetime is a server replay policy, not the unit or meaning of PVF
// summon time. No close signal exists: reopening during this window reuses it.
const PermitLifetime = 30 * time.Second
const receiptPrefix = "image_communication_receipt_"

var (
	ErrUnavailable = errors.New("image communication asset transaction unavailable")
	ErrOwnership   = errors.New("image communication character owner mismatch")
	ErrScene       = errors.New("image communication source scene unavailable")
	ErrEligibility = errors.New("image communication requires active quests")
	ErrGold        = errors.New("image communication requires more gold")
	ErrReceipt     = errors.New("image communication receipt invalid")
)

// Binding is server-owned. A random session nonce prevents reconnect/process
// restart reuse; scene generation prevents away-and-back reuse. It is not a
// client request ID. Bridge pins this identity and scene through commit/send.
type Binding struct {
	Session             [32]byte
	CharacterGeneration uint64
	SceneGeneration     uint64
}

type Command struct {
	AccountID, CharacterID string
	Binding                Binding
	Now                    time.Time
}

type Result struct {
	Charge, Gold, CurrentGold int64
	Replayed                  bool
	WalletKnown               bool
}

// Use serializes current quest membership, the authoritative wallet and a
// bounded durable receipt. Rollback leaves no receipt; an unknown commit result
// can be retried using the same binding, as can a failed socket write.
func Use(ctx context.Context, repos dnfrepo.Group, catalog *Catalog, scene Scene, cmd Command) (Result, error) {
	uow, ok := repos.CharacterAssets.(dnfrepo.CharacterAssetQuestUnitOfWork)
	if !ok || catalog == nil {
		return Result{}, ErrUnavailable
	}
	if strings.TrimSpace(cmd.AccountID) == "" || strings.TrimSpace(cmd.CharacterID) == "" || cmd.Binding.Session == ([32]byte{}) || cmd.Binding.CharacterGeneration == 0 || cmd.Binding.SceneGeneration == 0 || cmd.Now.IsZero() {
		return Result{}, ErrOwnership
	}
	if len(scene.SourceNPCs) == 0 {
		return Result{}, ErrScene
	}
	var result Result
	err := uow.WithinCharacterAssetsAndQuest(ctx, cmd.CharacterID, func(characters dnfrepo.CharacterRepository, quests dnfrepo.QuestReader) error {
		c, found, err := characters.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || c.CharacterID != cmd.CharacterID || c.AccountID != cmd.AccountID {
			return ErrOwnership
		}
		townID, hasTown := c.Stats["town_id"]
		areaID, hasArea := c.Stats["area_id"]
		if !hasTown || !hasArea || townID != scene.TownID || areaID != scene.Area.ID || c.Level < int(scene.Area.MinLevel) {
			return ErrScene
		}
		q, found, err := quests.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found || q.CharacterID != cmd.CharacterID {
			return ErrEligibility
		}
		// Match quest owner precedence: States overrides legacy Progress.
		states := make(map[int64]dnfrepo.QuestState, len(q.States)+len(q.Progress))
		for id, state := range q.Progress {
			states[id] = state
		}
		for id, state := range q.States {
			states[id] = state
		}
		for _, id := range scene.Area.NeedQuests {
			if !completedQuestStatus(states[id].Status) {
				return ErrScene
			}
		}
		active := map[int64]struct{}{}
		activeStates := map[int64]dnfrepo.QuestState{}
		for id, state := range states {
			if activeQuestStatus(state.Status) {
				active[id] = struct{}{}
				activeStates[id] = state
			}
		}
		eligible := catalog.Eligible(active)
		if len(eligible) == 0 {
			return ErrEligibility
		}
		charge := catalog.config.Charge
		gold := c.Stats["gold"]
		if gold < 0 || gold > math.MaxUint32 {
			return ErrGold
		}
		// Include the active state/version as well as eligible targets so an
		// abandon/reaccept or task change cannot revive an old authorization.
		encoded, err := json.Marshal(struct {
			Account, Character string
			Binding            Binding
			Scene              Scene
			Charge             int64
			Active             map[int64]dnfrepo.QuestState
			Updated            time.Time
			Targets            []Target
		}{cmd.AccountID, cmd.CharacterID, cmd.Binding, scene, charge, activeStates, q.UpdatedAt, eligible})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		result = Result{Charge: charge, CurrentGold: gold, WalletKnown: true}
		if version, exists := c.Stats[receiptPrefix+"version"]; exists {
			if version != 1 {
				return ErrReceipt
			}
			for _, key := range []string{"hash0", "hash1", "hash2", "hash3", "issued_ms", "expires_ms", "charge", "gold"} {
				if _, exists := c.Stats[receiptPrefix+key]; !exists {
					return ErrReceipt
				}
			}
		}
		matches := c.Stats[receiptPrefix+"version"] == 1
		for i := 0; i < 4; i++ {
			matches = matches && c.Stats[fmt.Sprintf("%shash%d", receiptPrefix, i)] == int64(binary.LittleEndian.Uint64(digest[i*8:]))
		}
		if matches {
			issued, expires := c.Stats[receiptPrefix+"issued_ms"], c.Stats[receiptPrefix+"expires_ms"]
			priorGold, hasGold := c.Stats[receiptPrefix+"gold"]
			if expires-issued != PermitLifetime.Milliseconds() || !hasGold || priorGold < 0 || priorGold > math.MaxUint32 || c.Stats[receiptPrefix+"charge"] != charge {
				return ErrReceipt
			}
			if cmd.Now.UnixMilli() < issued {
				return ErrReceipt
			}
			if cmd.Now.UnixMilli() < expires {
				result.Gold, result.Replayed = priorGold, true
				return nil
			}
		}
		if gold < charge {
			return ErrGold
		}
		updated := dnfrepo.CloneCharacter(c)
		if updated.Stats == nil {
			updated.Stats = map[string]int64{}
		}
		result.Gold, result.CurrentGold = gold-charge, gold-charge
		updated.Stats["gold"] = result.Gold
		updated.Stats[receiptPrefix+"version"] = 1
		updated.Stats[receiptPrefix+"issued_ms"] = cmd.Now.UnixMilli()
		updated.Stats[receiptPrefix+"expires_ms"] = cmd.Now.Add(PermitLifetime).UnixMilli()
		updated.Stats[receiptPrefix+"charge"] = charge
		updated.Stats[receiptPrefix+"gold"] = result.Gold
		for i := 0; i < 4; i++ {
			updated.Stats[fmt.Sprintf("%shash%d", receiptPrefix, i)] = int64(binary.LittleEndian.Uint64(digest[i*8:]))
		}
		updated.UpdatedAt = cmd.Now.UTC()
		return dnfrepo.SaveCharacterFields(ctx, characters, updated, dnfrepo.CharacterFieldStats)
	})
	if err != nil && !errors.Is(err, ErrGold) {
		return Result{}, err
	}
	return result, err
}

// Preserve the legacy aliases recognized by the quest owner and town gate.
// The directory receives active membership, not a raw status spelling.
func normalizedQuestStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(value)
}

func activeQuestStatus(value string) bool {
	switch normalizedQuestStatus(value) {
	case "active", "accepted", "inprogress", "progress":
		return true
	}
	return false
}

func completedQuestStatus(value string) bool {
	switch normalizedQuestStatus(value) {
	case "complete", "completed", "cleared", "finished", "done":
		return true
	}
	return false
}
