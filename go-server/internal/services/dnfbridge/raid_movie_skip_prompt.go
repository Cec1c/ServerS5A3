package dnfbridge

import (
	"fmt"
	"time"

	"longheng.io/server/internal/modules/dnf/raid"
)

// NOTI599 operates only on the receiving client's movie controller. Fixed
// v157's complete handler and common NOTI tail read no body. The native prompt
// callback can later send 659/1; merely sending this prompt is not completion.
func (s *Service) sendCurrentRaidMovieSkipPrompt(session *gameSession, captured runtimeRaidState, op uint16) error {
	o := &session.raidProjection
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failed {
		return fmt.Errorf("raid movie transport previously failed")
	}
	state, err := s.latestCurrentRaidProjectionState(session, captured)
	if err != nil {
		return err
	}
	b := state.Battle
	identity := currentRaidProjectionIdentity{InstanceID: b.InstanceID, Phase: b.Phase, CharID: session.selectedCharacterID, Generation: session.characterGeneration}
	if !o.resultSent || o.resultIdentity != identity || !s.currentRaidAuthenticated(session) {
		return raid.ErrRewardOwnership
	}
	if o.movieIdentity != identity {
		o.movieIdentity, o.moviePromptSent, o.movieCompleted = identity, false, false
		o.moviePromptAt = time.Time{}
	}
	packets := []currentAntonRewardPacket{{opcode: op, class: 1, body: []byte{1}}}
	now := s.currentRaidFailurePresentationNow()
	// NOTI599's native movie prompt silently does nothing until its movie
	// window exists. Failure input can arrive before that window is ready;
	// a successful socket write is not a client completion receipt. Allow a
	// bounded retry on later input, without changing the failure/return owner.
	retry := b.TimedOut && o.moviePromptSent && !now.Before(o.moviePromptAt.Add(time.Second))
	prompt := !o.movieCompleted && (!o.moviePromptSent || retry)
	if prompt {
		packets = append(packets, currentAntonRewardPacket{opcode: 599})
	}
	for _, packet := range packets {
		if err := s.sendGameUpperRawClassCodec(session, packet.opcode, packet.body, packet.class, true); err != nil {
			// A partially written frame cannot be retried on this stream. A new
			// connection has its own prompt state and may request it afresh.
			o.failed = true
			if session.conn != nil {
				_ = session.conn.Close()
			}
			return err
		}
		if packet.opcode == 599 {
			o.moviePromptSent = true
			o.moviePromptAt = now
		}
	}
	s.logInfo("dnfbridge raid movie stage", "kind", "game-raid-movie-stage", "char_id", identity.CharID, "instance_id", identity.InstanceID, "mode", b.Mode, "phase", b.Phase, "stage", 0, "prompt_written", prompt)
	return nil
}
