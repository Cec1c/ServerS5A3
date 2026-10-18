package dnfbridge

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sync"
)

// Native upper and legacy packets share a connection-wide uint16 sequence.
// Observe both paths so intervening traffic and rollover do not make two legitimate
// purchases share an identity. The serial window is half the wire space.
type guildPurchaseSequence struct {
	mu          sync.Mutex
	initialized bool
	high        uint64
	nonce       []byte
}

func (s *guildPurchaseSequence) observe(wire uint16) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		s.initialized = true
		s.high = 1<<16 | uint64(wire)
		return s.high
	}
	delta := int16(wire - uint16(s.high))
	if delta > 0 {
		s.high += uint64(delta)
	}
	if delta < 0 {
		return s.high - uint64(-int64(delta))
	}
	return s.high
}

// currentHigh returns the connection-wide high watermark without advancing it.
// New event owners use this to distinguish the packet being dispatched now from
// an older, already-seen wire value.
func (s *guildPurchaseSequence) currentHigh() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.high, s.initialized
}

func (s *guildPurchaseSequence) identity(serial uint64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonce == nil {
		s.nonce = make([]byte, 32)
		if _, err := rand.Read(s.nonce); err != nil {
			s.nonce = nil
			return "", err
		}
	}
	var data [40]byte
	copy(data[:32], s.nonce)
	binary.LittleEndian.PutUint64(data[32:], serial)
	sum := sha256.Sum256(data[:])
	return hex.EncodeToString(sum[:]), nil
}
