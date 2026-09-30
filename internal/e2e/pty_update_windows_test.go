//go:build windows

package e2e

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func updatePtyAttribute(attributes *windows.ProcThreadAttributeListContainer, console windows.Handle) error {
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	result, _, callErr := syscall.SyscallN(
		procedure.Addr(),
		uintptr(unsafe.Pointer(attributes.List())),
		0,
		uintptr(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE),
		uintptr(console),
		unsafe.Sizeof(console),
		0,
		0,
	)
	if result == 0 {
		if callErr != 0 {
			return callErr
		}
		return windows.ERROR_INVALID_PARAMETER
	}
	return nil
}
