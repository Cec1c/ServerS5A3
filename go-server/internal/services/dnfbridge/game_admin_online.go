package dnfbridge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"longheng.io/server/internal/modules/dnf/adminapi"
)

func (s *Service) administrativeOnlineSnapshot() (*adminapi.OnlineSnapshot, error) {
	if s == nil {
		return nil, errors.New("online sessions unavailable")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	out := &adminapi.OnlineSnapshot{ID: hex.EncodeToString(nonce[:]), Complete: true, Members: []adminapi.OnlineMember{}}
	s.mu.Lock()
	out.ObservedAt = time.Now().UTC()
	for id, session := range s.gameSessions {
		if session == nil {
			continue
		}
		if id == 0 || session.selectedCharacterID != id || session.characterGeneration == 0 {
			s.mu.Unlock()
			return nil, errors.New("online session identity unavailable")
		}
		if len(out.Members) == adminapi.MaxOnlineSnapshotMembers {
			s.mu.Unlock()
			return nil, errors.New("online session snapshot exceeds limit")
		}
		out.Members = append(out.Members, adminapi.OnlineMember{CharacterID: id, Generation: session.characterGeneration})
	}
	s.mu.Unlock()
	sort.Slice(out.Members, func(i, j int) bool { return out.Members[i].CharacterID < out.Members[j].CharacterID })
	return out, nil
}
