//go:build windows

package tun

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wintunRingCapacity = 0x800000 // 8 MiB ring buffer
	wintunTunnelType   = "Spaceship"
)

type wintunLib struct {
	dll                *windows.LazyDLL
	createAdapter      *windows.LazyProc
	openAdapter        *windows.LazyProc
	closeAdapter       *windows.LazyProc
	getAdapterLUID     *windows.LazyProc
	startSession       *windows.LazyProc
	endSession         *windows.LazyProc
	getReadWaitEvent   *windows.LazyProc
	receivePacket      *windows.LazyProc
	releaseRecvPacket  *windows.LazyProc
	allocateSendPacket *windows.LazyProc
	sendPacket         *windows.LazyProc
}

var (
	wintunLoadMu sync.Mutex
	wintunCache  = make(map[string]*wintunLib)
)

func loadWintun(customPath string) (*wintunLib, error) {
	path := resolveWintunPath(customPath)
	wintunLoadMu.Lock()
	defer wintunLoadMu.Unlock()
	if lib, ok := wintunCache[path]; ok {
		return lib, nil
	}
	dll := windows.NewLazyDLL(path)
	if err := dll.Load(); err != nil {
		return nil, fmt.Errorf("tun: load wintun.dll (%s): %w (place wintun.dll next to the executable or set tun.wintun_path)", path, err)
	}
	lib := &wintunLib{
		dll:                dll,
		createAdapter:      dll.NewProc("WintunCreateAdapter"),
		openAdapter:        dll.NewProc("WintunOpenAdapter"),
		closeAdapter:       dll.NewProc("WintunCloseAdapter"),
		getAdapterLUID:     dll.NewProc("WintunGetAdapterLUID"),
		startSession:       dll.NewProc("WintunStartSession"),
		endSession:         dll.NewProc("WintunEndSession"),
		getReadWaitEvent:   dll.NewProc("WintunGetReadWaitEvent"),
		receivePacket:      dll.NewProc("WintunReceivePacket"),
		releaseRecvPacket:  dll.NewProc("WintunReleaseReceivePacket"),
		allocateSendPacket: dll.NewProc("WintunAllocateSendPacket"),
		sendPacket:         dll.NewProc("WintunSendPacket"),
	}
	for _, proc := range []*windows.LazyProc{
		lib.createAdapter,
		lib.openAdapter,
		lib.closeAdapter,
		lib.getAdapterLUID,
		lib.startSession,
		lib.endSession,
		lib.getReadWaitEvent,
		lib.receivePacket,
		lib.releaseRecvPacket,
		lib.allocateSendPacket,
		lib.sendPacket,
	} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("tun: wintun.dll missing required symbol %q: %w", proc.Name, err)
		}
	}
	wintunCache[path] = lib
	return lib, nil
}

func resolveWintunPath(customPath string) string {
	if customPath != "" {
		return customPath
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "wintun.dll")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "wintun.dll"
}

func deterministicWintunGUID(name string) windows.GUID {
	sum := sha256.Sum256([]byte("spaceship-wintun:" + name))
	return windows.GUID{
		Data1: uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3]),
		Data2: uint16(sum[4])<<8 | uint16(sum[5]),
		Data3: uint16(sum[6])<<8 | uint16(sum[7]),
		Data4: [8]byte{sum[8], sum[9], sum[10], sum[11], sum[12], sum[13], sum[14], sum[15]},
	}
}

type wintunAdapter struct {
	lib    *wintunLib
	handle uintptr
}

func (l *wintunLib) CreateOrOpenAdapter(name string) (*wintunAdapter, error) {
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	type16, err := windows.UTF16PtrFromString(wintunTunnelType)
	if err != nil {
		return nil, err
	}
	guid := deterministicWintunGUID(name)
	r0, _, e1 := syscall.SyscallN(
		l.createAdapter.Addr(),
		uintptr(unsafe.Pointer(name16)),
		uintptr(unsafe.Pointer(type16)),
		uintptr(unsafe.Pointer(&guid)),
	)
	if r0 != 0 {
		return &wintunAdapter{lib: l, handle: r0}, nil
	}
	createErr := error(e1)
	if errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
		r1, _, e2 := syscall.SyscallN(l.openAdapter.Addr(), uintptr(unsafe.Pointer(name16)))
		if r1 != 0 {
			return &wintunAdapter{lib: l, handle: r1}, nil
		}
		return nil, fmt.Errorf("tun: create adapter: %w; open existing adapter: %w", createErr, error(e2))
	}
	return nil, fmt.Errorf("tun: create wintun adapter %q: %w", name, createErr)
}

func (a *wintunAdapter) LUID() uint64 {
	var luid uint64
	_, _, _ = syscall.SyscallN(a.lib.getAdapterLUID.Addr(), a.handle, uintptr(unsafe.Pointer(&luid)))
	return luid
}

func (a *wintunAdapter) Close() error {
	if a.handle == 0 {
		return nil
	}
	_, _, _ = syscall.SyscallN(a.lib.closeAdapter.Addr(), a.handle)
	a.handle = 0
	return nil
}

type wintunSession struct {
	lib    *wintunLib
	handle uintptr
}

func (a *wintunAdapter) StartSession(capacity uint32) (*wintunSession, error) {
	r0, _, e1 := syscall.SyscallN(a.lib.startSession.Addr(), a.handle, uintptr(capacity))
	if r0 == 0 {
		return nil, fmt.Errorf("tun: start wintun session: %w", error(e1))
	}
	return &wintunSession{lib: a.lib, handle: r0}, nil
}

func (s *wintunSession) End() {
	if s.handle == 0 {
		return
	}
	_, _, _ = syscall.SyscallN(s.lib.endSession.Addr(), s.handle)
	s.handle = 0
}

func (s *wintunSession) ReadWaitEvent() windows.Handle {
	r0, _, _ := syscall.SyscallN(s.lib.getReadWaitEvent.Addr(), s.handle)
	return windows.Handle(r0)
}

// wintunRingSlice wraps a non-Go pointer returned by wintun.dll's shared ring
// buffer without tripping go vet's unsafeptr check for Go-heap conversions.
func wintunRingSlice(addr uintptr, size int) []byte {
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
	return unsafe.Slice((*byte)(ptr), size)
}

func (s *wintunSession) ReceivePacket() ([]byte, error) {
	var size uint32
	r0, _, e1 := syscall.SyscallN(s.lib.receivePacket.Addr(), s.handle, uintptr(unsafe.Pointer(&size)))
	if r0 == 0 {
		return nil, error(e1)
	}
	return wintunRingSlice(r0, int(size)), nil
}

func (s *wintunSession) ReleaseReceivePacket(pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	_, _, _ = syscall.SyscallN(s.lib.releaseRecvPacket.Addr(), s.handle, uintptr(unsafe.Pointer(&pkt[0])))
}

func (s *wintunSession) AllocateSendPacket(size int) ([]byte, error) {
	r0, _, e1 := syscall.SyscallN(s.lib.allocateSendPacket.Addr(), s.handle, uintptr(size))
	if r0 == 0 {
		return nil, error(e1)
	}
	return wintunRingSlice(r0, size), nil
}

func (s *wintunSession) SendPacket(pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	_, _, _ = syscall.SyscallN(s.lib.sendPacket.Addr(), s.handle, uintptr(unsafe.Pointer(&pkt[0])))
}
