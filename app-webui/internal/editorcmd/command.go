// Package editorcmd parses a text-editor command template into executable
// arguments. File paths are substituted after parsing and never evaluated by a
// shell, so spaces and metacharacters in a filename remain literal data.
package editorcmd

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const FilePath = "${file_path}"

// Parse accepts whitespace-separated arguments with single/double quotes.
// Backslashes are literal except when escaping a quote, or unquoted whitespace;
// this preserves Windows paths as well as supporting quoted executable names.
// An empty template selects the platform's default text application.
func Parse(template string) ([]string, error) {
	if !utf8.ValidString(template) || strings.ContainsAny(template, "\x00\r\n") {
		return nil, errors.New("text_editor_command must be one valid line without NUL characters")
	}
	var arguments []string
	var word strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			arguments = append(arguments, word.String())
			word.Reset()
			started = false
		}
	}
	runes := []rune(template)
	for i := 0; i < len(runes); i++ {
		character := runes[i]
		if quote == '\'' {
			if character == quote {
				quote = 0
			} else {
				word.WriteRune(character)
			}
			continue
		}
		if character == '\\' && i+1 < len(runes) {
			next := runes[i+1]
			if next == '"' || (quote == 0 && (next == '\'' || unicode.IsSpace(next))) {
				word.WriteRune(next)
				started = true
				i++
				continue
			}
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				word.WriteRune(character)
			}
			continue
		}
		switch {
		case character == '\'' || character == '"':
			quote = character
			started = true
		case unicode.IsSpace(character):
			flush()
		case strings.ContainsRune("|&;<>", character):
			return nil, errors.New("text_editor_command must name one executable; quote literal | & ; < > characters in arguments")
		default:
			word.WriteRune(character)
			started = true
		}
	}
	if quote != 0 {
		return nil, errors.New("text_editor_command contains an unclosed quote")
	}
	flush()
	if len(arguments) == 0 {
		return nil, nil
	}
	if arguments[0] == "" || strings.Contains(arguments[0], "${") {
		return nil, errors.New("text_editor_command must start with an executable name or path, without placeholders")
	}
	found := false
	for _, argument := range arguments[1:] {
		found = found || strings.Contains(argument, FilePath)
		if strings.Contains(strings.ReplaceAll(argument, FilePath, ""), "${") {
			return nil, errors.New("text_editor_command only supports the ${file_path} placeholder")
		}
	}
	if !found {
		return nil, errors.New("text_editor_command must include ${file_path} in an argument")
	}
	return arguments, nil
}

// Expand preserves the argument boundaries established by Parse, even when
// the placeholder is embedded in an option such as --file=${file_path}.
func Expand(template, path string) ([]string, error) {
	arguments, err := Parse(template)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(arguments); i++ {
		arguments[i] = strings.ReplaceAll(arguments[i], FilePath, path)
	}
	return arguments, nil
}
