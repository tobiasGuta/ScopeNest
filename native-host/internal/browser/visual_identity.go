package browser

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/scopenest/scopenest/native-host/internal/security"
)

const maxWindowLabelRunes = 120

type rgbColor struct {
	red   uint8
	green uint8
	blue  uint8
}

func validateVisualIdentity(identity VisualIdentity) error {
	if identity == (VisualIdentity{}) {
		return nil
	}
	if err := security.ValidateName(identity.Name); err != nil {
		return err
	}
	if err := security.ValidateColor(identity.Color); err != nil {
		return err
	}
	if err := security.ValidateIcon(identity.Icon); err != nil {
		return err
	}
	return nil
}

// WindowLabel returns a bounded, single-line Chromium window name. It also
// normalizes defensively in case an identity came from a damaged local store;
// authoritative container mutations still use the shared security validators.
func WindowLabel(identity VisualIdentity) string {
	name := normalizeLabelPart(identity.Name)
	icon := normalizeLabelPart(identity.Icon)

	label := "ScopeNest"
	if icon != "" {
		label = "[" + icon + "] " + label
	}
	if name != "" {
		label += " — " + name
	}
	return truncateRunes(label, maxWindowLabelRunes)
}

func normalizeLabelPart(value string) string {
	var result strings.Builder
	pendingSpace := false
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.IsSpace(r) {
			pendingSpace = result.Len() > 0
			continue
		}
		if pendingSpace {
			result.WriteByte(' ')
			pendingSpace = false
		}
		result.WriteRune(r)
	}
	return result.String()
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func parseRGB(value string) (rgbColor, error) {
	if err := security.ValidateColor(value); err != nil {
		return rgbColor{}, err
	}
	parsed, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return rgbColor{}, errors.New("invalid identity color")
	}
	return rgbColor{
		red:   uint8(parsed >> 16),
		green: uint8(parsed >> 8),
		blue:  uint8(parsed),
	}, nil
}

func relativeLuminance(color rgbColor) float64 {
	linear := func(component uint8) float64 {
		value := float64(component) / 255
		if value <= 0.04045 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(color.red) + 0.7152*linear(color.green) + 0.0722*linear(color.blue)
}

func contrastingTextColor(color rgbColor) rgbColor {
	// At this luminance, black and white have equal WCAG contrast ratios.
	if relativeLuminance(color) > 0.1791287847 {
		return rgbColor{}
	}
	return rgbColor{red: 0xff, green: 0xff, blue: 0xff}
}
