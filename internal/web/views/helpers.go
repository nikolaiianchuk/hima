package views

import (
	"regexp"
	"strconv"
)

func itoa(i int) string     { return strconv.Itoa(i) }
func itoa64(i int64) string { return strconv.FormatInt(i, 10) }

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func isHexColor(s string) bool { return hexColor.MatchString(s) }

// boolAttr renders aria-current="true" for the active choice, nothing otherwise.
func boolAttr(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

func dateAttr(on bool) string {
	if on {
		return "date"
	}
	return "false"
}
