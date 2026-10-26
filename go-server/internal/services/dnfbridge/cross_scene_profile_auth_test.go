package dnfbridge

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"longheng.io/server/internal/modules/dnf/alignedcmd"
	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/party"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
	"longheng.io/server/internal/modules/dnf/worldmap"
)

// Reuse the central-party fixture; its repositories and resource tables are
// in-memory. No live PVF, gameplay entry or packet-sending hook is needed here.
func TestCrossSceneProfileCentralPartyIgnoresSceneAndRun(t *testing.T) {
	for _, kind := range []string{"different_run", "town_and_dungeon", "empty_session_caches", "older_party_generation", "full_channel_id"} {
		t.Run(kind, func(t *testing.T) {
			f := newPeerUserInfoActiveDungeonFixture(t)
			switch kind {
			case "different_run":
				f.target.dungeonPartyRunMu.Lock()
				f.target.dungeonPartyRun.Seed++
				f.target.dungeonPartyRun.RoomX++
				f.target.dungeonPartyRunMu.Unlock()
			case "town_and_dungeon":
				f.source.dungeon.mu.Lock()
				f.source.dungeon.runtime = nil
				f.source.dungeon.mu.Unlock()
			case "empty_session_caches":
				storeRuntimePartyState(f.source, alignedcmd.PartyState{})
				storeRuntimePartyState(f.target, alignedcmd.PartyState{})
			case "older_party_generation":
				// Membership is character-keyed (SnapshotByUser(id, 0)); the
				// current socket is checked separately by the bridge.
				f.service.mu.Lock()
				f.source.characterGeneration++
				f.target.characterGeneration++
				f.service.mu.Unlock()
			case "full_channel_id":
				f.source.channel.ID, f.target.channel.ID = 508, 508
			}
			if f.service.onlinePlayers.PeerInSameArea(1001, 1002) {
				t.Fatal("fixture must not authorize through same-area presence")
			}
			if !f.service.currentCrossSceneProfileAllowed(context.Background(), f.source, f.target, 1001, 1002) {
				t.Fatal("current central party members were rejected")
			}
			if !f.service.currentCrossSceneProfileAllowed(context.Background(), f.target, f.source, 1002, 1001) {
				t.Fatal("central party authorization was not symmetric")
			}
		})
	}
}

func TestCrossSceneProfileRejectsInvalidIdentityOrCachedParty(t *testing.T) {
	for _, kind := range []string{"nil_service", "nil_source", "nil_target", "same_session", "zero_source", "zero_target", "same_id", "wrong_source_id", "wrong_target_id", "legacy", "channel", "channel_alias", "zero_channel", "empty_source_account", "empty_target_account", "stale_source", "stale_target", "cache_only", "different_central_parties", "missing_repository", "missing_character_repository"} {
		t.Run(kind, func(t *testing.T) {
			f := newPeerUserInfoActiveDungeonFixture(t)
			s, source, target := f.service, f.source, f.target
			sourceID, targetID := uint16(1001), uint16(1002)
			switch kind {
			case "nil_service":
				s = nil
			case "nil_source":
				source = nil
			case "nil_target":
				target = nil
			case "same_session":
				target = source
			case "zero_source":
				sourceID = 0
			case "zero_target":
				targetID = 0
			case "same_id":
				targetID = sourceID
			case "wrong_source_id":
				sourceID = 7
			case "wrong_target_id":
				targetID = 7
			case "legacy":
				s.options.currentClientVersion = "90cn-decode-bypass-v1"
			case "channel":
				target.channel.ID++
			case "channel_alias":
				target.channel.ID += 256
			case "zero_channel":
				source.channel.ID, target.channel.ID = 0, 0
				source.connectionTownActorOwnerChannel, target.connectionTownActorOwnerChannel = 0, 0
			case "empty_source_account":
				source.accountID = ""
			case "empty_target_account":
				target.accountID = " \t"
			case "stale_source", "stale_target":
				id := sourceID
				if kind == "stale_target" {
					id = targetID
				}
				s.mu.Lock()
				delete(s.gameSessions, id)
				s.mu.Unlock()
			case "cache_only":
				// Both stale local caches still claim the original party.
				s.runtimePartyManager = party.NewRuntimePartyManager()
			case "different_central_parties":
				manager := s.runtimePartyManagerForService()
				if result := manager.LeaveCurrent(targetID); !result.OK {
					t.Fatal(result.Reason)
				}
				if result := manager.Create(party.RuntimePartyMember{UserID: targetID, SessionGeneration: target.characterGeneration}, alignedcmd.PartyState{MaxMembers: 4}); !result.OK {
					t.Fatal(result.Reason)
				}
			case "missing_repository":
				s.repositoryProvider = nil
			case "missing_character_repository":
				s.repositoryProvider = func() (dnfrepo.Group, bool) { return dnfrepo.Group{}, true }
			}
			if s.currentCrossSceneProfileAllowed(context.Background(), source, target, sourceID, targetID) {
				t.Fatal("invalid profile authorization accepted")
			}
		})
	}
}

type crossSceneProfileCharacterReader struct {
	dnfrepo.CharacterRepository
	load func(context.Context, string) (dnfrepo.CharacterRecord, bool, error)
}

func (r crossSceneProfileCharacterReader) Load(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
	return r.load(ctx, id)
}

func TestCrossSceneProfileRequiresExactRepositoryOwners(t *testing.T) {
	for _, id := range []string{"1001", "1002"} {
		for _, kind := range []string{"error", "missing", "character_id", "account_id", "padded_character_id", "padded_account_id"} {
			t.Run(id+"/"+kind, func(t *testing.T) {
				f := newPeerUserInfoActiveDungeonFixture(t)
				g := f.repositories
				base := g.Character
				g.Character = crossSceneProfileCharacterReader{CharacterRepository: base, load: func(ctx context.Context, key string) (dnfrepo.CharacterRecord, bool, error) {
					row, found, err := base.Load(ctx, key)
					if key == id {
						switch kind {
						case "error":
							err = errors.New("character load failed")
						case "missing":
							found = false
						case "character_id":
							row.CharacterID = "7"
						case "account_id":
							row.AccountID = "foreign"
						case "padded_character_id":
							row.CharacterID += " "
						case "padded_account_id":
							row.AccountID += " "
						}
					}
					return row, found, err
				}}
				f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return g, true }
				if f.service.currentCrossSceneProfileAllowed(context.Background(), f.source, f.target, 1001, 1002) {
					t.Fatal("invalid repository identity accepted")
				}
			})
		}
	}
}

// Inject transitions after the last Load has read its valid row. This checks
// the authorization window without scheduling goroutines or sleeping.
func TestCrossSceneProfileRechecksAfterRepositoryReads(t *testing.T) {
	for _, kind := range []string{"source_generation", "target_generation", "source_replaced", "target_replaced", "source_account", "target_account", "source_channel", "target_channel", "both_channels", "source_leaves", "target_leaves"} {
		t.Run(kind, func(t *testing.T) {
			f := newPeerUserInfoActiveDungeonFixture(t)
			g := f.repositories
			base := g.Character
			loads := 0
			g.Character = crossSceneProfileCharacterReader{CharacterRepository: base, load: func(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
				loads++
				row, found, err := base.Load(ctx, id)
				if id == "1002" {
					s := f.service
					switch kind {
					case "source_generation", "target_generation":
						peer := f.source
						if kind == "target_generation" {
							peer = f.target
						}
						s.mu.Lock()
						peer.characterGeneration++
						s.mu.Unlock()
					case "source_replaced", "target_replaced":
						peer := f.source
						if kind == "target_replaced" {
							peer = f.target
						}
						// Model the canonical-directory transition only, without
						// unrelated bind callbacks issuing nested repository reads.
						s.mu.Lock()
						s.gameSessions[peer.selectedCharacterID] = &gameSession{
							conn:                &bufferConn{},
							channel:             peer.channel,
							accountID:           peer.accountID,
							selectedCharacterID: peer.selectedCharacterID,
							characterGeneration: peer.characterGeneration + 1,
						}
						s.mu.Unlock()
					case "source_account":
						f.source.accountID = "foreign"
					case "target_account":
						f.target.accountID = "foreign"
					case "source_channel":
						f.source.channel.ID++
					case "target_channel":
						f.target.channel.ID++
					case "both_channels":
						f.source.channel.ID++
						f.target.channel.ID++
					case "source_leaves", "target_leaves":
						leaver := uint16(1001)
						if kind == "target_leaves" {
							leaver = 1002
						}
						if result := s.runtimePartyManagerForService().LeaveCurrent(leaver); !result.OK {
							t.Fatal(result.Reason)
						}
					}
				}
				return row, found, err
			}}
			f.service.repositoryProvider = func() (dnfrepo.Group, bool) { return g, true }
			if f.service.currentCrossSceneProfileAllowed(context.Background(), f.source, f.target, 1001, 1002) {
				t.Fatal("authorization survived a change during repository reads")
			}
			if loads != 2 {
				t.Fatalf("loads=%d, want both ownership reads", loads)
			}
		})
	}
}

func newCrossSceneProfileRaidTest(t *testing.T) (*Service, *gameSession, *gameSession, *runtimeRaidState) {
	t.Helper()
	s, source, target, _, _, snapshot := prepareApprovedCurrentRaid(t)
	// Exercise the raid arm without any ordinary-party authorization.
	s.runtimePartyManager = party.NewRuntimePartyManager()
	return s, source, target, s.raids[snapshot.RaidKey]
}

func TestCrossSceneProfileRaidAllowsLukeDifferentGroupsAndRuns(t *testing.T) {
	s, source, target, live := newCrossSceneProfileRaidTest(t)
	s.raidMu.Lock()
	delete(s.raids, live.RaidKey)
	live.RaidKey = (live.RaidKey & 0xff00ffff) | (32 << 16)
	live.RouteOrRaidType = 1
	source.channel.ID, source.channel.Type = 32, dnfenum.HiddenRaidType
	target.channel = source.channel
	live.Members[0].GroupIndex, live.Members[1].GroupIndex = 1, 2
	live.Battle = &runtimeRaidBattle{ChannelID: 32, ChannelType: dnfenum.HiddenRaidType, MemberAccounts: map[uint16]string{source.selectedCharacterID: source.accountID, target.selectedCharacterID: target.accountID}}
	s.raids[live.RaidKey] = live
	s.raidMu.Unlock()
	for i, peer := range []*gameSession{source, target} {
		peer.dungeon.mu.Lock()
		peer.dungeon.runtime = &runtimeDungeonState{Session: &worldmap.DungeonSession{}, raidEntry: &currentRaidDungeonEntry{RaidKey: live.RaidKey, RunID: strconv.Itoa(i)}}
		peer.dungeon.mu.Unlock()
	}
	for _, peer := range []*gameSession{source, target} {
		if !s.currentCrossSceneProfileAllowed(context.Background(), source, target, source.selectedCharacterID, target.selectedCharacterID) ||
			!s.currentCrossSceneProfileAllowed(context.Background(), target, source, target.selectedCharacterID, source.selectedCharacterID) {
			t.Fatal("same Luke raid rejected different groups/runs or mixed town/dungeon")
		}
		peer.dungeon.mu.Lock()
		peer.dungeon.runtime = nil
		peer.dungeon.mu.Unlock()
	}
}

func TestCrossSceneProfileRaidContextAndFinalMembership(t *testing.T) {
	for _, kind := range []string{"valid", "legacy_layout", "zero_key", "foreign_server", "channel_alias", "source_route", "target_route", "nonmember", "split_raids", "battle_channel", "battle_type", "source_battle_account", "target_battle_account", "leave_during_load", "context_during_load"} {
		t.Run(kind, func(t *testing.T) {
			s, source, target, live := newCrossSceneProfileRaidTest(t)
			sourceID, targetID := source.selectedCharacterID, target.selectedCharacterID
			live.Battle = &runtimeRaidBattle{ChannelID: source.channel.ID, ChannelType: source.channel.Type, MemberAccounts: map[uint16]string{sourceID: source.accountID, targetID: target.accountID}}
			switch kind {
			case "legacy_layout":
				live.CurrentLayout = false
			case "zero_key":
				live.RaidKey = 0
			case "foreign_server":
				s.options.channelAdvertiseID = 7
			case "channel_alias":
				source.channel.ID += 256
				target.channel.ID += 256
			case "source_route":
				source.channel.Type = dnfenum.HiddenRaidType
			case "target_route":
				target.channel.Type = dnfenum.HiddenRaidType
			case "nonmember", "split_raids":
				if kind == "split_raids" {
					other := cloneRuntimeRaidState(*live)
					other.RaidKey++
					other.Members = other.Members[1:]
					s.raids[other.RaidKey] = &other
				}
				live.Members = live.Members[:1]
			case "battle_channel":
				live.Battle.ChannelID++
			case "battle_type":
				live.Battle.ChannelType = 11
			case "source_battle_account":
				live.Battle.MemberAccounts[sourceID] = "foreign"
			case "target_battle_account":
				live.Battle.MemberAccounts[targetID] = "foreign"
			}
			g, _ := s.repositoryGroup()
			base := g.Character
			loads := 0
			g.Character = crossSceneProfileCharacterReader{CharacterRepository: base, load: func(ctx context.Context, id string) (dnfrepo.CharacterRecord, bool, error) {
				if !s.raidMu.TryLock() {
					t.Fatal("repository read occurred under raidMu")
				}
				s.raidMu.Unlock()
				loads++
				row, found, err := base.Load(ctx, id)
				if id == strconv.Itoa(int(targetID)) {
					s.raidMu.Lock()
					if kind == "leave_during_load" {
						live.Members = live.Members[:1]
					}
					if kind == "context_during_load" {
						live.CurrentLayout = false
					}
					s.raidMu.Unlock()
				}
				return row, found, err
			}}
			s.repositoryProvider = func() (dnfrepo.Group, bool) { return g, true }
			if got := s.currentCrossSceneProfileAllowed(context.Background(), source, target, sourceID, targetID); got != (kind == "valid") {
				t.Fatalf("raid authorization=%t", got)
			}
			if (kind == "valid" || kind == "leave_during_load" || kind == "context_during_load") && loads != 2 {
				t.Fatalf("loads=%d, want both ownership reads", loads)
			}
		})
	}
}

func TestCrossSceneProfileMembershipRecheckNeedsNoRepository(t *testing.T) {
	for _, raidMember := range []bool{false, true} {
		t.Run(strconv.FormatBool(raidMember), func(t *testing.T) {
			var s *Service
			var source, target *gameSession
			var live *runtimeRaidState
			if raidMember {
				s, source, target, live = newCrossSceneProfileRaidTest(t)
			} else {
				f := newPeerUserInfoActiveDungeonFixture(t)
				s, source, target = f.service, f.source, f.target
			}
			sourceID, targetID := source.selectedCharacterID, target.selectedCharacterID
			s.repositoryProvider = func() (dnfrepo.Group, bool) {
				t.Fatal("memory membership recheck accessed repositories")
				return dnfrepo.Group{}, false
			}
			if !s.currentCrossSceneProfileMembership(source, target, sourceID, targetID) {
				t.Fatal("current membership rejected")
			}
			if raidMember {
				s.raidMu.Lock()
				live.Members = live.Members[:1]
				s.raidMu.Unlock()
			} else if result := s.runtimePartyManagerForService().LeaveCurrent(targetID); !result.OK {
				t.Fatal(result.Reason)
			}
			if s.currentCrossSceneProfileMembership(source, target, sourceID, targetID) {
				t.Fatal("memory recheck accepted a departed member")
			}
		})
	}
}
