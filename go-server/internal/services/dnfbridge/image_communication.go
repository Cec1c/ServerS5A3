package dnfbridge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"

	"longheng.io/server/internal/modules/dnf/dnfenum"
	"longheng.io/server/internal/modules/dnf/imagecommunication"
	dnfpvf "longheng.io/server/internal/modules/dnf/pvf"
	dnftown "longheng.io/server/internal/modules/dnf/town"
)

type currentImageCommunicationResources struct {
	source  dnfpvf.Source
	catalog *imagecommunication.Catalog
	towns   *dnftown.Table
	scenes  map[[2]int64]imagecommunication.Scene
}

// Load lazily so a missing optional communication file rejects this command
// without preventing unrelated profiles from starting. All inputs come from
// the same runtime archive; successful scene/config snapshots are immutable.
func (s *Service) currentImageCommunicationScene(ctx context.Context, townID, areaID int64) (*imagecommunication.Catalog, imagecommunication.Scene, error) {
	s.imageCommunicationMu.Lock()
	defer s.imageCommunicationMu.Unlock()
	r := s.imageCommunicationResources
	if r == nil {
		s.initialEquipmentMu.Lock()
		archive, err := s.initialEquipmentArchiveLocked()
		s.initialEquipmentMu.Unlock()
		if err != nil {
			return nil, imagecommunication.Scene{}, err
		}
		catalog, err := imagecommunication.Load(ctx, archive)
		if err != nil {
			return nil, imagecommunication.Scene{}, err
		}
		towns, err := dnftown.Load(ctx, archive, dnftown.Options{})
		if err != nil {
			return nil, imagecommunication.Scene{}, err
		}
		r = &currentImageCommunicationResources{source: archive, catalog: catalog, towns: towns, scenes: map[[2]int64]imagecommunication.Scene{}}
		s.imageCommunicationResources = r
	}
	key := [2]int64{townID, areaID}
	scene, found := r.scenes[key]
	if !found {
		var err error
		scene, err = imagecommunication.LoadScene(ctx, r.source, r.towns, townID, areaID)
		if err != nil {
			return nil, imagecommunication.Scene{}, err
		}
		if r.scenes == nil {
			r.scenes = map[[2]int64]imagecommunication.Scene{}
		}
		r.scenes[key] = scene
	}
	return r.catalog, scene, nil
}

func advanceImageCommunicationSceneLocked(session *gameSession) {
	session.imageCommunicationSceneGeneration++
	if session.imageCommunicationSceneGeneration == 0 {
		session.imageCommunicationSceneGeneration++
	}
}

func (s *Service) handleCurrentImageCommunication(session *gameSession, body []byte) error {
	if session == nil || session.conn == nil {
		return nil
	}
	const opcode = uint16(dnfenum.CmdPacketImageCommunicationEquipmentUse)
	fail := func(err error) error {
		s.logGameEvent(session, "game-image-communication-rejected", "error", err)
		code := byte(2)
		if errors.Is(err, imagecommunication.ErrGold) {
			code = 10
		}
		return s.sendGameUpperFailure(session, opcode, code)
	}
	if len(body) != 0 {
		return fail(imagecommunication.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), createWriteTimeout)
	defer cancel()
	// Match binding mutation lock order. Holding dungeon then town prevents a
	// scene transition during the debit. The reservation pins canonical session
	// ownership through the reply; no service mutex is held during repository IO.
	session.dungeon.mu.Lock()
	defer session.dungeon.mu.Unlock()
	if session.dungeon.runtime != nil {
		return fail(imagecommunication.ErrScene)
	}
	s.mu.Lock()
	identity := boundGameSessionCharacter{session: session, character: session.selectedCharacterID, generation: session.characterGeneration}
	account := strings.TrimSpace(session.accountID)
	s.mu.Unlock()
	if identity.character == 0 || account == "" || !session.loginPacketSeen {
		return fail(imagecommunication.ErrOwnership)
	}
	reservation, err := s.reserveCurrentCharacterAssets(ctx, identity, account)
	if err != nil {
		return fail(err)
	}
	defer s.releaseCurrentCharacterAssets(reservation)
	session.townMu.Lock()
	defer session.townMu.Unlock()
	position := session.townPositionSnapshot
	if session.townSceneReadyCharacterID != identity.character || position.CharacterID != identity.character || session.imageCommunicationSceneGeneration == 0 {
		return fail(imagecommunication.ErrScene)
	}
	catalog, scene, err := s.currentImageCommunicationScene(ctx, int64(position.TownID), int64(position.AreaID))
	if err != nil {
		return fail(err)
	}
	repos, ok := s.repositoryGroup()
	if !ok {
		return fail(imagecommunication.ErrUnavailable)
	}
	if session.imageCommunicationNonce == ([32]byte{}) {
		if _, err := rand.Read(session.imageCommunicationNonce[:]); err != nil {
			return fail(err)
		}
	}
	result, err := imagecommunication.Use(ctx, repos, catalog, scene, imagecommunication.Command{
		AccountID: account, CharacterID: strconv.Itoa(int(identity.character)), Now: s.gameplayNow(),
		Binding: imagecommunication.Binding{Session: session.imageCommunicationNonce, CharacterGeneration: identity.generation, SceneGeneration: session.imageCommunicationSceneGeneration},
	})
	if err != nil {
		if replyErr := fail(err); replyErr != nil {
			return replyErr
		}
		if errors.Is(err, imagecommunication.ErrGold) && result.WalletKnown {
			return s.sendCarryGoldWallet(session, result.CurrentGold)
		}
		return nil
	}
	s.logGameEvent(session, "game-image-communication-committed", "charge", result.Charge, "gold", result.Gold, "replayed", result.Replayed)
	// V124: class1/475 success consumes fee then remaining gold, not NPC IDs.
	response := make([]byte, 8)
	binary.LittleEndian.PutUint32(response, uint32(result.Charge))
	binary.LittleEndian.PutUint32(response[4:], uint32(result.Gold))
	if err := s.sendGameUpperSuccess(session, opcode, response); err != nil {
		return err
	}
	// Replaying a committed reply must not leave the client wallet stale after
	// an intervening legitimate gold mutation. The receipt itself stays frozen.
	if result.Replayed && result.CurrentGold != result.Gold {
		return s.sendCarryGoldWallet(session, result.CurrentGold)
	}
	return nil
}

func imageCommunicationGameplayModule() gameplayModuleDefinition {
	opcode := uint16(dnfenum.CmdPacketImageCommunicationEquipmentUse)
	return gameplayModuleDefinition{
		Name: "image-communication",
		LegacyHandlers: map[uint16]gameplayHandler{opcode: func(s *Service, session *gameSession, request gameplayRequest) error {
			return s.handleCurrentImageCommunication(session, request.Body)
		}},
		UpperHandlers: map[uint16]gameplayHandler{opcode: defaultClassGameplayHandler("game-image-communication-rejected", "command_class_mismatch", func(s *Service, session *gameSession, body []byte) error {
			return s.handleCurrentImageCommunication(session, body)
		})},
	}
}
