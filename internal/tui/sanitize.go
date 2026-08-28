package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxAPITextBytes = 16 << 10

// sanitizeAPIText strips terminal escape sequences and non-printing controls
// from controller-provided text before it is rendered in the operator terminal.
func sanitizeAPIText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) > maxAPITextBytes {
		value = value[:maxAPITextBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value += "…"
	}
	var out strings.Builder
	for i := 0; i < len(value); {
		c := value[i]
		if c == 0x1b {
			i++
			if i >= len(value) {
				continue
			}
			switch value[i] {
			case '[':
				i++
				for i < len(value) {
					b := value[i]
					i++
					if b >= 0x40 && b <= 0x7e {
						break
					}
				}
			case ']', 'P', '^', '_':
				i++
				for i < len(value) {
					if value[i] == 0x07 {
						i++
						break
					}
					if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default:
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == '\u009b' {
			i += size
			for i < len(value) {
				b := value[i]
				i++
				if b >= 0x40 && b <= 0x7e {
					break
				}
			}
			continue
		}
		if unicode.IsControl(r) {
			switch c {
			case '\n', '\t':
				out.WriteByte(c)
			case '\r':
				if i+1 >= len(value) || value[i+1] != '\n' {
					out.WriteByte('\n')
				}
			}
			i++
			continue
		}
		out.WriteString(value[i : i+size])
		i += size
	}
	return out.String()
}
