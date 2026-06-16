package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	appUserModelID = "Shaoye.MultiPortProxy"
	appDisplayName = "MultiPortProxy"

	vtLPWSTR = 31

	coinitApartmentThreaded = 0x2
	rpcEChangedMode         = 0x80010106
)

type guid struct {
	data1 uint32
	data2 uint16
	data3 uint16
	data4 [8]byte
}

type propertyKey struct {
	fmtid guid
	pid   uint32
}

type propVariant struct {
	vt         uint16
	reserved1  uint16
	reserved2  uint16
	reserved3  uint16
	value      uintptr
	valueExtra uintptr
}

type iPropertyStore struct {
	lpVtbl *iPropertyStoreVtbl
}

type iPropertyStoreVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	getCount       uintptr
	getAt          uintptr
	getValue       uintptr
	setValue       uintptr
	commit         uintptr
}

var (
	ole32ForAppIdentity = syscall.NewLazyDLL("ole32.dll")
	shell32AppIdentity  = syscall.NewLazyDLL("shell32.dll")
	procCoInitializeEx  = ole32ForAppIdentity.NewProc("CoInitializeEx")
	procCoUninitialize  = ole32ForAppIdentity.NewProc("CoUninitialize")
	procSHGetPropStore  = shell32AppIdentity.NewProc("SHGetPropertyStoreForWindow")
	procSetProcessAppID = shell32AppIdentity.NewProc("SetCurrentProcessExplicitAppUserModelID")
	iidIPropertyStore   = guid{0x886D8EEB, 0x8CF2, 0x4446, [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	appUserModelFmtID   = guid{0x9F4C2855, 0x9F79, 0x4B39, [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}
	pkeyRelaunchCommand = propertyKey{appUserModelFmtID, 2}
	pkeyRelaunchIcon    = propertyKey{appUserModelFmtID, 3}
	pkeyRelaunchName    = propertyKey{appUserModelFmtID, 4}
	pkeyAppUserModelID  = propertyKey{appUserModelFmtID, 5}
)

func setProcessAppUserModelID() {
	appID, err := syscall.UTF16PtrFromString(appUserModelID)
	if err != nil {
		return
	}
	procSetProcessAppID.Call(uintptr(unsafe.Pointer(appID)))
}

func appExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(exe); err == nil {
		return abs
	}
	return exe
}

func appRelaunchCommand() string {
	exe := appExecutablePath()
	if exe == "" {
		return ""
	}
	return `"` + exe + `"`
}

func appRelaunchIconResource() string {
	exe := appExecutablePath()
	if exe == "" {
		return ""
	}
	return exe + ",0"
}

func applyTaskbarIdentityWhenReady(titles []string) {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		initialized := coInitializeForAppIdentity()
		if initialized {
			defer procCoUninitialize.Call()
		}

		for i := 0; i < 40; i++ {
			applied := false
			for _, h := range appWindowsMatching(titles) {
				if setWindowTaskbarIdentity(uintptr(h)) {
					applied = true
				}
			}
			if applied {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func coInitializeForAppIdentity() bool {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr == 0 {
		return true
	}
	return uint32(hr) != rpcEChangedMode && !hresultFailed(hr)
}

func setWindowTaskbarIdentity(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	var store *iPropertyStore
	hr, _, _ := procSHGetPropStore.Call(
		hwnd,
		uintptr(unsafe.Pointer(&iidIPropertyStore)),
		uintptr(unsafe.Pointer(&store)),
	)
	if hresultFailed(hr) || store == nil {
		return false
	}
	defer store.release()

	command := appRelaunchCommand()
	icon := appRelaunchIconResource()
	if command == "" || icon == "" {
		return false
	}

	// Relaunch 属性必须先于 AppUserModelID 写入，任务栏固定时才会使用本程序的启动命令和图标。
	if !store.setString(&pkeyRelaunchCommand, command) {
		return false
	}
	if !store.setString(&pkeyRelaunchName, appDisplayName) {
		return false
	}
	if !store.setString(&pkeyRelaunchIcon, icon) {
		return false
	}
	if !store.setString(&pkeyAppUserModelID, appUserModelID) {
		return false
	}
	return store.commit()
}

func (s *iPropertyStore) release() {
	syscall.SyscallN(s.lpVtbl.release, uintptr(unsafe.Pointer(s)))
}

func (s *iPropertyStore) setString(key *propertyKey, value string) bool {
	ptr, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return false
	}
	pv := propVariant{vt: vtLPWSTR, value: uintptr(unsafe.Pointer(ptr))}
	hr, _, _ := syscall.SyscallN(
		s.lpVtbl.setValue,
		uintptr(unsafe.Pointer(s)),
		uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(&pv)),
	)
	return !hresultFailed(hr)
}

func (s *iPropertyStore) commit() bool {
	hr, _, _ := syscall.SyscallN(s.lpVtbl.commit, uintptr(unsafe.Pointer(s)))
	return !hresultFailed(hr)
}

func hresultFailed(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}
