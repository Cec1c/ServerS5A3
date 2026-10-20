package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf16"
)

const multi270EXESHA = "bb42898c02fb43ad30537da776f648ce26ac37df2ff47912ca7174df43c80531"
const multi270DstrSHA = "ee8904c8540ef63807dc984604a3ea0f5cc4040228e5ac2e95d490c03d21ec08"
const multi270Base uint32 = 0x400000
const multi270Gate uint32 = 0x06a898ba
const multi270MutexReturn uint32 = 0x03345515
const multi270WindowReturn uint32 = 0x033a8580
const multi270MutexSlot uint32 = 0x041882f8
const multi270WindowSlot uint32 = 0x04188540
const multi270Key = "dbefeuate_ccen_khxfor_lcar_blr"

var multi270Classes = []string{"地下城与勇士", "지하성과용사"}

// A debuggee remains stopped until install and genuine PID registration both
// succeed. The caller owns cleanup on any error, including release/detach error.
func multi270CommitGate(install, bind, release func() error) error {
	if err := install(); err != nil {
		return err
	}
	if err := bind(); err != nil {
		return err
	}
	return release()
}

func multi270InstallPair(remote uint32, write func(uint32, []byte) error) error {
	if err := write(multi270Base+multi270MutexSlot, binary.LittleEndian.AppendUint32(nil, remote)); err != nil {
		return err
	}
	return write(multi270Base+multi270WindowSlot, binary.LittleEndian.AppendUint32(nil, remote+0x800))
}

// Never resume a stopped, partially installed child if termination failed and
// exit is not independently known. Keep the debug stop/kill-on-exit protection.
func multi270MayContinueAbort(terminateOK, exited bool) bool { return terminateOK || exited }

// Only these fixed main-image bytes are accepted. This v1 rejects relocation;
// every VA encoded by the shipped executable is therefore also checked at its
// preferred image base. An unsupported asset fails before child creation.
var multi270Anchors = map[int64]string{
	0x334550b:           "506a0156ff15f88258048bf8ff15ec8158043db7000000",
	0x33a8568:           "68a2110100e8aee01f0083c41c8bf86a0057ff1540855804",
	0x35a6548:           "6a026a008d85e8fdffff50ff15d8835804",
	0x35a65d8:           "803d10467306005775046a66eb026a6a56e832a05100",
	int64(multi270Gate): "e9fa74fffc",
}

func multi270CheckAsset(r io.ReaderAt, size int64, exe bool) error {
	expected, n := multi270DstrSHA, int64(1719808)
	if exe {
		expected, n = multi270EXESHA, 111763456
	}
	if size != n {
		return errors.New("multi270 unsupported asset size")
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(r, 0, size)); err != nil {
		return errors.New("multi270 asset read failed")
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("multi270 unsupported asset hash")
	}
	if exe {
		for off, encoded := range multi270Anchors {
			want, _ := hex.DecodeString(encoded)
			got := make([]byte, len(want))
			if _, err := r.ReadAt(got, off); err != nil || !bytes.Equal(got, want) {
				return errors.New("multi270 unsupported instruction bytes")
			}
		}
	}
	return nil
}

// Tiny x86 emitter. All conditional branches use checked rel32 fixups; the
// payload contains no loader, network, environment, credential or provider data.
type multi270Asm struct {
	b      []byte
	labels map[string]int
	fix    map[int]string
}

func multi270NewAsm() *multi270Asm {
	return &multi270Asm{labels: map[string]int{}, fix: map[int]string{}}
}
func (a *multi270Asm) emit(b ...byte)             { a.b = append(a.b, b...) }
func (a *multi270Asm) u32(v uint32)               { a.b = binary.LittleEndian.AppendUint32(a.b, v) }
func (a *multi270Asm) label(s string)             { a.labels[s] = len(a.b) }
func (a *multi270Asm) branch(cond byte, s string) { a.emit(0x0f, cond); a.fix[len(a.b)] = s; a.u32(0) }
func (a *multi270Asm) jump(s string)              { a.emit(0xe9); a.fix[len(a.b)] = s; a.u32(0) }
func (a *multi270Asm) call(addr uint32)           { a.emit(0xb8); a.u32(addr); a.emit(0xff, 0xd0) }
func (a *multi270Asm) push(v uint32)              { a.emit(0x68); a.u32(v) }
func (a *multi270Asm) finish() ([]byte, error) {
	for p, s := range a.fix {
		v, ok := a.labels[s]
		if !ok {
			return nil, errors.New("multi270 unbound branch")
		}
		binary.LittleEndian.PutUint32(a.b[p:], uint32(int32(v-p-4)))
	}
	return a.b, nil
}
func multi270Wide(s string) []byte {
	var b []byte
	for _, c := range append(utf16.Encode([]rune(s)), 0) {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return b
}

// EDX points to the original UTF16 argument. Comparisons include the terminator.
func (a *multi270Asm) wideCompare(s, fail string) {
	for i, c := range append(utf16.Encode([]rune(s)), 0) {
		a.emit(0x66, 0x81, 0xba)
		a.u32(uint32(i * 2))
		a.emit(byte(c), byte(c>>8))
		a.branch(0x85, fail)
	}
}

func multi270MutexThunk(original, newName uint32) ([]byte, error) {
	a := multi270NewAsm()
	a.emit(0x81, 0x3c, 0x24)
	a.u32(multi270Base + multi270MutexReturn)
	a.branch(0x85, "pass")
	a.emit(0x8b, 0x54, 0x24, 0x0c, 0x85, 0xd2)
	a.branch(0x84, "pass")
	a.wideCompare(multi270Key, "pass")
	a.emit(0xc7, 0x44, 0x24, 0x0c)
	a.u32(newName)
	a.label("pass")
	a.emit(0xb8)
	a.u32(original)
	a.emit(0xff, 0xe0)
	return a.finish()
}

type multi270APIs struct{ FindWindow, GetWindowPID, OpenProcess, GetTimes, CloseHandle, GetLastError, SetLastError uint32 }

func multi270WindowThunk(api multi270APIs, firstPID uint32, firstBirth uint64) ([]byte, error) {
	a := multi270NewAsm()
	// stdcall frame: locals(-4 HWND,-8 saved GLE,-12 PID,-20 creation,
	// -28 exit,-36 kernel,-44 user). EBX/ESI/EDI/EBP preserved.
	a.emit(0x55, 0x8b, 0xec, 0x83, 0xec, 0x30, 0x53, 0x56, 0x57)
	a.emit(0x81, 0x7d, 0x04)
	a.u32(multi270Base + multi270WindowReturn)
	a.branch(0x85, "passthrough")
	a.emit(0x83, 0x7d, 0x0c, 0)
	a.branch(0x85, "passthrough")
	a.emit(0x8b, 0x55, 0x08, 0x85, 0xd2)
	a.branch(0x84, "passthrough")
	a.wideCompare(multi270Classes[0], "class2")
	a.jump("target")
	a.label("class2")
	a.wideCompare(multi270Classes[1], "passthrough")
	a.label("target")
	a.emit(0xff, 0x75, 0x0c, 0xff, 0x75, 0x08)
	a.call(api.FindWindow)
	a.emit(0x89, 0x45, 0xfc)
	a.call(api.GetLastError)
	a.emit(0x89, 0x45, 0xf8)
	a.emit(0x83, 0x7d, 0xfc, 0)
	a.branch(0x84, "restore")
	a.emit(0xc7, 0x45, 0xf4, 0, 0, 0, 0, 0x8d, 0x45, 0xf4, 0x50, 0xff, 0x75, 0xfc)
	a.call(api.GetWindowPID)
	a.emit(0x85, 0xc0)
	a.branch(0x84, "restore")
	a.emit(0x81, 0x7d, 0xf4)
	a.u32(firstPID)
	a.branch(0x85, "restore")
	a.push(firstPID)
	a.push(0)
	a.push(0x1000)
	a.call(api.OpenProcess)
	a.emit(0x85, 0xc0)
	a.branch(0x84, "restore")
	a.emit(0x8b, 0xd8)
	// GetProcessTimes(handle, creation, exit, kernel, user).
	for _, off := range []byte{0xd4, 0xdc, 0xe4, 0xec} {
		a.emit(0x8d, 0x45, off, 0x50)
	}
	a.emit(0x53)
	a.call(api.GetTimes)
	a.emit(0x8b, 0xf0, 0x53)
	a.call(api.CloseHandle)
	a.emit(0x85, 0xc0)
	a.branch(0x84, "restore")
	a.emit(0x85, 0xf6)
	a.branch(0x84, "restore")
	a.emit(0x81, 0x7d, 0xec)
	a.u32(uint32(firstBirth))
	a.branch(0x85, "restore")
	a.emit(0x81, 0x7d, 0xf0)
	a.u32(uint32(firstBirth >> 32))
	a.branch(0x85, "restore")
	a.emit(0xc7, 0x45, 0xfc, 0, 0, 0, 0)
	a.label("restore")
	a.emit(0xff, 0x75, 0xf8)
	a.call(api.SetLastError)
	a.emit(0x8b, 0x45, 0xfc)
	a.jump("return")
	a.label("passthrough")
	a.emit(0xff, 0x75, 0x0c, 0xff, 0x75, 0x08)
	a.call(api.FindWindow)
	a.label("return")
	a.emit(0x5f, 0x5e, 0x5b, 0x8b, 0xe5, 0x5d, 0xc2, 0x08, 0x00)
	return a.finish()
}

func multi270Payload(remote uint32, api multi270APIs, mutex, firstPID uint32, firstBirth, childBirth uint64, childPID uint32) ([]byte, error) {
	if remote == 0 || remote > 0xffffcfff || firstPID == 0 || firstPID == childPID || firstBirth == 0 || childBirth == 0 {
		return nil, errors.New("multi270 invalid owned identity")
	}
	for _, v := range []uint32{api.FindWindow, api.GetWindowPID, api.OpenProcess, api.GetTimes, api.CloseHandle, api.GetLastError, api.SetLastError, mutex} {
		if v == 0 {
			return nil, errors.New("multi270 missing original API")
		}
	}
	b := make([]byte, 0x3000)
	name := multi270Wide(fmt.Sprintf("%s_270_%d_%016x", multi270Key, childPID, childBirth))
	copy(b[0x1800:], name)
	m, err := multi270MutexThunk(mutex, remote+0x1800)
	if err != nil {
		return nil, err
	}
	w, err := multi270WindowThunk(api, firstPID, firstBirth)
	if err != nil {
		return nil, err
	}
	if len(m) > 0x800 || len(w) > 0x1000 {
		return nil, errors.New("multi270 payload overflow")
	}
	copy(b, m)
	copy(b[0x800:], w)
	return b, nil
}

func multi270LoaderBreakpoint(code, address, ntdll, size, tid, primary uint32, seen bool) bool {
	return !seen && code == 0x80000003 && tid == primary && ntdll != 0 && size != 0 && address >= ntdll && uint64(address) < uint64(ntdll)+uint64(size)
}

func multi270CleanupError(cause error, proc *os.Process) error {
	pid, birth := "unknown", "unknown"
	if proc != nil {
		pid = strconv.Itoa(proc.Pid)
	}
	facts := captureClientStartFacts(proc)
	if facts.HasCreated {
		birth = strconv.FormatUint(facts.CreatedFiletime, 10)
	}
	cleanup := cleanupOwnedClient270(proc)
	return fmt.Errorf("%w; owned_child_pid=%s owned_child_birth_filetime=%s owned_child_cleanup=%s", cause, pid, birth, cleanup)
}
