package raid

import "testing"

func TestCurrentOtherChannelJoinExactOwnedCapture(t *testing.T) {
	// Historical owned evidence422 was generated from reversed metadata.
	// Preserve its exact capture, but decode its actual native contexts; it
	// names channel0/server23 and must not authorize our channel23/server0.
	body := []byte{0, 31, 0, 0, 0, 0, 0, 10, 0, 23}
	got, err := DecodeCurrentOtherChannelJoinRequest(body)
	if err != nil || got.TargetChannel != 0 || got.TargetCharKey != 31 || got.ClientValue != 0 || got.NativeValue != 10 || got.TargetServer != 23 {
		t.Fatalf("capture=%+v err=%v", got, err)
	}
	// A distinct nonzero identity prevents the two byte fields being collapsed.
	body = []byte{7, 0x34, 0x12, 0x78, 0x56, 0x34, 0x12, 0xbc, 0x9a, 25}
	got, err = DecodeCurrentOtherChannelJoinRequest(body)
	if err != nil || got.TargetChannel != 7 || got.TargetCharKey != 0x1234 || got.ClientValue != 0x12345678 || got.NativeValue != 0x9abc || got.TargetServer != 25 {
		t.Fatalf("identity=%+v err=%v", got, err)
	}
	for n := 0; n < len(body); n++ {
		if _, err := DecodeCurrentOtherChannelJoinRequest(body[:n]); err == nil {
			t.Fatalf("accepted %d-byte truncated request", n)
		}
	}
	if _, err := DecodeCurrentOtherChannelJoinRequest(append(body, 0)); err == nil {
		t.Fatal("accepted trailing byte")
	}
	if _, err := DecodeOtherChannelRequestJoinRequest(body); err == nil {
		t.Fatal("changed historical decoder")
	}
}
