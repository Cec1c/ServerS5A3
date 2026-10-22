package dnfbridge

import "strings"

// Only immutable identity values cross the Service lock boundary. The account
// accessor sees a detached literal, never a copied sync.Once or live session.
type buffOwnedSendIdentity struct {
	session          *gameSession
	character        uint16
	generation       uint64
	accountID        string
	rawAccountID     string
	accountFromToken bool
	loginPacketSeen  bool
	indexed          bool
	sharedFallback   bool
}

func (s *Service) buffOwnedSendIdentityMatchesLocked(identity buffOwnedSendIdentity) bool {
	session := identity.session
	if session == nil || session.selectedCharacterID != identity.character || session.characterGeneration != identity.generation || session.accountID != identity.rawAccountID || session.accountFromToken != identity.accountFromToken || session.loginPacketSeen != identity.loginPacketSeen {
		return false
	}
	current := s.gameSessions[identity.character]
	if identity.indexed || identity.generation > 0 {
		return current == session
	}
	// Historical zero-generation unindexed fixtures cannot override a live peer.
	return current == nil
}

func (s *Service) captureBuffOwnedSendIdentity(session *gameSession) (buffOwnedSendIdentity, bool) {
	if s == nil || session == nil {
		return buffOwnedSendIdentity{}, false
	}
	s.mu.Lock()
	character := session.selectedCharacterID
	current := s.gameSessions[character]
	if character == 0 || (current != nil && current != session) || (session.characterGeneration > 0 && current != session) {
		s.mu.Unlock()
		return buffOwnedSendIdentity{}, false
	}
	identity := buffOwnedSendIdentity{session: session, character: character, generation: session.characterGeneration, rawAccountID: session.accountID, accountFromToken: session.accountFromToken, loginPacketSeen: session.loginPacketSeen, indexed: current == session, sharedFallback: strings.TrimSpace(session.accountID) == "" && session.loginPacketSeen}
	s.mu.Unlock()
	// No login flag on this detached account literal: real-session WARN remains
	// once-only at the existing send site, outside Service.mu.
	// Strict account resolution requires the live token-authentication flag.
	// Preserve its captured value; never infer authentication from a nonempty ID.
	accountSnapshot := &gameSession{accountID: identity.rawAccountID, accountFromToken: identity.accountFromToken}
	identity.accountID = s.accountIDForSession(accountSnapshot)
	s.mu.Lock()
	valid := s.buffOwnedSendIdentityMatchesLocked(identity)
	s.mu.Unlock()
	return identity, valid
}

func (s *Service) buffOwnedSendIdentityCurrent(identity buffOwnedSendIdentity) bool {
	if s == nil || identity.session == nil {
		return false
	}
	s.mu.Lock()
	valid := s.buffOwnedSendIdentityMatchesLocked(identity)
	s.mu.Unlock()
	if !valid {
		return false
	}
	accountSnapshot := &gameSession{accountID: identity.rawAccountID, accountFromToken: identity.accountFromToken}
	account := s.accountIDForSession(accountSnapshot)
	s.mu.Lock()
	defer s.mu.Unlock()
	return account == identity.accountID && s.buffOwnedSendIdentityMatchesLocked(identity)
}
