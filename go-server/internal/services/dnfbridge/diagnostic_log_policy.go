package dnfbridge

import (
	"strconv"
	"strings"
)

const (
	// The production default suppresses routine diagnostics while retaining slow and failed events.
	verboseDiagnosticsEnv                  = "DNFBRIDGE_VERBOSE_DIAGNOSTICS"
	townLoadingSlowConstructionThresholdUs = 20000
	maxSampledUnhandledOpcodes             = 256
)

func readVerboseDiagnosticsDisabled() (disabled bool, raw string, invalid bool) {
	raw = strings.TrimSpace(envString(verboseDiagnosticsEnv, ""))
	switch strings.ToLower(raw) {
	case "", "0", "false", "no", "off", "quiet", "summary":
		return true, raw, false
	case "1", "true", "yes", "on", "full", "verbose", "debug":
		return false, raw, false
	default:
		return true, raw, true
	}
}

// verboseDiagnosticsEnabled 在结构体零值（&Service{...} 单测与探针字面量）下为 true，
// 保持既有诊断断言逐字节兼容；经 optionsFromConfig 加载的生产配置默认关闭高频明细。
func (s *Service) verboseDiagnosticsEnabled() bool {
	return s != nil && !s.options.verboseDiagnosticsDisabled
}

func (s *Service) shouldLogUnhandledPacket(isUpper bool, cmd byte, opcode uint16) (allow bool, firstSeen bool) {
	if s == nil {
		return false, false
	}
	if s.verboseDiagnosticsEnabled() {
		return true, false
	}
	var upperFlag uint32
	if isUpper {
		upperFlag = 1
	}
	key := (upperFlag << 24) | (uint32(cmd) << 16) | uint32(opcode)
	if _, loaded := s.unhandledOpcodeSeen.Load(key); loaded {
		return false, false
	}
	if s.unhandledOpcodeCount.Load() >= maxSampledUnhandledOpcodes {
		return false, false
	}
	if _, loaded := s.unhandledOpcodeSeen.LoadOrStore(key, struct{}{}); loaded {
		return false, false
	}
	if s.unhandledOpcodeCount.Add(1) > maxSampledUnhandledOpcodes {
		s.unhandledOpcodeSeen.Delete(key)
		s.unhandledOpcodeCount.Add(-1)
		return false, false
	}
	return true, true
}

func (s *Service) logUnhandledGameCommand(cmd byte, typ uint16, bodyLen int, nameFn func() string, knownFn func() bool) {
	allow, firstSeen := s.shouldLogUnhandledPacket(false, cmd, typ)
	if !allow {
		return
	}
	if firstSeen {
		s.logInfo("dnfbridge unhandled latest game command",
			"cmd", cmd,
			"type", typ,
			"runtime_cmd_name", nameFn(),
			"runtime_cmd_known", knownFn(),
			"body_len", bodyLen,
			"sampled_first_seen", true)
		return
	}
	s.logInfo("dnfbridge unhandled latest game command",
		"cmd", cmd,
		"type", typ,
		"runtime_cmd_name", nameFn(),
		"runtime_cmd_known", knownFn(),
		"body_len", bodyLen)
}

func (s *Service) logUnhandledUpperPacket(classification byte, msgID uint16, bodyLen int, nameFn func() string, knownFn func() bool) {
	allow, firstSeen := s.shouldLogUnhandledPacket(true, classification, msgID)
	if !allow {
		return
	}
	if firstSeen {
		s.logInfo("dnfbridge unhandled latest upper packet",
			"msg_id", msgID,
			"runtime_cmd_name", nameFn(),
			"runtime_cmd_known", knownFn(),
			"body_len", bodyLen,
			"sampled_first_seen", true)
		return
	}
	s.logInfo("dnfbridge unhandled latest upper packet",
		"msg_id", msgID,
		"runtime_cmd_name", nameFn(),
		"runtime_cmd_known", knownFn(),
		"body_len", bodyLen)
}

func hasSlowTownConstructionField(fields []any) bool {
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok || key != "construction_us" {
			continue
		}
		if us, ok := diagnosticInt64Field(fields[i+1]); ok && us >= townLoadingSlowConstructionThresholdUs {
			return true
		}
	}
	return false
}

func isAbnormalRaidPeerDiagnostic(kind string, fields []any) bool {
	switch kind {
	case "game-party-entry-rejected",
		"game-peer-userinfo-profile-refresh-failed",
		"game-party-peer-endpoint-registration-rejected",
		"party-udp-relay-silent-peer-kick",
		"party-udp-relay-silent-peer-kick-failed":
		return true
	}
	if kind == fiendWarPartyAdmissionDiagnosticKind {
		for i := 0; i+1 < len(fields); i += 2 {
			if key, ok := fields[i].(string); ok && key == "reason" {
				reason, _ := fields[i+1].(string)
				return fiendWarPartyAdmissionFailure(reason)
			}
		}
	}
	if isPartyDungeonLoadingDiagnosticKind(kind) {
		for i := 0; i+1 < len(fields); i += 2 {
			if key, ok := fields[i].(string); ok && key == "error" && fields[i+1] != nil {
				return true
			}
		}
	}
	return false
}

func diagnosticInt64Field(v any) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int64:
		return val, true
	case int32:
		return int64(val), true
	case uint:
		return int64(val), true
	case uint64:
		return int64(val), true
	case uint32:
		return int64(val), true
	case string:
		parsed, err := strconv.ParseInt(val, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
