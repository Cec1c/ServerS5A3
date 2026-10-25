package dnfbridge

import "testing"

func TestPVPPrivateCreateShapeDoesNotCaptureOrdinaryOptions(t *testing.T) {
	for _, opts := range [][]byte{nil, make([]byte, 6), make([]byte, 8), {0, 0, 0, 0, 0, 255}, {0, 0, 0, 0, 0, 0, 255, 0}, {1, 0, 0, 0, 0, 0, 1, 0}, {0, 0, 0, 0, 0, 0, 1, 1}} {
		if _, ok := currentPrivateCreationGrow(opts); ok {
			t.Fatalf("普通参数被识别为专用创建: %v", opts)
		}
	}
	for grow := byte(1); grow <= 5; grow++ {
		v, ok := currentPrivateCreationGrow([]byte{0, 0, 0, 0, 0, 0, grow, 0})
		if !ok || v != grow {
			t.Fatal(grow, v, ok)
		}
	}
}
