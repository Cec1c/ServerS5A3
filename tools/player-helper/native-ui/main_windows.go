package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Immutable build inputs, never caller-supplied DLLs, image identities or RVAs.
var expectedGameSHA = "bb7022670b546ab200bc6f9718a47e51ff1e8cc78b4049231229db72b6aa3552"
var expectedDLLSHA string
var expectedGameName = "DNF.exe"
var kernel = syscall.NewLazyDLL("kernel32.dll")
var user = syscall.NewLazyDLL("user32.dll")
var security = syscall.NewLazyDLL("advapi32.dll")

func wide(s string) *uint16 { return syscall.StringToUTF16Ptr(s) }
func last(call *syscall.LazyProc, args ...uintptr) uintptr {
	r, _, e := call.Call(args...)
	if r == 0 {
		panic(fmt.Errorf("%s: %w", call.Name, e))
	}
	return r
}
func hash(path string) string {
	f, e := os.Open(path)
	must(e)
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	must(e)
	return hex.EncodeToString(h.Sum(nil))
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func require(ok bool, msg string) {
	if !ok {
		panic(errors.New(msg))
	}
}

type command struct {
	Magic, Version, Bytes, PID                        uint32
	Created                                           uint64
	Window, Nonce, Action, State, Error, UI, Deadline uint32
	Reserved                                          [3]uint32
}
type attributes struct {
	Length     uint32
	Descriptor uintptr
	Inherit    uint32
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	code := 1
	defer func() {
		r := recover()
		if r != nil {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": fmt.Sprint(r)})
		}
		os.Exit(code)
	}()
	pid := flag.Uint("pid", 0, "")
	created := flag.Uint64("created", 0, "")
	hwnd := flag.Uint("window", 0, "")
	client := flag.String("client", "", "")
	flag.Parse()
	require(*pid > 0 && *created > 0 && *hwnd > 0 && *client != "" && flag.NArg() == 0, "invalid instance")
	require(unsafe.Sizeof(command{}) == 64 && unsafe.Sizeof(uintptr(0)) == 4, "x86 broker required")
	process, e := syscall.OpenProcess(0x101000, false, uint32(*pid))
	must(e)
	defer syscall.CloseHandle(process)
	var born, exit, kTime, uTime uint64
	last(kernel.NewProc("GetProcessTimes"), uintptr(process), uintptr(unsafe.Pointer(&born)), uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kTime)), uintptr(unsafe.Pointer(&uTime)))
	require(born == *created, "game instance changed")
	var session, ownSession uint32
	last(kernel.NewProc("ProcessIdToSessionId"), uintptr(*pid), uintptr(unsafe.Pointer(&session)))
	last(kernel.NewProc("ProcessIdToSessionId"), uintptr(os.Getpid()), uintptr(unsafe.Pointer(&ownSession)))
	require(session == ownSession, "game is in another desktop session")
	var targetToken, selfToken syscall.Token
	must(syscall.OpenProcessToken(process, syscall.TOKEN_QUERY, &targetToken))
	defer targetToken.Close()
	selfHandle, _, _ := kernel.NewProc("GetCurrentProcess").Call()
	must(syscall.OpenProcessToken(syscall.Handle(selfHandle), syscall.TOKEN_QUERY, &selfToken))
	defer selfToken.Close()
	t, e := targetToken.GetTokenUser()
	must(e)
	me, e := selfToken.GetTokenUser()
	must(e)
	targetSID, e := t.User.Sid.String()
	must(e)
	ownSID, e := me.User.Sid.String()
	must(e)
	require(targetSID == ownSID, "game belongs to another Windows user")
	path := make([]uint16, 32768)
	length := uint32(len(path))
	last(kernel.NewProc("QueryFullProcessImageNameW"), uintptr(process), 0, uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&length)))
	image := syscall.UTF16ToString(path[:length])
	wanted, e := filepath.Abs(*client)
	must(e)
	require(strings.EqualFold(image, wanted) && strings.EqualFold(filepath.Base(image), expectedGameName) && hash(image) == expectedGameSHA, "unapproved game image")
	var windowPID uint32
	thread := last(user.NewProc("GetWindowThreadProcessId"), uintptr(*hwnd), uintptr(unsafe.Pointer(&windowPID)))
	require(windowPID == uint32(*pid), "window belongs to another process")
	foreground, _, _ := user.NewProc("GetForegroundWindow").Call()
	var frontPID uint32
	last(user.NewProc("GetWindowThreadProcessId"), foreground, uintptr(unsafe.Pointer(&frontPID)))
	require(frontPID == uint32(*pid), "game is not the foreground window")
	self, e := os.Executable()
	must(e)
	dll := filepath.Join(filepath.Dir(self), "Arad.Helper.NativeUI.dll")
	require(len(expectedDLLSHA) == 64 && hash(dll) == expectedDLLSHA, "native bridge checksum mismatch")
	var random [4]byte
	_, e = rand.Read(random[:])
	must(e)
	nonce := binary.LittleEndian.Uint32(random[:])
	require(nonce != 0, "empty command nonce")
	name := fmt.Sprintf("Local\\Arad.Helper.NativeUI.%d.%08X", *pid, nonce)
	var descriptor uintptr
	last(security.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW"), uintptr(unsafe.Pointer(wide("D:P(A;;GA;;;SY)(A;;GA;;;"+ownSID+")"))), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	defer kernel.NewProc("LocalFree").Call(descriptor)
	sa := attributes{uint32(unsafe.Sizeof(attributes{})), descriptor, 0}
	mapping := last(kernel.NewProc("CreateFileMappingW"), ^uintptr(0), uintptr(unsafe.Pointer(&sa)), 4, 0, 64, uintptr(unsafe.Pointer(wide(name))))
	defer syscall.CloseHandle(syscall.Handle(mapping))
	view := last(kernel.NewProc("MapViewOfFile"), mapping, 6, 0, 0, 64)
	defer kernel.NewProc("UnmapViewOfFile").Call(view)
	q := (*command)(unsafe.Pointer(view))
	tick, _, _ := kernel.NewProc("GetTickCount").Call()
	*q = command{Magic: 0x31554841, Version: 1, Bytes: 64, PID: uint32(*pid), Created: *created, Window: uint32(*hwnd), Nonce: nonce, Action: 1, UI: 198, Deadline: uint32(tick) + 5000}
	event := last(kernel.NewProc("CreateEventW"), uintptr(unsafe.Pointer(&sa)), 0, 0, uintptr(unsafe.Pointer(wide(name+".done"))))
	defer syscall.CloseHandle(syscall.Handle(event))
	module := last(kernel.NewProc("LoadLibraryExW"), uintptr(unsafe.Pointer(wide(dll))), 0, 0x900)
	defer kernel.NewProc("FreeLibrary").Call(module)
	symbol := uintptr(0)
	for _, name := range []string{"AradWarehouseHook", "AradWarehouseHook@12", "_AradWarehouseHook@12"} {
		p, _ := syscall.BytePtrFromString(name)
		symbol, _, _ = kernel.NewProc("GetProcAddress").Call(module, uintptr(unsafe.Pointer(p)))
		if symbol != 0 {
			break
		}
	}
	require(symbol != 0, "native hook export missing")
	hook := last(user.NewProc("SetWindowsHookExW"), 3, symbol, module, thread)
	defer user.NewProc("UnhookWindowsHookEx").Call(hook)
	last(user.NewProc("PostMessageW"), uintptr(*hwnd), 0, uintptr(nonce), 0x4b50554e)
	wait, _, _ := kernel.NewProc("WaitForSingleObject").Call(event, 8000)
	require(wait == 0, "native UI request timed out")
	require(atomic.LoadUint32(&q.State) == 2 && q.Error == 0, fmt.Sprintf("native UI rejected request (%d)", q.Error))
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "windowId": 198, "nativeGameWindow": true, "persistentGamePatch": false})
	code = 0
}
