package dnfbridge

import (
	"os"
	"strings"

	"longheng.io/server/internal/modules/dnf/raid"
)

const antonModeSelectionEventEnv = "DNFBRIDGE_ANTON_MODE_SELECTION"

// The original UI's availability is independent of per-character used counts.
// Require both configured admission modes from the actual PVF. This advertises
// only the chooser; it neither changes entry cost/counts nor admits a raid.
func (s *Service) currentAntonModeSelectionAvailable(session *gameSession) bool {
	if s == nil || s.options.currentClientVersion != "official-24.9" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(antonModeSelectionEventEnv))) {
	case "off", "0", "false", "no", "disabled", "legacy":
		return false
	}
	s.initialEquipmentMu.Lock()
	source, err := s.initialEquipmentArchiveLocked()
	s.initialEquipmentMu.Unlock()
	if err == nil {
		// 01A55120 also checks this shared activity in Luke type 32. Require
		// its real two-mode admission rules before advertising the global bit.
		for _, path := range []string{"etc/raid/anton.etc", "etc/raid/luke/luke.etc"} {
			if _, err = raid.LoadAdmissionRule(source, path); err != nil {
				break
			}
		}
	}
	if err != nil {
		s.logGameEvent(session, "game-anton-mode-selection-availability", "event_id", currentAntonModeSelectionEventID, "available", false, "source", "actual_pvf_admission_rules", "error", err)
		return false
	}
	s.logGameEvent(session, "game-anton-mode-selection-availability", "event_id", currentAntonModeSelectionEventID, "available", true, "source", "actual_pvf_admission_rules", "native_selector", "01A55120", "window_id", 127)
	return true
}
