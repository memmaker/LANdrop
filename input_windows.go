// Keyboard input on Windows via SendInput (user32). Pure syscall, no cgo; works
// on Windows 7+. The INPUT layout here is amd64-only, matching the build targets.
package main

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32        = windows.NewLazySystemDLL("user32.dll")
	procSendInput = user32.NewProc("SendInput")
	procVkKeyScan = user32.NewProc("VkKeyScanW")
)

const (
	inputKeyboard    = 1
	keyeventfKeyup   = 0x0002
	keyeventfUnicode = 0x0004
)

// input mirrors the Win32 INPUT struct for the keyboard case on amd64 (40 bytes):
// Type + padding, then a 32-byte union large enough for KEYBDINPUT/MOUSEINPUT.
type input struct {
	Type uint32
	_    uint32
	Data [32]byte
}

func kbInput(vk, scan uint16, flags uint32) input {
	var in input
	in.Type = inputKeyboard
	p := unsafe.Pointer(&in.Data[0])
	*(*uint16)(p) = vk                                 // wVk
	*(*uint16)(unsafe.Pointer(uintptr(p) + 2)) = scan  // wScan
	*(*uint32)(unsafe.Pointer(uintptr(p) + 4)) = flags // dwFlags
	return in
}

func sendAll(in []input) error {
	if len(in) == 0 {
		return nil
	}
	n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(n) != len(in) {
		return fmt.Errorf("SendInput sent %d of %d events: %v", n, len(in), err)
	}
	return nil
}

var winVK = map[string]uint16{
	"enter": 0x0D, "return": 0x0D, "tab": 0x09, "space": 0x20,
	"backspace": 0x08, "delete": 0x2E, "escape": 0x1B, "esc": 0x1B,
	"left": 0x25, "up": 0x26, "right": 0x27, "down": 0x28,
	"home": 0x24, "end": 0x23, "pageup": 0x21, "pagedown": 0x22,
}

var winMods = map[string]uint16{
	"ctrl": 0x11, "control": 0x11, "alt": 0x12, "option": 0x12,
	"shift": 0x10, "cmd": 0x5B, "command": 0x5B, "win": 0x5B,
}

func sendInput(a inputArgs) error {
	if a.Text == "" && a.Key == "" {
		return fmt.Errorf("nothing to send")
	}
	if a.Text != "" {
		var ev []input
		for _, r := range a.Text { // type each rune as a Unicode event (BMP)
			ev = append(ev,
				kbInput(0, uint16(r), keyeventfUnicode),
				kbInput(0, uint16(r), keyeventfUnicode|keyeventfKeyup))
		}
		if err := sendAll(ev); err != nil {
			return err
		}
	}
	if a.Key != "" {
		return pressChord(a.Key)
	}
	return nil
}

func pressChord(chord string) error {
	parts := strings.Split(chord, "+")
	key := strings.TrimSpace(parts[len(parts)-1])
	var mods []uint16
	for _, m := range parts[:len(parts)-1] {
		vk, ok := winMods[strings.ToLower(strings.TrimSpace(m))]
		if !ok {
			return fmt.Errorf("unknown modifier %q", m)
		}
		mods = append(mods, vk)
	}
	vk, ok := winVK[strings.ToLower(key)]
	if !ok {
		if len([]rune(key)) != 1 {
			return fmt.Errorf("unknown key %q", key)
		}
		r, _, _ := procVkKeyScan.Call(uintptr(uint16([]rune(key)[0])))
		vk = uint16(r) & 0xFF // low byte is the virtual-key code
	}
	var ev []input
	for _, m := range mods {
		ev = append(ev, kbInput(m, 0, 0))
	}
	ev = append(ev, kbInput(vk, 0, 0), kbInput(vk, 0, keyeventfKeyup))
	for i := len(mods) - 1; i >= 0; i-- {
		ev = append(ev, kbInput(mods[i], 0, keyeventfKeyup))
	}
	return sendAll(ev)
}
