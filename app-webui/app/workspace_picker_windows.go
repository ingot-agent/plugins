//go:build windows

package appcomponent

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

type winGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type winFileDialog struct{ vtable *winFileDialogVTable }
type winFileDialogVTable struct {
	queryInterface      uintptr
	addRef              uintptr
	release             uintptr
	show                uintptr
	setFileTypes        uintptr
	setFileTypeIndex    uintptr
	getFileTypeIndex    uintptr
	advise              uintptr
	unadvise            uintptr
	setOptions          uintptr
	getOptions          uintptr
	setDefaultFolder    uintptr
	setFolder           uintptr
	getFolder           uintptr
	getCurrentSelection uintptr
	setFileName         uintptr
	getFileName         uintptr
	setTitle            uintptr
	setOKButtonLabel    uintptr
	setFileNameLabel    uintptr
	getResult           uintptr
	addPlace            uintptr
	setDefaultExtension uintptr
	close               uintptr
	setClientGUID       uintptr
	clearClientData     uintptr
	setFilter           uintptr
	getResults          uintptr
	getSelectedItems    uintptr
}

type winShellItem struct{ vtable *winShellItemVTable }
type winShellItemVTable struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	bindToHandler  uintptr
	getParent      uintptr
	getDisplayName uintptr
	getAttributes  uintptr
	compare        uintptr
}

var (
	ole32                    = syscall.NewLazyDLL("ole32.dll")
	shell32                  = syscall.NewLazyDLL("shell32.dll")
	user32                   = syscall.NewLazyDLL("user32.dll")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32.NewProc("CoUninitialize")
	procCoCreateInstance     = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
	procSHCreateItemFromPath = shell32.NewProc("SHCreateItemFromParsingName")
	procEnumThreadWindows    = user32.NewProc("EnumThreadWindows")
	procPostMessageW         = user32.NewProc("PostMessageW")
	procGetCurrentThreadID   = kernel32.NewProc("GetCurrentThreadId")
)

var (
	clsidFileOpenDialog = winGUID{0xdc1c5a9c, 0xe88a, 0x4dde, [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidFileOpenDialog   = winGUID{0xd57c7288, 0xd4ad, 0x4768, [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
	iidShellItem        = winGUID{0x43826d1e, 0xe718, 0x42ee, [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	fosPickFolders          = 0x20
	fosForceFileSystem      = 0x40
	fosNoChangeDir          = 0x8
	sigdnFileSystemPath     = 0x80058000
	hresultCanceled         = 0x800704c7
	wmClose                 = 0x0010
	windowsPickerCancelWait = 3500 * time.Millisecond
)

func selectNativeWorkspace(ctx context.Context, initialPath string) (string, bool, error) {
	type result struct {
		path     string
		canceled bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		path, canceled, err := showWindowsWorkspacePicker(ctx, initialPath)
		done <- result{path: path, canceled: canceled, err: err}
	}()
	select {
	case result := <-done:
		return result.path, result.canceled, result.err
	case <-ctx.Done():
		timer := time.NewTimer(windowsPickerCancelWait)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		return "", false, ctx.Err()
	}
}

func showWindowsWorkspacePicker(ctx context.Context, initialPath string) (string, bool, error) {
	hResult, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if winHRESULTFailed(hResult) {
		return "", false, winHRESULTError("initialize COM", hResult)
	}
	defer procCoUninitialize.Call()

	var dialog *winFileDialog
	hResult, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)), uintptr(unsafe.Pointer(&dialog)))
	if winHRESULTFailed(hResult) {
		return "", false, winHRESULTError("create IFileOpenDialog", hResult)
	}
	defer dialog.release()

	options, err := dialog.options()
	if err != nil {
		return "", false, err
	}
	if err := dialog.setOptions(options | fosPickFolders | fosForceFileSystem | fosNoChangeDir); err != nil {
		return "", false, err
	}
	if err := dialog.setTitle("Select Workspace Directory"); err != nil {
		return "", false, err
	}
	initialItem, err := winShellItemForPath(initialPath)
	if err != nil {
		return "", false, err
	}
	if initialItem != nil {
		defer initialItem.release()
		if err := dialog.setFolder(initialItem); err != nil {
			return "", false, err
		}
	}

	showDone, cancelDone := make(chan struct{}), make(chan struct{})
	threadID, _, _ := procGetCurrentThreadID.Call()
	// Keep cancellation outside COM: releasing a marshaled dialog proxy after
	// Show returns can call back into this waiting STA and deadlock the request.
	go cancelWindowsWorkspacePicker(ctx, uint32(threadID), showDone, cancelDone)

	hResult, _, _ = syscall.SyscallN(dialog.vtable.show, uintptr(unsafe.Pointer(dialog)), 0)
	close(showDone)
	<-cancelDone
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", false, ctxErr
	}
	if uint32(hResult) == hresultCanceled {
		return "", true, nil
	}
	if winHRESULTFailed(hResult) {
		return "", false, winHRESULTError("show IFileOpenDialog", hResult)
	}

	item, err := dialog.result()
	if err != nil {
		return "", false, err
	}
	defer item.release()
	return item.fileSystemPath()
}

func cancelWindowsWorkspacePicker(ctx context.Context, threadID uint32, showDone <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	waitForWindowsWorkspacePicker(ctx, showDone, func() {
		closeWindowsPickerThread(threadID, showDone)
	})
}

func waitForWindowsWorkspacePicker(ctx context.Context, showDone <-chan struct{}, closePicker func()) {
	select {
	case <-ctx.Done():
		closePicker()
	case <-showDone:
	}
}

func closeWindowsPickerThread(threadID uint32, showDone <-chan struct{}) {
	callback := syscall.NewCallback(func(window, _ uintptr) uintptr {
		procPostMessageW.Call(window, wmClose, 0, 0)
		return 1
	})
	for range 20 {
		procEnumThreadWindows.Call(uintptr(threadID), callback, 0)
		select {
		case <-showDone:
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func winShellItemForPath(path string) (*winShellItem, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode initial workspace path: %w", err)
	}
	var item *winShellItem
	hResult, _, _ := procSHCreateItemFromPath.Call(
		uintptr(unsafe.Pointer(pointer)), 0, uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item)))
	if winHRESULTFailed(hResult) {
		return nil, winHRESULTError("create initial workspace shell item", hResult)
	}
	return item, nil
}

func (dialog *winFileDialog) release() {
	if dialog != nil {
		syscall.SyscallN(dialog.vtable.release, uintptr(unsafe.Pointer(dialog)))
	}
}

func (dialog *winFileDialog) options() (uint32, error) {
	var options uint32
	hResult, _, _ := syscall.SyscallN(dialog.vtable.getOptions, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&options)))
	if winHRESULTFailed(hResult) {
		return 0, winHRESULTError("read IFileOpenDialog options", hResult)
	}
	return options, nil
}

func (dialog *winFileDialog) setOptions(options uint32) error {
	hResult, _, _ := syscall.SyscallN(dialog.vtable.setOptions, uintptr(unsafe.Pointer(dialog)), uintptr(options))
	if winHRESULTFailed(hResult) {
		return winHRESULTError("set IFileOpenDialog options", hResult)
	}
	return nil
}

func (dialog *winFileDialog) setTitle(title string) error {
	pointer, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	hResult, _, _ := syscall.SyscallN(dialog.vtable.setTitle, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(pointer)))
	if winHRESULTFailed(hResult) {
		return winHRESULTError("set IFileOpenDialog title", hResult)
	}
	return nil
}

func (dialog *winFileDialog) setFolder(item *winShellItem) error {
	hResult, _, _ := syscall.SyscallN(dialog.vtable.setFolder, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(item)))
	if winHRESULTFailed(hResult) {
		return winHRESULTError("set IFileOpenDialog initial folder", hResult)
	}
	return nil
}

func (dialog *winFileDialog) result() (*winShellItem, error) {
	var item *winShellItem
	hResult, _, _ := syscall.SyscallN(dialog.vtable.getResult, uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&item)))
	if winHRESULTFailed(hResult) {
		return nil, winHRESULTError("get IFileOpenDialog result", hResult)
	}
	return item, nil
}

func (item *winShellItem) release() {
	if item != nil {
		syscall.SyscallN(item.vtable.release, uintptr(unsafe.Pointer(item)))
	}
}

func (item *winShellItem) fileSystemPath() (string, bool, error) {
	var pointer *uint16
	hResult, _, _ := syscall.SyscallN(
		item.vtable.getDisplayName, uintptr(unsafe.Pointer(item)), sigdnFileSystemPath, uintptr(unsafe.Pointer(&pointer)))
	if winHRESULTFailed(hResult) {
		return "", false, winHRESULTError("read selected workspace path", hResult)
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pointer)))
	path := winUTF16PointerToString(pointer)
	return path, path == "", nil
}

func winUTF16PointerToString(pointer *uint16) string {
	if pointer == nil {
		return ""
	}
	values := make([]uint16, 0, 260)
	for current := unsafe.Pointer(pointer); ; current = unsafe.Add(current, unsafe.Sizeof(*pointer)) {
		value := *(*uint16)(current)
		if value == 0 {
			break
		}
		values = append(values, value)
	}
	return syscall.UTF16ToString(values)
}

func winHRESULTFailed(hResult uintptr) bool { return int32(uint32(hResult)) < 0 }

func winHRESULTError(operation string, hResult uintptr) error {
	return fmt.Errorf("%s: HRESULT 0x%08x", operation, uint32(hResult))
}
