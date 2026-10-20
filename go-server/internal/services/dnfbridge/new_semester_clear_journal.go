package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	oe "longheng.io/server/internal/modules/dnf/onlineevent"
	quest "longheng.io/server/internal/modules/dnf/quest"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
	"math/bits"
	"sort"
	"strconv"
	"sync"
	"time"
)

var errSemesterClearOwner = errors.New("new semester clear owner is unavailable")

// This journal has no packet or claim entrypoint. Its role scope, 06:00 day
// boundary and shared 28-day campaign are explicit local operating policy.
type currentSemesterMember struct {
	owner    repo.SemesterClearOwner
	identity boundGameSessionCharacter
	scene    *worldmap.DungeonSession
}
type currentSemesterRun struct {
	mu       sync.Mutex
	nonce    string
	campaign oe.EpicCampaign
	members  map[uint16]currentSemesterMember
	receipt  *semesterClearReceipt
	invalid  bool
}
type semesterClearReceipt struct {
	Version     int                       `json:"version"`
	Nonce       string                    `json:"nonce"`
	Campaign    oe.EpicCampaign           `json:"campaign"`
	DungeonID   int64                     `json:"dungeon_id"`
	MazeIndex   int                       `json:"maze_index"`
	MapID       int64                     `json:"map_id"`
	BossID      int64                     `json:"boss_id"`
	BossObject  uint32                    `json:"boss_object"`
	CompletedAt time.Time                 `json:"completed_at"`
	Owners      []repo.SemesterClearOwner `json:"owners"`
	Source      repo.SemesterClearOwner   `json:"source"`
}

func (s *Service) freezeCurrentSemesterRun(session *gameSession, r *runtimeDungeonState) error {
	if r == nil || r.Dungeon.ID != 6062 || !s.currentEpicBenefitsEnabled() {
		return nil
	}
	// An unauthenticated legacy/test selector remains an ordinary dungeon path;
	// it cannot create activity eligibility or a claim owner.
	if session == nil || !session.loginPacketSeen || !session.accountFromToken || session.accountID == "" {
		return nil
	}
	identity, ok := s.boundGameSessionCharacterSnapshot(session)
	if !ok || r.Request.DungeonID != 6062 || r.AccountID == "" || r.Character.AccountID != r.AccountID || r.Character.CharacterID != strconv.Itoa(int(identity.character)) {
		return errSemesterClearOwner
	}
	campaign, e := s.currentEpicCampaign(session)
	if e != nil {
		return e
	}
	if !campaign.Active(s.gameplayNow()) {
		return nil
	}
	var nonce [16]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	r.semesterRun = &currentSemesterRun{nonce: hex.EncodeToString(nonce[:]), campaign: campaign, members: map[uint16]currentSemesterMember{identity.character: {owner: repo.SemesterClearOwner{AccountID: r.AccountID, CharacterID: r.Character.CharacterID}, identity: identity, scene: r.Session}}}
	return nil
}

// Existing adoption holds source dungeon.mu but has released registry/instance
// locks here. We never acquire a peer dungeon lock. No service.mu is held while
// taking instance.mu or semesterRun.mu.
func attachCurrentSemesterPartyMember(session *gameSession, r *runtimeDungeonState) {
	if r == nil || r.semesterRun == nil || r.partyInstance == nil {
		return
	}
	own := r.semesterRun
	member := own.members[session.selectedCharacterID]
	instance := r.partyInstance
	instance.mu.Lock()
	if instance.semesterRun == nil {
		instance.semesterRun = own
	}
	shared := instance.semesterRun
	instance.mu.Unlock()
	shared.mu.Lock()
	defer shared.mu.Unlock()
	// Campaign differences cannot silently join a prior run's authorization.
	if !sameSemesterCampaign(shared.campaign, own.campaign) {
		own.invalid = true
		return
	}
	if shared.receipt != nil {
		own.invalid = true
		return
	}
	shared.members[member.identity.character] = member
	r.semesterRun = shared
}

func sameSemesterCampaign(a, b oe.EpicCampaign) bool {
	return a.Version == b.Version && a.Starts.Equal(b.Starts) && a.Ends.Equal(b.Ends) && a.CreatedAt.Equal(b.CreatedAt)
}

func semesterDayKey(c oe.EpicCampaign) string {
	return "event_new_semester_v1:" + c.Starts.UTC().Format("20060102T150405Z")
}
func semesterReceiptScope(nonce string) string { return "server:new-semester-clear:v1:" + nonce }

// Called under source dungeon.mu. The narrow run lock serializes completion
// and keeps the first receipt immutable across all peer/retry paths. service.mu
// only protects the immutable eligibility freeze. It is released before UOW,
// because existing activity UOW callbacks validate bindings under service.mu.
// The already accepted contribution survives a later disconnect; only the
// physically committed receipt is reward authority. No callback takes a peer lock.
func (s *Service) persistCurrentSemesterClear(ctx context.Context, session *gameSession, r *runtimeDungeonState, snapshot worldmap.DungeonSessionSnapshot) (bool, error) {
	if r.semesterRun == nil {
		return false, nil
	}
	if !s.currentEpicBenefitsEnabled() {
		return false, nil
	}
	run := r.semesterRun
	catalog, e := s.loadQuestCatalog(ctx)
	if e != nil {
		return true, e
	}
	g, ok := s.repositoryGroup()
	if !ok || g.SemesterClear == nil {
		return true, repo.ErrSemesterClearTransaction
	}
	// Snapshot actual active instance members before taking run.mu. A party
	// roster alone or a retained old DungeonSession cannot prove current presence.
	active := map[uint16]*worldmap.DungeonSession{session.selectedCharacterID: r.Session}
	if instance := r.partyInstance; instance != nil {
		active = map[uint16]*worldmap.DungeonSession{}
		instance.mu.Lock()
		for id := range instance.members {
			active[id] = instance.scenes[id]
		}
		instance.mu.Unlock()
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	decodedNonce, nonceErr := hex.DecodeString(run.nonce)
	if nonceErr != nil || len(decodedNonce) != 16 || run.campaign.Validate() != nil {
		return true, errSemesterClearOwner
	}
	if run.invalid {
		return true, errSemesterClearOwner
	}
	if r.Request.DungeonID != 6062 || r.Dungeon.ID != 6062 || snapshot.Run.DungeonID != 6062 || snapshot.Run.MazeIndex != r.MazeIndex || snapshot.Run.Status != worldmap.DungeonRunCompleted || snapshot.Scene.Map.Map.ID != 80488 || snapshot.Scene.Coordinate != (worldmap.RoomCoordinate{X: 2, Y: 0}) || !r.bossDieCheckAccepted || !dungeonRuntimeOwnsCharacter(r, session.selectedCharacterID) {
		return true, errSemesterClearOwner
	}
	var boss uint32
	for _, m := range r.Room.Snapshot().Monsters {
		if m.Definition.ID == 63851 && m.State == runtimeDungeonMonsterDefeated {
			if _, known := r.Room.DefeatedDeathTarget(m.ObjectKey); !known {
				continue
			}
			if body, known := r.Room.DeathResponseBody(m.ObjectKey); !known || len(body) == 0 {
				continue
			}
			if boss != 0 {
				return true, errSemesterClearOwner
			}
			boss = m.ObjectKey
		}
	}
	if boss == 0 {
		return true, errSemesterClearOwner
	}
	if !run.campaign.Active(r.clearMapCompletionAt) {
		return false, nil
	}
	s.mu.Lock()
	bindingLocked := true
	defer func() {
		if bindingLocked {
			s.mu.Unlock()
		}
	}()
	source := repo.SemesterClearOwner{AccountID: r.AccountID, CharacterID: r.Character.CharacterID}
	sourceMember, found := run.members[session.selectedCharacterID]
	if !found || sourceMember.owner != source || sourceMember.identity.session != session || sourceMember.identity.generation != session.characterGeneration || s.gameSessions[session.selectedCharacterID] != session || session.accountID != source.AccountID {
		return true, errSemesterClearOwner
	}
	if run.receipt == nil {
		owners := []repo.SemesterClearOwner{}
		for id, member := range run.members {
			if active[id] != member.scene {
				continue
			}
			// Only actual, still-bound final-room participants qualify. Scene reads use
			// DungeonSession's own mutex, not a peer runtime/dungeon mutex.
			if s.gameSessions[id] != member.identity.session || member.identity.session.characterGeneration != member.identity.generation || member.identity.session.selectedCharacterID != id || member.identity.session.accountID != member.owner.AccountID {
				continue
			}
			peer := member.scene.Snapshot()
			if peer.Run.DungeonID != 6062 || peer.Run.MazeIndex != r.MazeIndex || peer.Run.Status == worldmap.DungeonRunAbandoned || peer.Scene.Map.Map.ID != 80488 || peer.Scene.Coordinate != (worldmap.RoomCoordinate{X: 2, Y: 0}) {
				continue
			}
			if _, bound := peer.Scene.RuntimeObjects[boss]; !bound {
				continue
			}
			owners = append(owners, member.owner)
		}
		sort.Slice(owners, func(i, j int) bool { return owners[i].CharacterID < owners[j].CharacterID })
		if _, e = repo.SemesterClearOwners(source, owners, semesterReceiptScope(run.nonce)); e != nil {
			return true, e
		}
		run.receipt = &semesterClearReceipt{Version: 1, Nonce: run.nonce, Campaign: run.campaign, DungeonID: 6062, MazeIndex: r.MazeIndex, MapID: 80488, BossID: 63851, BossObject: boss, CompletedAt: r.clearMapCompletionAt, Owners: owners, Source: source}
	}
	receipt := *run.receipt
	if receipt.Version != 1 || receipt.DungeonID != 6062 || receipt.MapID != 80488 || receipt.BossID != 63851 || !run.campaign.Active(receipt.CompletedAt) || receipt.Nonce != run.nonce || !sameSemesterCampaign(receipt.Campaign, run.campaign) || receipt.BossObject != boss || receipt.MazeIndex != r.MazeIndex {
		return true, errSemesterClearOwner
	}
	// Frozen owners are checked again inside the physical tx against their rows.
	owners := receipt.Owners
	included := false
	for _, owner := range owners {
		if owner == source {
			included = true
		}
	}
	if !included {
		return true, errSemesterClearOwner
	}
	raw, e := json.Marshal(receipt)
	if e != nil {
		return true, e
	}
	s.mu.Unlock()
	bindingLocked = false
	scope := semesterReceiptScope(run.nonce)
	var result quest.ClearMapPersistResult
	var pending []int64
	e = g.SemesterClear.WithinSemesterClear(ctx, source, owners, scope, func(tx repo.Group) error {
		old, known, err := tx.Settings.Load(ctx, scope)
		if err != nil {
			return err
		}
		if known && old.Values["receipt"] != string(raw) {
			return errSemesterClearOwner
		}
		// Source quest work is inside the same physical tx even if receipt already
		// exists from another peer. No missing quest is synthesized.
		if _, hasQuest, err := tx.Quest.Load(ctx, source.CharacterID); err != nil {
			return err
		} else if hasQuest {
			owner, err := quest.NewOwner(tx)
			if err != nil {
				return err
			}
			result, err = owner.ApplyClearMapCompletion(ctx, catalog, source.CharacterID, quest.ClearMapCompletionInput{DungeonID: 6062, MapID: 80488, CompletionKey: r.clearMapCompletionKey, CompletedAt: r.clearMapCompletionAt})
			if err != nil {
				return err
			}
			persisted, _, err := tx.Quest.Load(ctx, source.CharacterID)
			if err != nil {
				return err
			}
			pending = currentDungeonClearMapPendingRewardQuestIDs(persisted, result.Completions)
		}
		if known {
			return nil
		}
		day := receipt.CompletedAt.In(oe.ChinaCalendar).Add(-6 * time.Hour)
		start := run.campaign.Starts.In(oe.ChinaCalendar).Add(-6 * time.Hour)
		dayDate := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, oe.ChinaCalendar)
		startDate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, oe.ChinaCalendar)
		ordinal := int(dayDate.Sub(startDate) / (24 * time.Hour))
		if ordinal < 0 || ordinal >= 28 {
			return errSemesterClearOwner
		}
		key := semesterDayKey(run.campaign)
		for _, participant := range owners {
			ch, _, err := tx.Character.Load(ctx, participant.CharacterID)
			if err != nil {
				return err
			}
			bitmap := ch.Stats[key+":day_bitmap"]
			if bitmap < 0 || bitmap >= (1<<28) {
				return errSemesterClearOwner
			}
			mask := uint32(bitmap) | (uint32(1) << uint(ordinal))
			if ch.Stats == nil {
				ch.Stats = map[string]int64{}
			}
			ch.Stats[key+":day_bitmap"] = int64(mask)
			ch.Stats[key+":qualified_days"] = int64(bits.OnesCount32(mask))
			ch.Stats[key+":version"] = 1
			if err = repo.SaveCharacterFields(ctx, tx.Character, ch, repo.CharacterFieldStats); err != nil {
				return err
			}
		}
		return tx.Settings.Save(ctx, repo.SettingsRecord{Scope: scope, Values: map[string]string{"receipt": string(raw)}, UpdatedAt: receipt.CompletedAt})
	})
	if e != nil {
		return true, fmt.Errorf("semester clear physical commit: %w", e)
	}
	r.clearMapCompletionQuestIDs = nil
	for _, completion := range result.Completions {
		r.clearMapCompletionQuestIDs = append(r.clearMapCompletionQuestIDs, completion.QuestID)
	}
	r.clearMapCompletionPendingQuestIDs = append([]int64(nil), pending...)
	r.clearMapCompletionPhaseAPersisted = true
	return true, nil
}
