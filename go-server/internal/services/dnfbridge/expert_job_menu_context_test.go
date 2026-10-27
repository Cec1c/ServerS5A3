package dnfbridge

import (
	"context"
	"reflect"
	"testing"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// These are menu-admission tests, not enchant settlement or PVF fixtures. The
// repository boundary deliberately reports unavailable: invalid contexts must
// never reach it, while an admitted town self-use must reach it and fail closed.
func TestCurrentExpertJobMenuEnchantContextRejectsBeforeRepository(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Service, **gameSession, *uint16)
	}{
		{name: "nil_session", mutate: func(_ *Service, session **gameSession, _ *uint16) { *session = nil }},
		{name: "zero_selected_character", mutate: func(_ *Service, session **gameSession, _ *uint16) {
			(*session).selectedCharacterID = 0
		}},
		{name: "zero_owner", mutate: func(_ *Service, _ **gameSession, owner *uint16) { *owner = 0 }},
		{name: "other_owner", mutate: func(_ *Service, _ **gameSession, owner *uint16) { *owner = 73 }},
		{name: "no_online_manager", mutate: func(s *Service, _ **gameSession, _ *uint16) { s.onlinePlayers = nil }},
		{name: "not_online", mutate: func(s *Service, _ **gameSession, _ *uint16) {
			s.onlinePlayers = newOnlinePlayerManager()
		}},
		{name: "old_session_same_character_and_connection_id", mutate: func(s *Service, session **gameSession, owner *uint16) {
			// Matching IDs do not confer ownership on a replaced session pointer.
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: *owner, TownID: 38,
				Session: &gameSession{selectedCharacterID: *owner, connID: (*session).connID}})
		}},
		{name: "online_entry_without_session", mutate: func(s *Service, _ **gameSession, owner *uint16) {
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: *owner, TownID: 38})
		}},
		{name: "zero_town", mutate: func(s *Service, session **gameSession, owner *uint16) {
			s.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: *owner, Session: *session, TownID: 0})
		}},
		{name: "dungeon_runtime_present", mutate: func(_ *Service, session **gameSession, _ *uint16) {
			(*session).dungeon.runtime = &runtimeDungeonState{}
		}},
		{name: "visitor_of_other_store", mutate: func(s *Service, session **gameSession, _ *uint16) {
			s.expertJobVisitors[*session] = 73
		}},
		{name: "visitor_of_self_store", mutate: func(s *Service, session **gameSession, owner *uint16) {
			s.expertJobVisitors[*session] = *owner
		}},
		{name: "visitor_entry_with_zero_owner", mutate: func(s *Service, session **gameSession, _ *uint16) {
			// A present zero-valued association must not become menu admission.
			s.expertJobVisitors[*session] = 0
		}},
		{name: "own_enchant_store_registered", mutate: func(s *Service, session **gameSession, owner *uint16) {
			s.expertJobStores[*owner] = &currentExpertJobStore{OwnerCharacterID: *owner,
				OwnerSession: *session, Kind: currentExpertJobEnchantStoreKind, TownID: 38}
		}},
		{name: "own_disjoint_store_registered", mutate: func(s *Service, session **gameSession, owner *uint16) {
			s.expertJobStores[*owner] = &currentExpertJobStore{OwnerCharacterID: *owner,
				OwnerSession: *session, Kind: currentExpertJobDisjointStoreKind, TownID: 38}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, session, repositoryCalls := newExpertJobMenuContextBoundaryFixture()
			owner := session.selectedCharacterID
			test.mutate(service, &session, &owner)
			beforeStores, beforeVisitors := expertJobMenuContextRegistrations(service)
			store, err := service.resolveCurrentExpertJobMenuEnchantStore(context.Background(), session, owner)
			if err == nil || store != nil {
				t.Fatalf("invalid menu context admitted: store=%+v err=%v", store, err)
			}
			if *repositoryCalls != 0 {
				t.Fatalf("invalid menu context accessed repository %d times", *repositoryCalls)
			}
			assertExpertJobMenuContextRegistrations(t, service, beforeStores, beforeVisitors)
		})
	}
}

func TestCurrentExpertJobMenuEnchantContextTownSelfReachesRepository(t *testing.T) {
	for _, name := range []string{"plain_town", "unrelated_store_and_visitor"} {
		t.Run(name, func(t *testing.T) {
			service, session, repositoryCalls := newExpertJobMenuContextBoundaryFixture()
			if name == "unrelated_store_and_visitor" {
				peer := &gameSession{selectedCharacterID: 73}
				service.expertJobStores[73] = &currentExpertJobStore{OwnerCharacterID: 73,
					OwnerSession: peer, Kind: currentExpertJobEnchantStoreKind, TownID: 38}
				service.expertJobVisitors[peer] = 73
			}
			beforeStores, beforeVisitors := expertJobMenuContextRegistrations(service)
			store, err := service.resolveCurrentExpertJobMenuEnchantStore(context.Background(), session, session.selectedCharacterID)
			if *repositoryCalls != 1 {
				t.Fatalf("valid online town self-use must reach repository once, got %d (store=%+v err=%v)", *repositoryCalls, store, err)
			}
			if err == nil || store != nil {
				t.Fatalf("unavailable authoritative repository must fail closed: store=%+v err=%v", store, err)
			}
			assertExpertJobMenuContextRegistrations(t, service, beforeStores, beforeVisitors)
		})
	}
}

func newExpertJobMenuContextBoundaryFixture() (*Service, *gameSession, *int) {
	session := &gameSession{selectedCharacterID: 72, connID: "menu-context-72", accountID: "menu-context-account"}
	repositoryCalls := new(int)
	service := &Service{
		onlinePlayers:     newOnlinePlayerManager(),
		expertJobStores:   make(map[uint16]*currentExpertJobStore),
		expertJobVisitors: make(map[*gameSession]uint16),
		repositoryProvider: func() (dnfrepo.Group, bool) {
			*repositoryCalls++
			return dnfrepo.Group{}, false
		},
	}
	service.onlinePlayers.EnterArea(&onlinePlayerInfo{CharacterID: 72, Session: session, TownID: 38, AreaID: 0})
	return service, session, repositoryCalls
}

func expertJobMenuContextRegistrations(s *Service) (map[uint16]currentExpertJobStore, map[*gameSession]uint16) {
	stores := make(map[uint16]currentExpertJobStore, len(s.expertJobStores))
	for id, store := range s.expertJobStores {
		stores[id] = *cloneCurrentExpertJobStore(store)
	}
	visitors := make(map[*gameSession]uint16, len(s.expertJobVisitors))
	for session, owner := range s.expertJobVisitors {
		visitors[session] = owner
	}
	return stores, visitors
}

func assertExpertJobMenuContextRegistrations(t *testing.T, s *Service, stores map[uint16]currentExpertJobStore, visitors map[*gameSession]uint16) {
	t.Helper()
	afterStores, afterVisitors := expertJobMenuContextRegistrations(s)
	if !reflect.DeepEqual(afterStores, stores) || !reflect.DeepEqual(afterVisitors, visitors) {
		t.Fatalf("menu context changed store/visitor registration: stores=%+v visitors=%v", afterStores, afterVisitors)
	}
}
