package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Appearance is the UI preference stored in settings.appearance
// (PLAN.md section 7.1).
type Appearance struct {
	Estilo string `json:"estilo"` // grafito | terminal | medianoche | monocromo | enfoque
	Tema   string `json:"tema"`   // sistema | oscuro | claro
}

// Default appearance values.
const (
	DefaultEstilo = "grafito"
	TemaSistema   = "sistema"
	TemaOscuro    = "oscuro"
	TemaClaro     = "claro"
)

// backgrounds is --bg of every style: [dark, light] (PLAN.md 7.1 tokens,
// same values as web/src/theme/*.ts).
var backgrounds = map[string][2]string{
	"grafito":    {"#0e0f11", "#f6f7f8"},
	"terminal":   {"#0a0a09", "#f4f1e9"},
	"medianoche": {"#0b0d17", "#f4f5fb"},
	"monocromo":  {"#000000", "#ffffff"},
	"enfoque":    {"#131211", "#faf8f5"},
}

// RGB is an opaque color.
type RGB struct{ R, G, B uint8 }

// Dark resolves the color scheme: tema oscuro/claro wins, "sistema" (or
// anything unknown) follows systemDark.
func (a Appearance) Dark(systemDark bool) bool {
	switch a.Tema {
	case TemaOscuro:
		return true
	case TemaClaro:
		return false
	default:
		return systemDark
	}
}

// Background returns --bg for the appearance. Unknown styles fall back to
// Grafito.
func (a Appearance) Background(systemDark bool) RGB {
	return BackgroundFor(a.Estilo, a.Dark(systemDark))
}

// BackgroundFor returns --bg of estilo in the dark or light scheme.
func BackgroundFor(estilo string, dark bool) RGB {
	pair, ok := backgrounds[estilo]
	if !ok {
		pair = backgrounds[DefaultEstilo]
	}
	hex := pair[1]
	if dark {
		hex = pair[0]
	}
	c, _ := ParseHexColor(hex)
	return c
}

// ParseHexColor parses "#rrggbb".
func ParseHexColor(s string) (RGB, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return RGB{}, fmt.Errorf("color %q: want #rrggbb", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("color %q: %w", s, err)
	}
	return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil //nolint:gosec // G115: v has 24 significant bits; each byte is extracted on purpose
}

// AppearanceFromSettings extracts settings.appearance from the body of
// GET /api/v1/settings. Missing fields keep the defaults.
func AppearanceFromSettings(body []byte) (Appearance, error) {
	var s struct {
		Appearance *Appearance `json:"appearance"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return Appearance{}, fmt.Errorf("settings: %w", err)
	}
	a := Appearance{Estilo: DefaultEstilo, Tema: TemaSistema}
	if s.Appearance != nil {
		if s.Appearance.Estilo != "" {
			a.Estilo = s.Appearance.Estilo
		}
		if s.Appearance.Tema != "" {
			a.Tema = s.Appearance.Tema
		}
	}
	return a, nil
}

// State is what the desktop app remembers between runs outside config.toml
// (DataDir/desktop-state.json): the last appearance, so the window opens
// with the right background before the core answers.
type State struct {
	Appearance Appearance `json:"appearance"`
}

// StateFileName lives in config.DataDir.
const StateFileName = "desktop-state.json"

// LoadState reads dataDir/desktop-state.json; a missing or broken file
// yields the defaults.
func LoadState(dataDir string) State {
	st := State{Appearance: Appearance{Estilo: DefaultEstilo, Tema: TemaSistema}}
	b, err := os.ReadFile(filepath.Join(dataDir, StateFileName))
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// SaveState writes dataDir/desktop-state.json atomically.
func SaveState(dataDir string, st State) error {
	if dataDir == "" {
		return errors.New("desktop: empty data dir")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("desktop: state: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, StateFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("desktop: state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("desktop: state: %w", err)
	}
	return nil
}
