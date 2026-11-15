package dnfbridge

import (
	"errors"
)

// Experimental 101 builds may freeze requester replies under the market lock
// and write them on the same request loop after releasing it. Asset commits,
// maintenance, ownership checks and reply ordering keep their existing scope.
// No field on a shared session is used as an interception hook.
type currentAuctionMutationBatch struct {
	frames []currentAuctionReadResponse
	bytes  int
}

const auctionMutationMaxFrames = 16
const auctionMutationMaxBytes = 1 << 20

func currentAuctionMutationReplies(batches []*currentAuctionMutationBatch) *currentAuctionMutationBatch {
	if len(batches) == 1 {
		return batches[0]
	}
	return nil
}

func (b *currentAuctionMutationBatch) response(s *Service, session *gameSession, r currentAuctionReadResponse) error {
	if b == nil {
		return r.send(s, session)
	}
	if len(b.frames) >= auctionMutationMaxFrames || len(r.body) > auctionMutationMaxBytes-b.bytes {
		return errors.New("auction reply snapshot exceeds bound")
	}
	b.frames = append(b.frames, currentAuctionReadRaw(r.opcode, r.body, r.classification))
	b.bytes += len(r.body)
	return nil
}

func (b *currentAuctionMutationBatch) raw(s *Service, session *gameSession, opcode uint16, body []byte, class byte) error {
	return b.response(s, session, currentAuctionReadRaw(opcode, body, class))
}

func (b *currentAuctionMutationBatch) success(s *Service, session *gameSession, opcode uint16, body []byte) error {
	return b.response(s, session, currentAuctionReadSuccess(opcode, body))
}

func (b *currentAuctionMutationBatch) failure(s *Service, session *gameSession, opcode uint16, code byte) error {
	return b.response(s, session, currentAuctionReadFailure(opcode, code))
}

func (b *currentAuctionMutationBatch) send(s *Service, session *gameSession) error {
	for _, frame := range b.frames {
		if err := frame.send(s, session); err != nil {
			return err
		}
	}
	return nil
}
