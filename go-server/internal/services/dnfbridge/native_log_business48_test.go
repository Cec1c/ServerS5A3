package dnfbridge

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"longheng.io/server/internal/modules/dnf/buffswap"
)

// Public synthetic marker/errors, never live credentials or provider output.
const business48PrivateMarker = "public-synthetic48-unknown-write-body-must-be-hashed"

type business48FailureConn struct {
	*bufferConn
	failure  error
	attempts int
}

func (c *business48FailureConn) Write(data []byte) (int, error) {
	if len(data) >= 3 && binary.LittleEndian.Uint16(data[1:3]) == nativeBuffSwapSnapshotID {
		c.attempts++
		return 0, c.failure
	}
	return c.bufferConn.Write(data)
}

func business48Fixture(t *testing.T, failure error) (*Service, *gameSession, *business48FailureConn, string) {
	t.Helper()
	t.Setenv(loginActorGenerationRebuildEnv, "")
	t.Setenv(buffSwapReapplyEnv, "on")
	s, session, buffer, logPath := newSceneReadyReplayFixture(t)
	repos, _ := s.repositoryGroup()
	character, found, err := repos.Character.Load(context.Background(), "6")
	if err != nil || !found {
		t.Fatal("memory character owner unavailable")
	}
	session.accountID = character.AccountID
	if buffswap.Apply(context.Background(), repos, "6", s.accountIDForSession(session),
		buffswap.Command{Skill: 38, Slot: buffswap.SlotCount, List: 44, Index: 0xffff}) != nil {
		t.Fatal("memory buff owner unavailable")
	}
	conn := &business48FailureConn{bufferConn: buffer, failure: failure}
	session.conn = conn
	return s, session, conn, logPath
}

func TestBusiness48RebuildPreservesReturnedNestedErrorAndOriginHashes(t *testing.T) {
	var typedNil *os.PathError
	cause := errors.New(business48PrivateMarker)
	ordinary := errors.New("public48-ordinary-write-failure")
	cases := []struct {
		name    string
		failure error
		leaf    error
	}{
		{"plain_unknown", cause, cause},
		{"wrapped_unknown", fmt.Errorf("public48 wrapper: %w", cause), cause},
		{"joined_unknown", errors.Join(ordinary, fmt.Errorf("public48 wrapper: %w", cause)), cause},
		{"typed_nil", typedNil, typedNil},
		{"panic_error", &supplement45PanicError{cause: cause}, cause},
	}
	for _, tc := range cases {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after_%t", tc.name, after), func(t *testing.T) {
				s, session, conn, path := business48Fixture(t, tc.failure)
				original := conn.failure
				if after {
					armLoginRouteUpToOp24(session)
					session.initialTownRouteStage = currentInitialTownRouteTransitionSent
					session.initialTownLegacySceneReadyAccepted = true
					s.armLoginActorGenerationRebuild(session)
					s.afterLoginSceneReadyFirstClientPacket(session, "public48_scene_commit")
					written := conn.write.Len()
					s.armLoginActorGenerationRebuild(session)
					s.afterLoginSceneReadyFirstClientPacket(session, "public48_duplicate_scene")
					if conn.write.Len() != written || !session.loginActorGenerationRebuildSent {
						t.Fatal("failed refresh changed duplicate/latch behavior")
					}
				} else {
					got := s.rebuildLoginActorGenerationAfterSceneCommit(session, "public48_direct_commit", session.selectedCharacterID)
					if !errors.Is(got, original) || !errors.Is(got, tc.leaf) {
						t.Fatal("returned join lost original send/provider error or unwrap identity")
					}
					joined, ok := got.(interface{ Unwrap() []error })
					if !ok || len(joined.Unwrap()) != 2 || joined.Unwrap()[1] != original {
						t.Fatal("return no longer joins pet then the exact original buff error")
					}
				}
				if conn.failure != original || conn.attempts != 1 || !errors.Is(conn.failure, tc.leaf) {
					t.Fatal("logging changed original send error or attempted another send")
				}
				actors := 0
				for _, frame := range loginActorGenerationSplit(t, conn.write.Bytes()) {
					if frame.msgID == 2 {
						actors++
					}
					if frame.msgID == 14 || frame.msgID == nativeBuffSwapSnapshotID {
						t.Fatal("failed pet/buff produced a successful refresh frame")
					}
				}
				if actors != 2 {
					t.Fatal("actor generation sending changed")
				}
				logText := sceneReadyReplayTestLog(t, s, path)
				formatted := fmt.Sprint(original)
				exact := fmt.Sprintf("error=redacted error_len=%d error_sha256=%x", len(formatted), sha256.Sum256([]byte(formatted)))
				matched := 0
				for _, line := range strings.Split(logText, "\n") {
					if strings.Contains(line, "kind=game-login-actor-generation-buff-refresh-failed ") && strings.Contains(line, exact) {
						matched++
					}
				}
				if matched != 1 || strings.Count(logText, "kind=game-upper-current-equipped-creature-item-rule-rejected ") != 1 || !strings.Contains(logText, "rejected invalid slot 26") {
					t.Fatal("source-specific public pet rule or exact private buff hash evidence missing/duplicated")
				}
				if strings.Contains(logText, business48PrivateMarker) || strings.Contains(logText, supplement45ErrorMarker) || strings.Contains(logText, "PANIC=") || strings.Contains(logText, ordinary.Error()) {
					t.Fatal("unknown send/provider/nil/panic text was exposed")
				}
			})
		}
	}
}
