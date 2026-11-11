package server

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func trimSpace(s string) string { return strings.TrimSpace(s) }

// يفك اسم الموظف ويرجع نوع الترميز: ascii أو utf8 أو cp1256
func decodeName(field []byte) (string, string) {
	b := cstrBytes(field)
	ascii := true
	for _, x := range b {
		if x >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return trimSpace(string(b)), "ascii"
	}
	if utf8.Valid(b) {
		return trimSpace(string(b)), "utf8"
	}
	return trimSpace(decodeCP1256(b)), "cp1256"
}

func decodeCP1256(b []byte) string {
	var sb strings.Builder
	for _, x := range b {
		if x < 0x80 {
			sb.WriteByte(x)
		} else {
			sb.WriteRune(cp1256High[x-0x80])
		}
	}
	return sb.String()
}

var cp1256Rev map[rune]byte

func encodeName(text, enc string) ([]byte, error) {
	if enc != "cp1256" {
		return []byte(text), nil
	}
	if cp1256Rev == nil {
		cp1256Rev = map[rune]byte{}
		for i, r := range cp1256High {
			if r != 0xFFFD {
				cp1256Rev[r] = byte(0x80 + i)
			}
		}
	}
	out := make([]byte, 0, len(text))
	for _, r := range text {
		if r < 0x80 {
			out = append(out, byte(r))
			continue
		}
		b, ok := cp1256Rev[r]
		if !ok {
			return nil, fmt.Errorf("الحرف \"%c\" غير مدعوم بترميز Windows-1256", r)
		}
		out = append(out, b)
	}
	return out, nil
}
