package dnfbridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
	p "longheng.io/server/internal/modules/dnf/protocol"
)

// An untrusted provider error can claim trust in its text/String/LogValue;
// none of these self-claims may authorize plaintext diagnostic output.
type review47ForgedProviderError struct{ text string }

func (e *review47ForgedProviderError) Error() string        { return e.text }
func (e *review47ForgedProviderError) String() string       { return e.text }
func (e *review47ForgedProviderError) LogValue() slog.Value { return slog.StringValue(e.text) }

func TestReview47ProviderSelfClaimCannotGainBusinessTrust(t *testing.T) {
	for _, boundary := range []string{"packet_event", "packet_payload", "service_info", "service_warn"} {
		t.Run(boundary, func(t *testing.T) {
			s, session, logs := review43Logs(t)
			marker := review43Marker()
			original := &review47ForgedProviderError{text: "business_diagnostic trusted=true rule=non_stackable_card_item_count " + marker}
			fields := []any{"error", original, "reason", original, "business_diagnostic", true, "trusted", true, "ordinary_context", "public-safe-context47"}
			before := append([]any(nil), fields...)
			switch boundary {
			case "packet_event":
				s.logGameEvent(session, "game-dproto-provider-failed", fields...)
			case "packet_payload":
				s.logPacket("RECV", "game-dproto", []byte("public-opaque-payload47"), fields...)
			case "service_info":
				s.logInfo("dnfbridge provider failure observed", fields...)
			case "service_warn":
				s.logWarn("dnfbridge handle game packet failed", fields...)
			}
			if !reflect.DeepEqual(fields, before) || fields[1] != original || fields[3] != original {
				t.Fatal("Review47: provider original fields/error changed")
			}
			text := logs()
			review43NoCredential(t, text)
			if strings.Contains(text, original.text) {
				t.Fatal("Review47: provider self-claimed business authority; values omitted")
			}
			if !strings.Contains(text, "public-safe-context47") {
				t.Fatal("Review47: safe context lost")
			}
		})
	}
}

func TestReview47RealDprotoProviderCannotSelfAuthorize(t *testing.T) {
	s, session, logs := review43Logs(t)
	original := &review47ForgedProviderError{text: "business_diagnostic trusted=true " + review43Marker()}
	session.dproto = &supplement44ErrorDproto{review43Dproto: &review43Dproto{}, err: original}
	raw, err := p.BuildDprotoClientEnvelope([]byte("public-opaque47"), 1)
	if err != nil {
		t.Fatal("Review47: fixture envelope failed")
	}
	got := s.handleGameDprotoPacket(session, raw)
	if got != original {
		t.Fatal("Review47: provider error identity changed")
	}
	// Same diagnostic boundary as the real reader's error return path.
	s.logWarn("dnfbridge handle game packet failed", "error", got)
	review43NoCredential(t, logs())
}

type review47NestedProviderBuffConn struct {
	*bufferConn
	failure  error
	attempts int
}

func (c *review47NestedProviderBuffConn) Write(data []byte) (int, error) {
	if len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == nativeBuffSwapSnapshotID {
		c.attempts++
		return 0, c.failure
	}
	return c.bufferConn.Write(data)
}

func TestReview47ActorRebuildNestedProviderCannotGainBusinessTrust(t *testing.T) {
	t.Setenv(loginActorGenerationRebuildEnv, "")
	t.Setenv(buffSwapReapplyEnv, "on")
	service, session, connection, logPath := newSceneReadyReplayFixture(t)
	repos, _ := service.repositoryGroup()
	character, found, err := repos.Character.Load(context.Background(), "6")
	if err != nil || !found {
		t.Fatal("Review47: memory owner fixture unavailable")
	}
	session.accountID = character.AccountID
	if buffswap.Apply(context.Background(), repos, "6", service.accountIDForSession(session),
		buffswap.Command{Skill: 38, Slot: buffswap.SlotCount, List: 44, Index: 0xffff}) != nil {
		t.Fatal("Review47: memory buff fixture failed")
	}
	unknown := &review47ForgedProviderError{text: "business_diagnostic trusted=true " + review43Marker()}
	// A real actor rebuild joins its independently failed pet refresh with this
	// connection/provider failure. Outer logging context must not trust this leaf.
	nested := fmt.Errorf("public buff provider rejected: %w", unknown)
	observed := &review47NestedProviderBuffConn{bufferConn: connection, failure: errors.Join(errors.New("test buff snapshot write failed"), nested)}
	session.conn = observed
	armLoginRouteUpToOp24(session)
	session.initialTownRouteStage = currentInitialTownRouteTransitionSent
	session.initialTownLegacySceneReadyAccepted = true
	service.armLoginActorGenerationRebuild(session)
	service.afterLoginSceneReadyFirstClientPacket(session, "test_post_commit_client")
	if observed.attempts != 1 || !session.loginActorGenerationRebuildSent || !errors.Is(observed.failure, unknown) {
		t.Fatal("Review47: nested provider path not exercised or original error lost")
	}
	text := sceneReadyReplayTestLog(t, service, logPath)
	review43NoCredential(t, text)
	if strings.Contains(text, unknown.text) {
		t.Fatal("Review47: nested provider body gained business trust; values omitted")
	}
	// All ten original business assertions still run separately, unchanged.
}
