package drive

import (
	"fmt"
	"regexp"
	"strings"
)

var folderColorAliases = map[string]string{
	"red":    "#db4437",
	"orange": "#f09300",
	"yellow": "#f6bf26",
	"green":  "#0f9d58",
	"teal":   "#0097a7",
	"blue":   "#4285f4",
	"purple": "#9334e6",
	"pink":   "#d81b60",
	"gray":   "#9aa0a6",
	"grey":   "#9aa0a6",
}

var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ResolveFolderColor maps a color name (red/blue/...) or hex color to #RRGGBB.
func ResolveFolderColor(raw string) (hex string, label string, err error) {
	color := strings.TrimSpace(raw)
	if color == "" {
		return "", "", fmt.Errorf("empty color")
	}

	key := strings.ToLower(color)
	if namedHex, ok := folderColorAliases[key]; ok {
		return namedHex, key, nil
	}
	if !hexColorPattern.MatchString(color) {
		return "", "", fmt.Errorf("unsupported color %q (use names: red/orange/yellow/green/teal/blue/purple/pink/gray or #RRGGBB)", raw)
	}
	return strings.ToLower(color), color, nil
}
