package raid

import (
	"encoding/binary"
	"fmt"
)

// Fixed v157 023FB4B3 -> 018FBF20 sends zero for a skip input while the
// movie is active. 023FAC8C -> 018FBED0(true) sends one on close completion
// and disarms the sender. Only one confirms completion; bridge owns identity
// and the native prompt response to zero.
type MovieSkipRequest struct{ Completed bool }

func DecodeMovieSkipRequest(body []byte) (MovieSkipRequest, error) {
	if len(body) != 1 || body[0] > 1 {
		return MovieSkipRequest{}, fmt.Errorf("raid movie completion must be one boolean byte")
	}
	return MovieSkipRequest{Completed: body[0] == 1}, nil
}

// 00F19600 writes party1, gold0 and, only with its PC-room flag, PC2.
// All requests use the same card0..3. This is a request, not reward eligibility.
type RewardCardRequest struct{ Kind, Card byte }

func DecodeRewardCardRequest(body []byte) (RewardCardRequest, error) {
	if len(body) != 2 || body[0] > 2 || body[1] > 3 {
		return RewardCardRequest{}, fmt.Errorf("raid reward request kind/card outside native range")
	}
	return RewardCardRequest{Kind: body[0], Card: body[1]}, nil
}

// 0363E380 uses the actual u32 and u16 writers, yielding six bytes.
// Preserve the typed values without asserting an unverified behavior meaning.
type DoBehaviorRequest struct {
	Scene    uint32
	Behavior uint16
}

func DecodeDoBehaviorRequest(body []byte) (DoBehaviorRequest, error) {
	if len(body) != 6 {
		return DoBehaviorRequest{}, fmt.Errorf("raid behavior request must be u32/u16")
	}
	return DoBehaviorRequest{Scene: binary.LittleEndian.Uint32(body[:4]), Behavior: binary.LittleEndian.Uint16(body[4:])}, nil
}
