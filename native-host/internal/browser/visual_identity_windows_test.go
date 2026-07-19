//go:build windows

package browser

import (
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type fakeIdentityClock struct {
	current time.Time
	sleeps  int
}

func (c *fakeIdentityClock) now() time.Time { return c.current }
func (c *fakeIdentityClock) sleep(delay time.Duration) {
	c.current = c.current.Add(delay)
	c.sleeps++
}

type fakeWindow struct {
	pid     uint32
	visible bool
	owner   uintptr
}

type fakeWindowAPI struct {
	order       []uintptr
	windows     map[uintptr]fakeWindow
	styleCalls  []uintptr
	styleError  error
	styleErrors map[uintptr]error
	appearAfter int
	enumerated  int
}

func (f *fakeWindowAPI) topLevelWindows() ([]uintptr, error) {
	f.enumerated++
	if f.enumerated <= f.appearAfter {
		return nil, nil
	}
	return append([]uintptr(nil), f.order...), nil
}
func (f *fakeWindowAPI) processID(window uintptr) (uint32, error) {
	value, ok := f.windows[window]
	if !ok {
		return 0, errors.New("unknown fake window")
	}
	return value.pid, nil
}
func (f *fakeWindowAPI) isVisible(window uintptr) bool { return f.windows[window].visible }
func (f *fakeWindowAPI) owner(window uintptr) uintptr  { return f.windows[window].owner }
func (f *fakeWindowAPI) style(window uintptr, _, _ rgbColor) error {
	f.styleCalls = append(f.styleCalls, window)
	if err, ok := f.styleErrors[window]; ok {
		return err
	}
	return f.styleError
}

func TestColorRefUsesWindowsByteOrder(t *testing.T) {
	tests := []struct {
		color rgbColor
		want  uint32
	}{
		{color: rgbColor{red: 0xff}, want: 0x000000ff},
		{color: rgbColor{green: 0xff}, want: 0x0000ff00},
		{color: rgbColor{blue: 0xff}, want: 0x00ff0000},
		{color: rgbColor{red: 0x12, green: 0x34, blue: 0x56}, want: 0x00563412},
	}
	for _, test := range tests {
		if actual := colorRef(test.color); actual != test.want {
			t.Fatalf("colorRef(%#v) = %#08x, want %#08x", test.color, actual, test.want)
		}
	}
}

func TestKnownIdentityColorsConvertToColorRef(t *testing.T) {
	tests := map[string]uint32{
		"#FF0000": 0x000000ff,
		"#00FF00": 0x0000ff00,
		"#0000FF": 0x00ff0000,
		"#2563EB": 0x00eb6325,
		"#7C3AED": 0x00ed3a7c,
		"#0891B2": 0x00b29108,
		"#DC2626": 0x002626dc,
	}
	for value, want := range tests {
		color, err := parseRGB(value)
		if err != nil {
			t.Fatal(err)
		}
		if actual := colorRef(color); actual != want {
			t.Fatalf("colorRef(%s) = %#08x, want %#08x", value, actual, want)
		}
	}
}

func TestInitialWindowSelectionUsesOnlyCurrentJobMembership(t *testing.T) {
	windows := &fakeWindowAPI{
		order: []uintptr{1, 2, 3, 4},
		windows: map[uintptr]fakeWindow{
			1: {pid: 999, visible: true},
			2: {pid: 200, visible: false},
			3: {pid: 200, visible: true, owner: 88},
			4: {pid: 201, visible: true},
		},
	}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	styleInitialOwnedWindow(
		func() bool { return true },
		func() (map[uint32]struct{}, error) {
			return map[uint32]struct{}{200: {}, 201: {}}, nil
		},
		windows,
		clock,
		rgbColor{red: 0x12, green: 0x34, blue: 0x56},
	)
	if len(windows.styleCalls) != 1 || windows.styleCalls[0] != 4 {
		t.Fatalf("styled windows = %#v, want only owned visible top-level window 4", windows.styleCalls)
	}
}

func TestInitialWindowStylingStopsOnConfirmedUnsupportedDWM(t *testing.T) {
	windows := &fakeWindowAPI{
		order:      []uintptr{7},
		windows:    map[uintptr]fakeWindow{7: {pid: 700, visible: true}},
		styleError: errDWMUnsupported,
	}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	styleInitialOwnedWindow(
		func() bool { return true },
		func() (map[uint32]struct{}, error) { return map[uint32]struct{}{700: {}}, nil },
		windows,
		clock,
		rgbColor{},
	)
	if len(windows.styleCalls) != 1 || clock.sleeps != 0 {
		t.Fatalf("style calls = %#v, sleeps = %d; want one confirmed-unsupported attempt", windows.styleCalls, clock.sleeps)
	}
}

func TestInitialWindowStylingContinuesAfterTransientFailure(t *testing.T) {
	windows := &fakeWindowAPI{
		order:       []uintptr{7, 8},
		windows:     map[uintptr]fakeWindow{7: {pid: 700, visible: true}, 8: {pid: 800, visible: true}},
		styleErrors: map[uintptr]error{7: errors.New("transient invalid window")},
	}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	styleInitialOwnedWindow(
		func() bool { return true },
		func() (map[uint32]struct{}, error) { return map[uint32]struct{}{700: {}, 800: {}}, nil },
		windows,
		clock,
		rgbColor{},
	)
	if len(windows.styleCalls) != 2 || windows.styleCalls[0] != 7 || windows.styleCalls[1] != 8 || clock.sleeps != 0 {
		t.Fatalf("style calls = %#v, sleeps = %d; want transient failure followed by success", windows.styleCalls, clock.sleeps)
	}
}

func TestUnsupportedDWMHRESULTsAreDistinguished(t *testing.T) {
	for _, result := range []uint32{hresultNotImplemented, hresultCallNotImplemented} {
		if !isUnsupportedDWMHRESULT(result) {
			t.Fatalf("HRESULT %#08x was not recognized as unsupported", result)
		}
	}
	for _, result := range []uint32{0x80070006, 0x80070057} {
		if isUnsupportedDWMHRESULT(result) {
			t.Fatalf("transient HWND/argument HRESULT %#08x was classified as unsupported", result)
		}
	}
}

func TestWindowsIdentityAPIStopsBeforeDWMOnUnsupportedBuild(t *testing.T) {
	api := windowsIdentityAPI{buildNumber: func() (uint32, error) { return minimumDWMColorBuild - 1, nil }}
	err := api.style(1, rgbColor{}, rgbColor{})
	if !errors.Is(err, errDWMUnsupported) {
		t.Fatalf("style error = %v, want confirmed unsupported result", err)
	}
}

func TestInitialWindowStylingWaitsForAsynchronousWindowCreation(t *testing.T) {
	windows := &fakeWindowAPI{
		order:       []uintptr{8},
		windows:     map[uintptr]fakeWindow{8: {pid: 800, visible: true}},
		appearAfter: 3,
	}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	styleInitialOwnedWindow(
		func() bool { return true },
		func() (map[uint32]struct{}, error) { return map[uint32]struct{}{800: {}}, nil },
		windows,
		clock,
		rgbColor{},
	)
	if windows.enumerated != 4 || clock.sleeps != 3 || len(windows.styleCalls) != 1 {
		t.Fatalf("enumerations=%d sleeps=%d style=%#v; want styling after three polls", windows.enumerated, clock.sleeps, windows.styleCalls)
	}
}

func TestInitialWindowStylingStopsWhenProcessExits(t *testing.T) {
	windows := &fakeWindowAPI{windows: map[uintptr]fakeWindow{}}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	styleInitialOwnedWindow(
		func() bool { return false },
		func() (map[uint32]struct{}, error) { t.Fatal("queried job after process exit"); return nil, nil },
		windows,
		clock,
		rgbColor{},
	)
	if clock.sleeps != 0 || len(windows.styleCalls) != 0 {
		t.Fatal("process exit did not stop styling immediately")
	}
}

func TestInitialWindowStylingStopsWhenProcessExitsDuringPolling(t *testing.T) {
	windows := &fakeWindowAPI{windows: map[uintptr]fakeWindow{}}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	runningCalls := 0
	styleInitialOwnedWindow(
		func() bool {
			runningCalls++
			return runningCalls < 4
		},
		func() (map[uint32]struct{}, error) { return map[uint32]struct{}{}, nil },
		windows,
		clock,
		rgbColor{},
	)
	if clock.sleeps != 3 || len(windows.styleCalls) != 0 {
		t.Fatalf("sleeps=%d style=%#v; want clean exit during polling", clock.sleeps, windows.styleCalls)
	}
}

func TestInitialWindowStylingHasBoundedPolling(t *testing.T) {
	windows := &fakeWindowAPI{windows: map[uintptr]fakeWindow{}}
	clock := &fakeIdentityClock{current: time.Unix(0, 0)}
	styleInitialOwnedWindow(
		func() bool { return true },
		func() (map[uint32]struct{}, error) { return map[uint32]struct{}{}, nil },
		windows,
		clock,
		rgbColor{},
	)
	if clock.sleeps == 0 || clock.sleeps > 68 {
		t.Fatalf("poll count = %d, want a bounded approximately ten-second search", clock.sleeps)
	}
	if elapsed := clock.current.Sub(time.Unix(0, 0)); elapsed > visualIdentityTimeout+visualIdentityPollInterval {
		t.Fatalf("polling exceeded its bound: %s", elapsed)
	}
}

func TestQueryJobProcessIDsRetriesForCompleteCurrentList(t *testing.T) {
	calls := 0
	query := func(_ uintptr, class uint32, buffer unsafe.Pointer, bufferSize uint32, _ *uint32) (uintptr, error) {
		calls++
		if class != jobObjectBasicProcessIDList {
			t.Fatalf("information class = %d, want %d", class, jobObjectBasicProcessIDList)
		}
		data := unsafe.Slice((*byte)(buffer), int(bufferSize))
		binary.LittleEndian.PutUint32(data[0:4], 70)
		returned := 70
		if calls == 1 {
			returned = initialJobPIDCapacity
		}
		binary.LittleEndian.PutUint32(data[4:8], uint32(returned))
		for index := 0; index < returned; index++ {
			putTestProcessID(data, index, uintptr(1000+index))
		}
		return 1, nil
	}

	ids, err := queryJobProcessIDs(syscall.Handle(42), query)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(ids) != 70 {
		t.Fatalf("query calls = %d, IDs = %d; want complete list after one retry", calls, len(ids))
	}
	if _, ok := ids[1069]; !ok {
		t.Fatal("final process ID is missing from complete Job Object list")
	}
}

func TestQueryJobProcessIDsRejectsUnboundedAllocation(t *testing.T) {
	calls := 0
	query := func(_ uintptr, _ uint32, buffer unsafe.Pointer, bufferSize uint32, _ *uint32) (uintptr, error) {
		calls++
		data := unsafe.Slice((*byte)(buffer), int(bufferSize))
		binary.LittleEndian.PutUint32(data[0:4], maxJobProcessIDs+1)
		return 0, errorMoreData
	}
	if _, err := queryJobProcessIDs(syscall.Handle(42), query); err == nil {
		t.Fatal("accepted a Job Object process list above the allocation bound")
	}
	if calls != 1 {
		t.Fatalf("query calls = %d, want immediate rejection", calls)
	}
}

func putTestProcessID(buffer []byte, index int, value uintptr) {
	offset := 8 + index*int(unsafe.Sizeof(uintptr(0)))
	if unsafe.Sizeof(uintptr(0)) == 8 {
		binary.LittleEndian.PutUint64(buffer[offset:offset+8], uint64(value))
		return
	}
	binary.LittleEndian.PutUint32(buffer[offset:offset+4], uint32(value))
}
