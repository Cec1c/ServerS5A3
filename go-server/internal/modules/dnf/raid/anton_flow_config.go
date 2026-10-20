package raid

import (
	"fmt"
	"math"

	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
)

// Keep the real phase/reward/charge timing in the admission snapshot. No
// jumping-server configuration or hard-coded timing fallback is accepted.
type AntonFlowConfiguration struct {
	PhaseBreakSeconds     uint32
	MovieEndSeconds       [4]uint32
	ShieldChargePerSecond [2]uint32
}

func LoadAntonFlowConfiguration(source dnfpvf.Source) (AntonFlowConfiguration, error) {
	return loadTwoPhaseRaidFlowConfiguration(source, "etc/raid/anton.etc", false)
}

func LoadLukeFlowConfiguration(source dnfpvf.Source) (AntonFlowConfiguration, error) {
	return loadTwoPhaseRaidFlowConfiguration(source, "etc/raid/luke/luke.etc", true)
}

func loadTwoPhaseRaidFlowConfiguration(source dnfpvf.Source, path string, luke bool) (AntonFlowConfiguration, error) {
	text, err := source.ReadText(path)
	if err != nil {
		return AntonFlowConfiguration{}, err
	}
	doc, err := dnfpvf.Parse(path, text)
	if err != nil {
		return AntonFlowConfiguration{}, err
	}
	breakTime, hasBreak := doc.Int("PHASE BREAK TIME")
	movies, charges := doc.Ints("MOVIE END TIME"), doc.Ints("SHIELD CHARGE RATE")
	if !hasBreak || breakTime <= 0 || breakTime > math.MaxUint32 || len(movies) != 4 || (!luke && len(charges) != 2) || (luke && len(charges) != 0) {
		return AntonFlowConfiguration{}, fmt.Errorf("Anton reward/break/charge timing unavailable")
	}
	config := AntonFlowConfiguration{PhaseBreakSeconds: uint32(breakTime)}
	for i, value := range movies {
		if value <= 0 || value > math.MaxUint32 {
			return AntonFlowConfiguration{}, fmt.Errorf("Anton movie timing invalid")
		}
		config.MovieEndSeconds[i] = uint32(value)
	}
	for i, value := range charges {
		if value <= 0 || value > 10000 {
			return AntonFlowConfiguration{}, fmt.Errorf("Anton shield rate invalid")
		}
		config.ShieldChargePerSecond[i] = uint32(value)
	}
	return config, nil
}
