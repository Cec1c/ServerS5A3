package mysql

import "os"

const mysqlExperimentalPeakEnv = "DNFBRIDGE_EXPERIMENTAL_PEAK"

// mysqlStoreEnvironment is captured once when a production router is built.
// Directly constructed test stores leave captured=false and keep the historical
// per-call environment semantics used by focused compatibility tests.
type mysqlStoreEnvironment struct {
	captured                 bool
	mirrorStatsAuthoritative bool
	experimentalPeak         bool
}

func captureMySQLStoreEnvironment() mysqlStoreEnvironment {
	return mysqlStoreEnvironment{
		captured:                 true,
		mirrorStatsAuthoritative: mysqlEnvSwitch(mysqlCharacterMirrorStatsAuthoritativeEnv, true),
		experimentalPeak:         os.Getenv(mysqlExperimentalPeakEnv) == "on",
	}
}

func (r mysqlRouter) environment() mysqlStoreEnvironment {
	if r.env.captured {
		return r.env
	}
	return captureMySQLStoreEnvironment()
}
