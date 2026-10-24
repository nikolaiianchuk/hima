package notify

import "mime"

func mimeEncode(s string) string { return mime.QEncoding.Encode("utf-8", s) }
