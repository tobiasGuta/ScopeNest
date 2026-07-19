//go:build windows

package browser

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"syscall"
	"time"
	"unsafe"
)

const (
	visualIdentityPollInterval = 150 * time.Millisecond
	visualIdentityTimeout      = 10 * time.Second
	minimumDWMColorBuild       = 22000
	initialJobPIDCapacity      = 64
	maxJobProcessIDs           = 4096
	errorMoreData              = syscall.Errno(234)
	// GW_OWNER is 4 in the Microsoft GetWindow uCmd contract.
	// https://learn.microsoft.com/windows/win32/api/winuser/nf-winuser-getwindow
	gwOwner = 4
	// DWMWA_BORDER_COLOR, DWMWA_CAPTION_COLOR, and DWMWA_TEXT_COLOR are
	// consecutive values 34-36 and are supported starting with Windows 11 22000.
	// https://learn.microsoft.com/windows/win32/api/dwmapi/ne-dwmapi-dwmwindowattribute
	dwmwaBorderColor  = 34
	dwmwaCaptionColor = 35
	dwmwaTextColor    = 36
	// E_NOTIMPL is a standard HRESULT; 0x80070078 is
	// HRESULT_FROM_WIN32(ERROR_CALL_NOT_IMPLEMENTED).
	// https://learn.microsoft.com/windows/win32/seccrypto/common-hresult-values
	// https://learn.microsoft.com/windows/win32/api/winerror/nf-winerror-hresult_from_win32
	hresultNotImplemented     = 0x80004001
	hresultCallNotImplemented = 0x80070078
)

var errDWMUnsupported = errors.New("DWM visual identity is unsupported")

var (
	user32                   = syscall.NewLazyDLL("user32.dll")
	enumWindowsProc          = user32.NewProc("EnumWindows")
	isWindowVisibleProc      = user32.NewProc("IsWindowVisible")
	getWindowProc            = user32.NewProc("GetWindow")
	getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	dwmapi                   = syscall.NewLazyDLL("dwmapi.dll")
	dwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	ntdll                    = syscall.NewLazyDLL("ntdll.dll")
	rtlGetVersion            = ntdll.NewProc("RtlGetVersion")
)

type jobQuery func(job uintptr, class uint32, buffer unsafe.Pointer, bufferSize uint32, returnLength *uint32) (uintptr, error)

type initialWindowAPI interface {
	topLevelWindows() ([]uintptr, error)
	processID(window uintptr) (uint32, error)
	isVisible(window uintptr) bool
	owner(window uintptr) uintptr
	style(window uintptr, background, text rgbColor) error
}

type identityClock interface {
	now() time.Time
	sleep(time.Duration)
}

type systemIdentityClock struct{}

func (systemIdentityClock) now() time.Time            { return time.Now() }
func (systemIdentityClock) sleep(delay time.Duration) { time.Sleep(delay) }

type windowsIdentityAPI struct {
	buildNumber func() (uint32, error)
}

type rtlOSVersionInfo struct {
	size         uint32
	majorVersion uint32
	minorVersion uint32
	buildNumber  uint32
	platformID   uint32
	servicePack  [128]uint16
}

func startVisualIdentity(process Process, identity VisualIdentity) {
	managed, ok := process.(*jobProcess)
	if !ok {
		return
	}
	color, err := parseRGB(identity.Color)
	if err != nil {
		return
	}
	go styleInitialOwnedWindow(
		managed.Running,
		managed.ownedProcessIDs,
		windowsIdentityAPI{buildNumber: windowsBuildNumber},
		systemIdentityClock{},
		color,
	)
}

func styleInitialOwnedWindow(
	running func() bool,
	ownedProcessIDs func() (map[uint32]struct{}, error),
	windows initialWindowAPI,
	clock identityClock,
	color rgbColor,
) {
	deadline := clock.now().Add(visualIdentityTimeout)
	for {
		if !running() {
			return
		}

		owned, err := ownedProcessIDs()
		if err == nil {
			candidates, enumerateErr := windows.topLevelWindows()
			if enumerateErr == nil {
				for _, window := range candidates {
					if !windows.isVisible(window) || windows.owner(window) != 0 {
						continue
					}
					pid, pidErr := windows.processID(window)
					if pidErr != nil {
						continue
					}
					if _, belongsToJob := owned[pid]; !belongsToJob {
						continue
					}
					confirmed, confirmErr := ownedProcessIDs()
					if confirmErr != nil {
						continue
					}
					currentPID, currentPIDErr := windows.processID(window)
					if currentPIDErr != nil || currentPID != pid || !windows.isVisible(window) || windows.owner(window) != 0 {
						continue
					}
					if _, stillBelongsToJob := confirmed[currentPID]; !stillBelongsToJob {
						continue
					}
					// Styling is intentionally best effort and must never affect
					// process lifetime or the launch response.
					styleErr := windows.style(window, color, contrastingTextColor(color))
					if styleErr == nil || errors.Is(styleErr, errDWMUnsupported) {
						return
					}
				}
			}
		}

		if !clock.now().Before(deadline) {
			return
		}
		clock.sleep(visualIdentityPollInterval)
	}
}

func systemJobQuery(job uintptr, class uint32, buffer unsafe.Pointer, bufferSize uint32, returnLength *uint32) (uintptr, error) {
	result, _, callErr := queryInformationJobObject.Call(
		job,
		uintptr(class),
		uintptr(buffer),
		uintptr(bufferSize),
		uintptr(unsafe.Pointer(returnLength)),
	)
	return result, callErr
}

func queryJobProcessIDs(job syscall.Handle, query jobQuery) (map[uint32]struct{}, error) {
	capacity := initialJobPIDCapacity
	for capacity <= maxJobProcessIDs {
		buffer := make([]byte, 8+capacity*int(unsafe.Sizeof(uintptr(0))))
		var returnedBytes uint32
		result, callErr := query(
			uintptr(job),
			jobObjectBasicProcessIDList,
			unsafe.Pointer(&buffer[0]),
			uint32(len(buffer)),
			&returnedBytes,
		)
		assigned := int(binary.LittleEndian.Uint32(buffer[0:4]))
		returned := int(binary.LittleEndian.Uint32(buffer[4:8]))

		if assigned > maxJobProcessIDs || returned > maxJobProcessIDs {
			return nil, errors.New("browser job contains too many processes")
		}
		if result != 0 && returned >= assigned {
			return parseJobProcessIDs(buffer)
		}
		if result == 0 && !errors.Is(callErr, errorMoreData) && assigned <= capacity {
			return nil, fmt.Errorf("query browser job process IDs: %w", callErr)
		}

		next := capacity * 2
		if assigned > next {
			next = assigned
		}
		if next <= capacity || next > maxJobProcessIDs {
			return nil, errors.New("browser job process ID list exceeds the allocation limit")
		}
		capacity = next
	}
	return nil, errors.New("browser job process ID list exceeds the allocation limit")
}

func parseJobProcessIDs(buffer []byte) (map[uint32]struct{}, error) {
	if len(buffer) < 8 {
		return nil, errors.New("browser job process ID response is truncated")
	}
	assigned := int(binary.LittleEndian.Uint32(buffer[0:4]))
	returned := int(binary.LittleEndian.Uint32(buffer[4:8]))
	if returned > assigned || returned > maxJobProcessIDs {
		return nil, errors.New("browser job process ID response is invalid")
	}
	pointerSize := int(unsafe.Sizeof(uintptr(0)))
	required := 8 + returned*pointerSize
	if required > len(buffer) {
		return nil, errors.New("browser job process ID response is truncated")
	}

	result := make(map[uint32]struct{}, returned)
	for index := 0; index < returned; index++ {
		offset := 8 + index*pointerSize
		var value uint64
		if pointerSize == 8 {
			value = binary.LittleEndian.Uint64(buffer[offset : offset+8])
		} else {
			value = uint64(binary.LittleEndian.Uint32(buffer[offset : offset+4]))
		}
		if value == 0 || value > math.MaxUint32 {
			return nil, errors.New("browser job process ID response contains an invalid process ID")
		}
		result[uint32(value)] = struct{}{}
	}
	return result, nil
}

func (windowsIdentityAPI) topLevelWindows() ([]uintptr, error) {
	windows := make([]uintptr, 0, 16)
	callback := syscall.NewCallback(func(window, _ uintptr) uintptr {
		windows = append(windows, window)
		return 1
	})
	result, _, callErr := enumWindowsProc.Call(callback, 0)
	if result == 0 {
		return nil, fmt.Errorf("enumerate top-level windows: %w", callErr)
	}
	return windows, nil
}

func (windowsIdentityAPI) processID(window uintptr) (uint32, error) {
	var pid uint32
	threadID, _, callErr := getWindowThreadProcessID.Call(window, uintptr(unsafe.Pointer(&pid)))
	if threadID == 0 || pid == 0 {
		return 0, fmt.Errorf("read window process ID: %w", callErr)
	}
	return pid, nil
}

func (windowsIdentityAPI) isVisible(window uintptr) bool {
	result, _, _ := isWindowVisibleProc.Call(window)
	return result != 0
}

func (windowsIdentityAPI) owner(window uintptr) uintptr {
	result, _, _ := getWindowProc.Call(window, gwOwner)
	return result
}

func (api windowsIdentityAPI) style(window uintptr, background, text rgbColor) error {
	readBuild := api.buildNumber
	if readBuild == nil {
		readBuild = windowsBuildNumber
	}
	build, err := readBuild()
	if err != nil {
		return err
	}
	if build < minimumDWMColorBuild {
		return errDWMUnsupported
	}
	backgroundRef := colorRef(background)
	if err := setDWMColor(window, dwmwaBorderColor, backgroundRef); err != nil {
		return err
	}
	// Caption coloring is newer Windows behavior and may not be honored by
	// Chromium's custom frame. Keep both calls best effort after the border.
	_ = setDWMColor(window, dwmwaCaptionColor, backgroundRef)
	_ = setDWMColor(window, dwmwaTextColor, colorRef(text))
	return nil
}

func setDWMColor(window uintptr, attribute uintptr, color uint32) error {
	result, _, _ := dwmSetWindowAttribute.Call(
		window,
		attribute,
		uintptr(unsafe.Pointer(&color)),
		unsafe.Sizeof(color),
	)
	if int32(result) < 0 {
		if isUnsupportedDWMHRESULT(uint32(result)) {
			return fmt.Errorf("%w (HRESULT 0x%08x)", errDWMUnsupported, uint32(result))
		}
		return fmt.Errorf("DwmSetWindowAttribute failed with HRESULT 0x%08x", uint32(result))
	}
	return nil
}

func isUnsupportedDWMHRESULT(result uint32) bool {
	switch result {
	case hresultNotImplemented, hresultCallNotImplemented:
		return true
	default:
		return false
	}
}

func windowsBuildNumber() (uint32, error) {
	// RtlGetVersion returns the actual running build and requires the structure
	// size to be initialized before the call.
	// https://learn.microsoft.com/windows/win32/devnotes/rtlgetversion
	info := rtlOSVersionInfo{size: uint32(unsafe.Sizeof(rtlOSVersionInfo{}))}
	status, _, _ := rtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	if status != 0 {
		return 0, fmt.Errorf("RtlGetVersion failed with NTSTATUS 0x%08x", uint32(status))
	}
	return info.buildNumber, nil
}

func colorRef(color rgbColor) uint32 {
	// COLORREF is 0x00bbggrr, not the textual #RRGGBB order.
	// https://learn.microsoft.com/windows/win32/gdi/colorref
	return uint32(color.red) | uint32(color.green)<<8 | uint32(color.blue)<<16
}
