package dnfbridge

import (
	"errors"
	"fmt"
)

// This marker is created only before a profile frame is encoded. A repository
// or shared preparation deadline says nothing about this recipient's socket.
var errRaidProfilePreparation = errors.New("raid profile preparation unavailable")

func raidProfilePreparationError(err error) error {
	return fmt.Errorf("%w: %w", errRaidProfilePreparation, err)
}

// A requested refresh can be retried by the next native query. Do not publish
// the dependent roster after incomplete preparation, or retire a clean stream.
func (s *Service) currentRaidProfileRequestError(peer *gameSession, err error) error {
	if errors.Is(err, errRaidProfilePreparation) {
		s.logWarn("dnfbridge raid recipient projection deferred",
			"conn_id", peer.connID, "channel_id", peer.channel.ID,
			"char_id", selectedCharacterID(peer), "stage", "profile_preparation", "error", err)
		return nil
	}
	return err
}

// A committed raid transition may be broadcast by another member's command.
// Returning a recipient's socket error would close that command's healthy
// connection too. Retire only the failed stream (which may have a partial
// frame), keep the committed state, and continue delivering to other members.
func (s *Service) failRuntimeRaidRecipient(peer *gameSession, stage string, err error) {
	if peer == nil || err == nil {
		return
	}
	if errors.Is(err, errRaidProfilePreparation) {
		_ = s.currentRaidProfileRequestError(peer, err)
		return
	}
	peer.closeGameTransport()
	s.logWarn("dnfbridge raid recipient projection failed",
		"conn_id", peer.connID, "channel_id", peer.channel.ID,
		"char_id", selectedCharacterID(peer), "stage", stage, "error", err)
}
