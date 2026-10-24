package notify

import (
	"encoding/json"
	"mime"
)

func mimeEncode(s string) string { return mime.QEncoding.Encode("utf-8", s) }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
