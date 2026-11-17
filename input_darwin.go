// Keyboard input on macOS via System Events (osascript). Needs the Accessibility
// permission for LANdrop, or the events are silently dropped.
package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func sendInput(a inputArgs) error {
	if a.Text == "" && a.Key == "" {
		return fmt.Errorf("nothing to send")
	}
	if a.Text != "" {
		if err := osa("keystroke " + aqs(a.Text)); err != nil {
			return err
		}
	}
	if a.Key != "" {
		return osaKey(a.Key)
	}
	return nil
}

var macKeyCode = map[string]int{
	"enter": 36, "return": 36, "tab": 48, "space": 49,
	"backspace": 51, "delete": 51, "escape": 53, "esc": 53,
	"left": 123, "right": 124, "down": 125, "up": 126,
	"home": 115, "end": 119, "pageup": 116, "pagedown": 121,
}

var macMods = map[string]string{
	"cmd": "command down", "command": "command down",
	"ctrl": "control down", "control": "control down",
	"alt": "option down", "option": "option down",
	"shift": "shift down",
}

// osaKey presses one key or chord like "enter" or "cmd+shift+4".
func osaKey(chord string) error {
	parts := strings.Split(chord, "+")
	key := strings.TrimSpace(parts[len(parts)-1])
	var mods []string
	for _, m := range parts[:len(parts)-1] {
		v, ok := macMods[strings.ToLower(strings.TrimSpace(m))]
		if !ok {
			return fmt.Errorf("unknown modifier %q", m)
		}
		mods = append(mods, v)
	}
	using := ""
	if len(mods) > 0 {
		using = " using {" + strings.Join(mods, ", ") + "}"
	}
	if code, ok := macKeyCode[strings.ToLower(key)]; ok {
		return osa(fmt.Sprintf("key code %d%s", code, using))
	}
	if len([]rune(key)) != 1 {
		return fmt.Errorf("unknown key %q", key)
	}
	return osa("keystroke " + aqs(key) + using)
}

func osa(stmt string) error {
	out, err := exec.Command("osascript", "-e", `tell application "System Events" to `+stmt).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// aqs renders s as an AppleScript double-quoted string literal.
func aqs(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
