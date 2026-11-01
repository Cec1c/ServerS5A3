//go:build windows

package dnfbridge

import (
	"context"
	"encoding/binary"
	"math/bits"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestNativePlayerTCPResolvesActualHTTPPeerProcess(t *testing.T) {
	var pid uint32
	var born time.Time
	var valid bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid, born, valid = localPlayerHTTPProcess(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !valid || pid != uint32(os.Getpid()) || born.IsZero() || born.After(time.Now()) {
		t.Fatalf("真实TCP身份不符 pid=%d born=%v valid=%v", pid, born, valid)
	}
}

func TestNativePlayerTCPRequiresFullTupleAndUniqueEstablishedOwner(t *testing.T) {
	row := make([]byte, 24)
	binary.LittleEndian.PutUint32(row, 5)
	copy(row[4:8], net.IPv4(127, 0, 0, 1).To4())
	copy(row[12:16], net.IPv4(127, 0, 0, 1).To4())
	binary.LittleEndian.PutUint32(row[8:12], uint32(bits.ReverseBytes16(45678)))
	binary.LittleEndian.PutUint32(row[16:20], uint32(bits.ReverseBytes16(18111)))
	binary.LittleEndian.PutUint32(row[20:24], 123)
	for _, name := range []string{"正常", "同端口不同IP", "非已连接", "无PID", "歧义", "截断"} {
		t.Run(name, func(t *testing.T) {
			table := make([]byte, 4)
			binary.LittleEndian.PutUint32(table, 1)
			table = append(table, row...)
			switch name {
			case "同端口不同IP":
				table[8] = 126
			case "非已连接":
				table[4] = 2
			case "无PID":
				binary.LittleEndian.PutUint32(table[24:28], 0)
			case "歧义":
				binary.LittleEndian.PutUint32(table, 2)
				table = append(table, row...)
			case "截断":
				table = table[:len(table)-1]
			}
			pid, ok := playerLoopbackOwnerFromTable(table, 45678, 18111, net.IPv4(127, 0, 0, 1), net.IPv4(127, 0, 0, 1))
			if ok != (name == "正常") || (ok && pid != 123) {
				t.Fatalf("pid=%d ok=%v", pid, ok)
			}
		})
	}
}
