package runner

import (
	"errors"
	"strings"
)

// ErrUnterminatedQuote is returned when the command line ends inside a quote.
var ErrUnterminatedQuote = errors.New("unterminated quote in command")

// splitCommand tokenizes a command line the way bash does for the subset the
// panel generates: whitespace separates words, single quotes are literal,
// double quotes honour backslash escapes, and adjacent fragments join into one
// word (`'a'\”b'` is a single word `a'b`).
//
// The panel never evaluates a command as shell code, so this is the only place
// a command string is turned into arguments.
func splitCommand(input string) ([]string, error) {
	var (
		words   []string
		current strings.Builder
		started bool
	)

	flush := func() {
		if started {
			words = append(words, current.String())
			current.Reset()
			started = false
		}
	}

	for i := 0; i < len(input); i++ {
		switch ch := input[i]; ch {
		case ' ', '\t', '\n', '\r':
			flush()
		case '\'':
			started = true
			end := strings.IndexByte(input[i+1:], '\'')
			if end < 0 {
				return nil, ErrUnterminatedQuote
			}
			current.WriteString(input[i+1 : i+1+end])
			i += end + 1
		case '"':
			started = true
			i++
			for ; i < len(input); i++ {
				if input[i] == '"' {
					break
				}
				if input[i] == '\\' && i+1 < len(input) {
					next := input[i+1]
					// Inside double quotes bash only treats these as escapes.
					if next == '"' || next == '\\' || next == '$' || next == '`' || next == '\n' {
						current.WriteByte(next)
						i++
						continue
					}
				}
				current.WriteByte(input[i])
			}
			if i >= len(input) {
				return nil, ErrUnterminatedQuote
			}
		case '\\':
			started = true
			if i+1 >= len(input) {
				current.WriteByte('\\')
				continue
			}
			i++
			current.WriteByte(input[i])
		default:
			started = true
			current.WriteByte(ch)
		}
	}
	flush()
	return words, nil
}

// isAssignment reports whether word is a leading `NAME=value` environment
// assignment, the form crontab.list uses for real_time/no_tee/ID/....
func isAssignment(word string) (string, string, bool) {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return "", "", false
	}
	name := word[:eq]
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z'):
		case i > 0 && ch >= '0' && ch <= '9':
		default:
			return "", "", false
		}
	}
	return name, word[eq+1:], true
}
