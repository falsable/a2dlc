package resources

import (
	"syscall"
	"unsafe"
)

func cgroupCPUs() (float64, bool) { return 0, false }

// loadAvg: Windows has no load average; the CPUs are taken as free.
func loadAvg() (float64, bool) { return 0, false }

type memoryStatusEx struct {
	Length, MemoryLoad                                 uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

var procGlobalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memory is the physical memory and what is available now (GlobalMemoryStatusEx).
func memory() (int64, int64, error) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, 0, err
	}
	return int64(m.TotalPhys), int64(m.AvailPhys), nil
}
