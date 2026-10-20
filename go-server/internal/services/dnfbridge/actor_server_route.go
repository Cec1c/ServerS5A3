package dnfbridge

import "fmt"

// The native actor key contains the advertised server and the channel. This
// runtime server is distinct from a character's persisted social server_group_id.
// Paired EXE 2af985b6 compares the mode0 prefix with its CHANNELINFO identity at
// RVA 0x01ec5043 / 0x06a83a50 and skips local actor creation on a mismatch.
func (s *Service) currentSceneActorRoute() byte {
	if s == nil {
		return currentSceneObjectRoute
	}
	// Start rejects values outside the client's supported server range before
	// opening listeners; no live configuration reload changes this identity.
	return byte(s.channelAdvertiseID())
}

func (s *Service) validateCurrentSceneActorRoute() error {
	id := s.channelAdvertiseID()
	if id < 0 || id >= 250 {
		return fmt.Errorf("advertised server %d is outside native actor server range 0..249", id)
	}
	return nil
}
