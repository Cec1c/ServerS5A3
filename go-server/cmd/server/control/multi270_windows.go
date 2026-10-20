//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var multi270Kernel = windows.NewLazySystemDLL("kernel32.dll")
var multi270WaitDebug = multi270Kernel.NewProc("WaitForDebugEvent")
var multi270Continue = multi270Kernel.NewProc("ContinueDebugEvent")
var multi270Detach = multi270Kernel.NewProc("DebugActiveProcessStop")
var multi270KillOnExit = multi270Kernel.NewProc("DebugSetProcessKillOnExit")
var multi270Read = multi270Kernel.NewProc("ReadProcessMemory")
var multi270Write = multi270Kernel.NewProc("WriteProcessMemory")
var multi270Protect = multi270Kernel.NewProc("VirtualProtectEx")
var multi270Alloc = multi270Kernel.NewProc("VirtualAllocEx")
var multi270Flush = multi270Kernel.NewProc("FlushInstructionCache")
var multi270GetContext = multi270Kernel.NewProc("GetThreadContext")
var multi270SetContext = multi270Kernel.NewProc("SetThreadContext")

// Fixed errors only: never include CreateProcess command line, account argument,
// provider error text, API buffer or an untrusted path in a diagnostic.
func multi270Fault(s string) error { return errors.New("start client: multi270 " + s) }

func cleanupOwnedClient270(proc *os.Process) string {
	if proc == nil {
		return "unconfirmed"
	}
	confirmed := false
	_ = proc.WithHandle(func(handle uintptr) {
		h := windows.Handle(handle)
		wait, err := windows.WaitForSingleObject(h, 0)
		if err != nil {
			return
		}
		if wait == windows.WAIT_OBJECT_0 {
			confirmed = true
			return
		}
		if wait != uint32(windows.WAIT_TIMEOUT) {
			return
		}
		if windows.TerminateProcess(h, 1) != nil {
			return
		}
		wait, err = windows.WaitForSingleObject(h, 5000)
		confirmed = err == nil && wait == windows.WAIT_OBJECT_0
	})
	_ = proc.Release()
	if confirmed {
		return "confirmed"
	}
	return "unconfirmed"
}

func startClient270(ctx context.Context, cmd *exec.Cmd, multi bool, bind func(int) error) (*os.Process, error) {
	if !multi {
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start client: %w", err)
		}
		if err := bind(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			return nil, err
		}
		return cmd.Process, nil
	}
	return multi270OwnedStart(ctx, cmd, bind)
}

type multi270First struct {
	handle windows.Handle
	pid    uint32
	birth  uint64
}

func multi270Birth(h windows.Handle) (uint64, error) {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return 0, multi270Fault("BIRTH_UNAVAILABLE")
	}
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime), nil
}
func multi270FirstOwner(executable string) (multi270First, error) {
	var found multi270First
	s, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return found, multi270Fault("FIRST_INVENTORY_UNAVAILABLE")
	}
	defer windows.CloseHandle(s)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	err = windows.Process32First(s, &e)
	for err == nil {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), filepath.Base(executable)) {
			h, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, e.ProcessID)
			if openErr != nil {
				if found.handle != 0 {
					windows.CloseHandle(found.handle)
				}
				return multi270First{}, multi270Fault("FIRST_IDENTITY_UNAVAILABLE")
			}
			b := make([]uint16, 32768)
			n := uint32(len(b))
			pathErr := windows.QueryFullProcessImageName(h, 0, &b[0], &n)
			if pathErr != nil {
				windows.CloseHandle(h)
				if found.handle != 0 {
					windows.CloseHandle(found.handle)
				}
				return multi270First{}, multi270Fault("FIRST_IDENTITY_UNAVAILABLE")
			}
			wait, waitErr := windows.WaitForSingleObject(h, 0)
			if sameExecutable(windows.UTF16ToString(b[:n]), executable) && waitErr == nil && wait == uint32(windows.WAIT_TIMEOUT) {
				birth, birthErr := multi270Birth(h)
				if birthErr != nil || birth == 0 {
					windows.CloseHandle(h)
					if found.handle != 0 {
						windows.CloseHandle(found.handle)
					}
					return multi270First{}, multi270Fault("FIRST_IDENTITY_UNAVAILABLE")
				}
				if found.handle != 0 {
					windows.CloseHandle(h)
					windows.CloseHandle(found.handle)
					return multi270First{}, multi270Fault("FIRST_AMBIGUOUS")
				}
				found = multi270First{h, e.ProcessID, birth}
			} else {
				windows.CloseHandle(h)
			}
		}
		err = windows.Process32Next(s, &e)
	}
	if err != windows.ERROR_NO_MORE_FILES {
		if found.handle != 0 {
			windows.CloseHandle(found.handle)
		}
		return multi270First{}, multi270Fault("FIRST_INVENTORY_UNAVAILABLE")
	}
	if found.handle == 0 {
		return found, multi270Fault("FIRST_NOT_FOUND")
	}
	return found, nil
}

// Keep assets open with only SHARE_READ through launch: no write/replace window
// between hash validation and CreateProcess. No provider data leaves this helper.
func multi270Asset(path string, exe bool) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, multi270Fault("ASSET_PATH_INVALID")
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, multi270Fault("ASSET_OPEN_FAILED")
	}
	f := os.NewFile(uintptr(h), path)
	info, err := f.Stat()
	if err == nil {
		err = multi270CheckAsset(f, info.Size(), exe)
	}
	if err != nil {
		f.Close()
		return nil, multi270Fault("ASSET_GATE_REJECTED")
	}
	return f, nil
}

// Every remote API in this type is reachable only after successful CreateProcess
// on this thread. There is no arbitrary-PID attach/open-for-write entry point.
type multi270Child struct {
	ntdllBase, ntdllSize uint32
	pi                   windows.ProcessInformation
	modules              map[string]uint32
	pending              bool
	eventPID, eventTID   uint32
	detached             bool
}

func (c *multi270Child) read(address uint32, n int) ([]byte, error) {
	if n < 1 || n > 1<<20 || address == 0 || uint64(address)+uint64(n) > 1<<32 {
		return nil, multi270Fault("READ_BOUNDS")
	}
	b := make([]byte, n)
	var count uintptr
	r, _, _ := multi270Read.Call(uintptr(c.pi.Process), uintptr(address), uintptr(unsafe.Pointer(&b[0])), uintptr(n), uintptr(unsafe.Pointer(&count)))
	if r == 0 || count != uintptr(n) {
		return nil, multi270Fault("CHILD_READ_FAILED")
	}
	return b, nil
}
func (c *multi270Child) write(address uint32, b []byte) error {
	if len(b) == 0 {
		return multi270Fault("WRITE_BOUNDS")
	}
	var count uintptr
	r, _, _ := multi270Write.Call(uintptr(c.pi.Process), uintptr(address), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&count)))
	if r == 0 || count != uintptr(len(b)) {
		return multi270Fault("CHILD_WRITE_FAILED")
	}
	return nil
}
func (c *multi270Child) protect(address, size uint32, to uint32) (uint32, error) {
	var old uint32
	r, _, _ := multi270Protect.Call(uintptr(c.pi.Process), uintptr(address), uintptr(size), uintptr(to), uintptr(unsafe.Pointer(&old)))
	if r == 0 {
		return 0, multi270Fault("CHILD_PROTECT_FAILED")
	}
	return old, nil
}
func (c *multi270Child) patch(address uint32, b []byte) error {
	old, err := c.protect(address, uint32(len(b)), windows.PAGE_READWRITE)
	if err != nil {
		return err
	}
	writeErr := c.write(address, b)
	_, restoreErr := c.protect(address, uint32(len(b)), old)
	if writeErr != nil {
		return writeErr
	}
	if restoreErr != nil {
		return restoreErr
	}
	r, _, _ := multi270Flush.Call(uintptr(c.pi.Process), uintptr(address), uintptr(len(b)))
	if r == 0 {
		return multi270Fault("CHILD_FLUSH_FAILED")
	}
	got, err := c.read(address, len(b))
	if err != nil || !bytes.Equal(got, b) {
		return multi270Fault("CHILD_WRITE_VERIFY_FAILED")
	}
	return nil
}
func (c *multi270Child) continuation(status uint32) error {
	r, _, _ := multi270Continue.Call(uintptr(c.eventPID), uintptr(c.eventTID), uintptr(status))
	if r == 0 {
		return multi270Fault("DEBUG_CONTINUE_FAILED")
	}
	c.pending = false
	return nil
}

func multi270Ptr(b []byte, off int) uintptr {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return uintptr(binary.LittleEndian.Uint64(b[off:]))
	}
	return uintptr(binary.LittleEndian.Uint32(b[off:]))
}
func multi270EventUnion() int {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return 16
	}
	return 12
}

func (c *multi270Child) abort() bool {
	wait, waitErr := windows.WaitForSingleObject(c.pi.Process, 0)
	exited := waitErr == nil && wait == windows.WAIT_OBJECT_0
	terminateErr := windows.TerminateProcess(c.pi.Process, 1)
	if !multi270MayContinueAbort(terminateErr == nil, exited) {
		// No Continue/Detach here: an alive partial child must remain stopped. Restore
		// kill-on-debugger-exit if release reached that policy change before failing.
		multi270KillOnExit.Call(1)
		return false
	}
	if c.pending {
		if c.continuation(0x10002) != nil {
			multi270KillOnExit.Call(1)
			return false
		}
	}
	// Termination can itself queue EXIT_PROCESS_DEBUG_EVENT. Drain it rather than
	// waiting for a signaled process while withholding the required continuation.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		wait, waitErr = windows.WaitForSingleObject(c.pi.Process, 0)
		if waitErr == nil && wait == windows.WAIT_OBJECT_0 {
			multi270Detach.Call(uintptr(c.pi.ProcessId))
			return true
		}
		var event [176]byte
		r, _, _ := multi270WaitDebug.Call(uintptr(unsafe.Pointer(&event[0])), 100)
		if r == 0 {
			continue
		}
		pid, tid := binary.LittleEndian.Uint32(event[4:]), binary.LittleEndian.Uint32(event[8:])
		if pid != c.pi.ProcessId {
			multi270KillOnExit.Call(1)
			return false
		}
		u := event[multi270EventUnion():]
		psize := int(unsafe.Sizeof(uintptr(0)))
		code := binary.LittleEndian.Uint32(event[:])
		switch code {
		case 3:
			for i := 0; i < 3; i++ {
				h := windows.Handle(multi270Ptr(u, i*psize))
				if h != 0 && h != c.pi.Process && h != c.pi.Thread {
					windows.CloseHandle(h)
				}
			}
		case 6:
			if h := windows.Handle(multi270Ptr(u, 0)); h != 0 {
				windows.CloseHandle(h)
			}
		case 2:
			if h := windows.Handle(multi270Ptr(u, 0)); h != 0 && h != c.pi.Thread {
				windows.CloseHandle(h)
			}
		}
		c.eventPID, c.eventTID, c.pending = pid, tid, true
		if c.continuation(0x10002) != nil {
			multi270KillOnExit.Call(1)
			return false
		}
	}
	// Only the owned PID may be detached after confirmed TerminateProcess success.
	// Never announce cleanup complete without a signaled held process handle.
	multi270Detach.Call(uintptr(c.pi.ProcessId))
	wait, waitErr = windows.WaitForSingleObject(c.pi.Process, 1000)
	return waitErr == nil && wait == windows.WAIT_OBJECT_0
}
func (c *multi270Child) threadIP(set bool) error {
	// Native x86 CONTEXT_CONTROL, EIP offset184. This initial candidate is
	// Windows386 only (also on WOW64). Use the separately built 386 normal CLI;
	// reject amd64 multi before creation rather than guess WOW64 debug events.
	var raw [716 + 16]byte
	base := unsafe.Pointer(&raw[0])
	contextPointer := unsafe.Add(base, (-uintptr(base))&15)
	p := uintptr(contextPointer)
	b := unsafe.Slice((*byte)(contextPointer), 716)
	binary.LittleEndian.PutUint32(b, 0x10001)
	get, setProc := multi270GetContext, multi270SetContext
	r, _, _ := get.Call(uintptr(c.pi.Thread), p)
	if r == 0 {
		return multi270Fault("X86_CONTEXT_UNAVAILABLE")
	}
	if binary.LittleEndian.Uint32(b[184:]) != multi270Base+multi270Gate+1 {
		return multi270Fault("GATE_EIP_MISMATCH")
	}
	if set {
		binary.LittleEndian.PutUint32(b[184:], multi270Base+multi270Gate)
		r, _, _ = setProc.Call(uintptr(c.pi.Thread), p)
		if r == 0 {
			return multi270Fault("X86_CONTEXT_RESTORE_FAILED")
		}
	}
	runtime.KeepAlive(raw)
	return nil
}

func (c *multi270Child) cstring(addr uint32) (string, error) {
	var out []byte
	for i := uint32(0); i < 256; i++ {
		b, err := c.read(addr+i, 1)
		if err != nil {
			return "", err
		}
		if b[0] == 0 {
			return string(out), nil
		}
		out = append(out, b[0])
	}
	return "", multi270Fault("EXPORT_STRING_BOUNDS")
}
func (c *multi270Child) exportDirectory(base uint32) (uint32, uint32, error) {
	h, err := c.read(base, 0x40)
	if err != nil || string(h[:min(len(h), 2)]) != "MZ" {
		return 0, 0, multi270Fault("MODULE_HEADER_INVALID")
	}
	off := binary.LittleEndian.Uint32(h[0x3c:])
	if off > 0x100000 {
		return 0, 0, multi270Fault("MODULE_HEADER_BOUNDS")
	}
	h, err = c.read(base+off, 0x108)
	if err != nil || string(h[:4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(h[24:]) != 0x10b {
		return 0, 0, multi270Fault("MODULE_NOT_PE32")
	}
	rva, size := binary.LittleEndian.Uint32(h[120:]), binary.LittleEndian.Uint32(h[124:])
	if rva == 0 || size < 40 || size > 1<<20 {
		return 0, 0, multi270Fault("EXPORT_DIRECTORY_INVALID")
	}
	return rva, size, nil
}
func (c *multi270Child) module(base uint32) error {
	rva, _, err := c.exportDirectory(base)
	if err != nil {
		return err
	}
	h, err := c.read(base+rva, 40)
	if err != nil {
		return err
	}
	name, err := c.cstring(base + binary.LittleEndian.Uint32(h[12:]))
	if err != nil {
		return err
	}
	c.modules[strings.ToLower(name)] = base
	if strings.EqualFold(name, "ntdll.dll") {
		dos, readErr := c.read(base, 0x40)
		if readErr != nil {
			return readErr
		}
		nt := binary.LittleEndian.Uint32(dos[0x3c:])
		imageSize, readErr := c.read(base+nt+80, 4)
		if readErr != nil {
			return readErr
		}
		n := binary.LittleEndian.Uint32(imageSize)
		if n == 0 || uint64(base)+uint64(n) > 1<<32 {
			return multi270Fault("NTDLL_IMAGE_BOUNDS")
		}
		c.ntdllBase, c.ntdllSize = base, n
	}
	return nil
}
func (c *multi270Child) resolve(module, name string, depth int) (uint32, error) {
	if depth > 4 {
		return 0, multi270Fault("EXPORT_FORWARD_LOOP")
	}
	module = strings.ToLower(module)
	if !strings.HasSuffix(module, ".dll") {
		module += ".dll"
	}
	base := c.modules[module]
	if base == 0 {
		return 0, multi270Fault("REQUIRED_MODULE_MISSING")
	}
	rva, size, err := c.exportDirectory(base)
	if err != nil {
		return 0, err
	}
	h, err := c.read(base+rva, 40)
	if err != nil {
		return 0, err
	}
	nf, nn := binary.LittleEndian.Uint32(h[20:]), binary.LittleEndian.Uint32(h[24:])
	if nf == 0 || nf > 32768 || nn > 32768 {
		return 0, multi270Fault("EXPORT_TABLE_BOUNDS")
	}
	fs, err := c.read(base+binary.LittleEndian.Uint32(h[28:]), int(nf)*4)
	if err != nil {
		return 0, err
	}
	ns, err := c.read(base+binary.LittleEndian.Uint32(h[32:]), int(nn)*4)
	if err != nil {
		return 0, err
	}
	ord, err := c.read(base+binary.LittleEndian.Uint32(h[36:]), int(nn)*2)
	if err != nil {
		return 0, err
	}
	for i := uint32(0); i < nn; i++ {
		s, err := c.cstring(base + binary.LittleEndian.Uint32(ns[i*4:]))
		if err != nil {
			return 0, err
		}
		if s != name {
			continue
		}
		idx := uint32(binary.LittleEndian.Uint16(ord[i*2:]))
		if idx >= nf {
			return 0, multi270Fault("EXPORT_ORDINAL_BOUNDS")
		}
		v := binary.LittleEndian.Uint32(fs[idx*4:])
		if v >= rva && v < rva+size {
			forward, err := c.cstring(base + v)
			if err != nil {
				return 0, err
			}
			j := strings.LastIndex(forward, ".")
			if j < 1 || strings.Contains(forward[j+1:], "#") {
				return 0, multi270Fault("UNSUPPORTED_EXPORT_FORWARD")
			}
			return c.resolve(forward[:j], forward[j+1:], depth+1)
		}
		if v == 0 {
			return 0, multi270Fault("NULL_EXPORT")
		}
		return base + v, nil
	}
	return 0, multi270Fault("API_EXPORT_NOT_FOUND")
}

func (c *multi270Child) install(first multi270First, birth uint64) error {
	if err := c.threadIP(false); err != nil {
		return err
	}
	// Recheck unchanged code while all debuggee threads are stopped. The gate's
	// first byte alone is our INT3; no main image relocation is supported in v1.
	for rva, s := range multi270Anchors {
		want, decodeErr := hex.DecodeString(s)
		if decodeErr != nil {
			return multi270Fault("ANCHOR_CONSTANT_INVALID")
		}
		if uint32(rva) == multi270Gate {
			want[0] = 0xcc
		}
		got, err := c.read(multi270Base+uint32(rva), len(want))
		if err != nil || !bytes.Equal(got, want) {
			return multi270Fault("RUNTIME_BYTES_MISMATCH")
		}
	}
	var api multi270APIs
	specs := []struct {
		module, name string
		out          *uint32
	}{{"user32", "FindWindowW", &api.FindWindow}, {"user32", "GetWindowThreadProcessId", &api.GetWindowPID}, {"kernel32", "OpenProcess", &api.OpenProcess}, {"kernel32", "GetProcessTimes", &api.GetTimes}, {"kernel32", "CloseHandle", &api.CloseHandle}, {"kernel32", "GetLastError", &api.GetLastError}, {"kernel32", "SetLastError", &api.SetLastError}}
	for _, s := range specs {
		v, err := c.resolve(s.module, s.name, 0)
		if err != nil {
			return err
		}
		*s.out = v
	}
	mutex, err := c.resolve("kernel32", "CreateMutexW", 0)
	if err != nil {
		return err
	}
	for _, s := range []struct{ rva, api uint32 }{{multi270MutexSlot, mutex}, {multi270WindowSlot, api.FindWindow}} {
		b, err := c.read(multi270Base+s.rva, 4)
		if err != nil || binary.LittleEndian.Uint32(b) != s.api {
			return multi270Fault("ORIGINAL_IAT_MISMATCH")
		}
	}
	wait, err := windows.WaitForSingleObject(first.handle, 0)
	if err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		return multi270Fault("FIRST_NO_LONGER_RUNNING")
	}
	addr, _, _ := multi270Alloc.Call(uintptr(c.pi.Process), 0, 0x3000, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if addr == 0 || uint64(addr) > 0xffffcfff {
		return multi270Fault("PAYLOAD_ALLOCATION_FAILED")
	}
	payload, err := multi270Payload(uint32(addr), api, mutex, first.pid, first.birth, birth, c.pi.ProcessId)
	if err != nil {
		return err
	}
	if err = c.write(uint32(addr), payload); err != nil {
		return err
	}
	if _, err = c.protect(uint32(addr), 0x3000, windows.PAGE_EXECUTE_READ); err != nil {
		return err
	}
	flush, _, _ := multi270Flush.Call(uintptr(c.pi.Process), addr, 0x3000)
	if flush == 0 {
		return multi270Fault("PAYLOAD_FLUSH_FAILED")
	}
	// Any failure below leaves the gate stopped and the owned child is terminated.
	// No partially installed child is ever released; FIRST/server/DB are untouched.
	if err = multi270InstallPair(uint32(addr), c.patch); err != nil {
		return err
	}
	if err = c.patch(multi270Base+multi270Gate, []byte{0xe9}); err != nil {
		return err
	}
	return c.threadIP(true)
}

func multi270OwnedStart(ctx context.Context, cmd *exec.Cmd, bind func(int) error) (proc *os.Process, err error) {
	if runtime.GOARCH != "386" {
		return nil, multi270Fault("UNSUPPORTED_CONTROLLER_ARCH")
	}
	exe, err := multi270Asset(cmd.Path, true)
	if err != nil {
		return nil, err
	}
	defer exe.Close()
	dstr, err := multi270Asset(filepath.Join(cmd.Dir, "dstr.dat"), false)
	if err != nil {
		return nil, err
	}
	defer dstr.Close()
	first, err := multi270FirstOwner(cmd.Path)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(first.handle)
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, multi270Fault("COMMAND_ENCODING_FAILED")
	}
	if len(cmd.Args) != 2 {
		return nil, multi270Fault("COMMAND_SHAPE_INVALID")
	}
	line, err := windows.UTF16PtrFromString(syscall.EscapeArg(cmd.Path) + " " + syscall.EscapeArg(cmd.Args[1]))
	if err != nil {
		return nil, multi270Fault("COMMAND_ENCODING_FAILED")
	}
	dir, err := windows.UTF16PtrFromString(cmd.Dir)
	if err != nil {
		return nil, multi270Fault("DIRECTORY_ENCODING_FAILED")
	}
	var env []uint16
	for _, s := range cmd.Env {
		v, e := windows.UTF16FromString(s)
		if e != nil {
			return nil, multi270Fault("ENVIRONMENT_ENCODING_FAILED")
		}
		env = append(env, v...)
	}
	env = append(env, 0)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	child := &multi270Child{modules: map[string]uint32{}}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	// DEBUG_ONLY_THIS_PROCESS, Unicode environment, and unchanged NEW_PROCESS_GROUP.
	// Do not use exec.Cmd with CREATE_SUSPENDED: Go closes the primary-thread handle.
	if e := windows.CreateProcess(app, line, nil, nil, false, 0x2|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NEW_PROCESS_GROUP, &env[0], dir, &si, &child.pi); e != nil {
		return nil, multi270Fault("CREATE_FAILED")
	}
	var ownedBirth uint64
	defer func() {
		if err != nil {
			cleanup := "confirmed"
			if !child.abort() {
				cleanup = "unconfirmed"
			}
			birthText := "unknown"
			if ownedBirth != 0 {
				birthText = fmt.Sprintf("%d", ownedBirth)
			}
			err = fmt.Errorf("%w; owned_child_pid=%d owned_child_birth_filetime=%s owned_child_cleanup=%s", err, child.pi.ProcessId, birthText, cleanup)
		}
		windows.CloseHandle(child.pi.Thread)
		windows.CloseHandle(child.pi.Process)
	}()
	birth, e := multi270Birth(child.pi.Process)
	ownedBirth = birth
	if e != nil {
		return nil, e
	}
	deadline := time.Now().Add(30 * time.Second)
	loaderBreakSeen := false
	gateArmed := false
	for {
		if ctx.Err() != nil {
			return nil, multi270Fault("START_CANCELLED")
		}
		if time.Now().After(deadline) {
			return nil, multi270Fault("GATE_TIMEOUT")
		}
		var event [176]byte
		r, _, callErr := multi270WaitDebug.Call(uintptr(unsafe.Pointer(&event[0])), 100)
		if r == 0 {
			if callErr == windows.ERROR_SEM_TIMEOUT {
				continue
			}
			return nil, multi270Fault("DEBUG_WAIT_FAILED")
		}
		code := binary.LittleEndian.Uint32(event[:])
		child.eventPID = binary.LittleEndian.Uint32(event[4:])
		child.eventTID = binary.LittleEndian.Uint32(event[8:])
		child.pending = true
		if child.eventPID != child.pi.ProcessId {
			return nil, multi270Fault("FOREIGN_DEBUG_EVENT")
		}
		u := event[multi270EventUnion():]
		status := uint32(0x10002)
		switch code {
		case 3: // CREATE_PROCESS_DEBUG_EVENT
			psize := int(unsafe.Sizeof(uintptr(0)))
			base := multi270Ptr(u, 3*psize)
			if h := windows.Handle(multi270Ptr(u, 0)); h != 0 {
				windows.CloseHandle(h)
			}
			// Debug-event handles may alias the CreateProcess handles; never close twice.
			if h := windows.Handle(multi270Ptr(u, psize)); h != 0 && h != child.pi.Process {
				windows.CloseHandle(h)
			}
			if h := windows.Handle(multi270Ptr(u, 2*psize)); h != 0 && h != child.pi.Thread {
				windows.CloseHandle(h)
			}
			if base != uintptr(multi270Base) || child.eventTID != child.pi.ThreadId || gateArmed {
				return nil, multi270Fault("MAIN_IMAGE_BASE_OR_THREAD_MISMATCH")
			}
			b, e := child.read(multi270Base+multi270Gate, 5)
			if e != nil || !bytes.Equal(b, []byte{0xe9, 0xfa, 0x74, 0xff, 0xfc}) {
				return nil, multi270Fault("ENTRY_BYTES_MISMATCH")
			}
			if e = child.patch(multi270Base+multi270Gate, []byte{0xcc}); e != nil {
				return nil, e
			}
			gateArmed = true
		case 6: // LOAD_DLL_DEBUG_EVENT; disk resources remain DATAFILE, not loaders.
			psize := int(unsafe.Sizeof(uintptr(0)))
			base := multi270Ptr(u, psize)
			if h := windows.Handle(multi270Ptr(u, 0)); h != 0 {
				windows.CloseHandle(h)
			}
			if uint64(base) > 0xffffffff {
				return nil, multi270Fault("MODULE_ADDRESS_UNSUPPORTED")
			}
			// Some data-only/managed modules have no exports. Required APIs are checked
			// strictly at the gate; ignoring an unrelated exportless DLL grants nothing.
			_ = child.module(uint32(base))
		case 2:
			if h := windows.Handle(multi270Ptr(u, 0)); h != 0 && h != child.pi.Thread {
				windows.CloseHandle(h)
			}
		case 5:
			return nil, multi270Fault("CHILD_EXITED_BEFORE_GATE")
		case 1:
			exception := binary.LittleEndian.Uint32(u)
			addrOff := 12
			if unsafe.Sizeof(uintptr(0)) == 8 {
				addrOff = 16
			}
			address := multi270Ptr(u, addrOff)
			if exception == 0x80000003 && address == uintptr(multi270Base+multi270Gate) {
				if !gateArmed || child.eventTID != child.pi.ThreadId {
					return nil, multi270Fault("GATE_THREAD_MISMATCH")
				}
				e = multi270CommitGate(
					func() error { return child.install(first, birth) },
					func() error { return bind(int(child.pi.ProcessId)) },
					func() error {
						firstWait, firstErr := windows.WaitForSingleObject(first.handle, 0)
						if firstErr != nil || firstWait != uint32(windows.WAIT_TIMEOUT) {
							return multi270Fault("FIRST_NO_LONGER_RUNNING")
						}
						var openErr error
						proc, openErr = os.FindProcess(int(child.pi.ProcessId))
						if openErr != nil {
							return multi270Fault("PROCESS_HANDLE_UNAVAILABLE")
						}
						if releaseErr := child.continuation(status); releaseErr != nil {
							proc.Release()
							return releaseErr
						}
						ok, _, _ := multi270KillOnExit.Call(0)
						if ok == 0 {
							proc.Release()
							return multi270Fault("DEBUG_RELEASE_POLICY_FAILED")
						}
						ok, _, _ = multi270Detach.Call(uintptr(child.pi.ProcessId))
						if ok == 0 {
							proc.Release()
							return multi270Fault("DEBUG_DETACH_FAILED")
						}
						child.detached = true
						return nil
					})
				if e != nil {
					return nil, e
				}
				return proc, nil
			}
			if multi270LoaderBreakpoint(exception, uint32(address), child.ntdllBase, child.ntdllSize, child.eventTID, child.pi.ThreadId, loaderBreakSeen) {
				loaderBreakSeen = true
			} else {
				status = 0x80010001
			} // NOT_HANDLED: preserve unrelated exceptions.
		}
		if e = child.continuation(status); e != nil {
			return nil, e
		}
	}
}
