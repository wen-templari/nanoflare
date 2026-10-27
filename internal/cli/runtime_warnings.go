package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type runtimeToken struct {
	text string
	line int
}

// Advisory checks intentionally recognize only direct global API calls and
// literal fetch-init objects. They are not a proof of runtime compatibility.
func warnPrivateRuntime(files []nanoflare.WorkerFile, out io.Writer) {
	for _, file := range files {
		if file.Wasm != nil {
			continue
		}
		tokens := runtimeTokens(file.Content)
		shadowed := map[string]bool{}
		for i := 0; i+1 < len(tokens); i++ {
			if tokens[i].text == "const" || tokens[i].text == "let" || tokens[i].text == "var" || tokens[i].text == "function" {
				shadowed[tokens[i+1].text] = true
			}
		}
		seen := map[string]bool{}
		warn := func(token runtimeToken, kind, message string) {
			if !seen[kind] {
				fmt.Fprintf(out, "warning: %s:%d: %s\n", file.Path, token.line, message)
				seen[kind] = true
			}
		}
		for i, token := range tokens {
			global := i == 0 || tokens[i-1].text != "." || i >= 2 && tokens[i-2].text == "globalThis"
			if token.text == "caches" && global && !shadowed["caches"] && i+2 < len(tokens) && tokens[i+1].text == "." && (tokens[i+2].text == "default" || tokens[i+2].text == "open") {
				warn(token, "cache", "Cache API is unavailable in the private dynamic runtime; response Cache-Control headers do not provide programmatic storage")
			}
			if token.text != "fetch" || !global || shadowed["fetch"] || i+1 >= len(tokens) || tokens[i+1].text != "(" {
				continue
			}
			depth, argument := 0, 0
			for j := i + 2; j < len(tokens); j++ {
				t := tokens[j].text
				if t == ")" && depth == 0 {
					break
				}
				if t == "," && depth == 0 {
					argument++
					continue
				}
				if argument == 1 && depth == 0 && t != "{" {
					break
				}
				if argument == 1 && depth == 1 && t == "cf" && j+1 < len(tokens) && tokens[j+1].text == ":" {
					warn(tokens[j], "cf", "fetch cf options are unavailable in the private dynamic runtime")
				}
				switch t {
				case "(", "{", "[":
					depth++
				case ")", "}", "]":
					depth--
				}
			}
		}
	}
}

func runtimeTokens(source string) []runtimeToken {
	var tokens []runtimeToken
	line := 1
	for i := 0; i < len(source); {
		c := source[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "//") {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			i += 2
			for i < len(source) && !strings.HasPrefix(source[i:], "*/") {
				if source[i] == '\n' {
					line++
				}
				i++
			}
			if i < len(source) {
				i += 2
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			i++
			for i < len(source) {
				if source[i] == '\\' {
					i += 2
					continue
				}
				if source[i] == '\n' {
					line++
				}
				if source[i] == quote {
					i++
					break
				}
				i++
			}
			tokens = append(tokens, runtimeToken{"<literal>", line})
			continue
		}
		// Skip regular-expression literals in common expression positions.
		if c == '/' && (len(tokens) == 0 || strings.Contains("=(:,![", tokens[len(tokens)-1].text) || tokens[len(tokens)-1].text == "return") {
			i++
			class := false
			for i < len(source) && source[i] != '\n' {
				if source[i] == '\\' {
					i += 2
					continue
				}
				if source[i] == '[' {
					class = true
				}
				if source[i] == ']' {
					class = false
				}
				if source[i] == '/' && !class {
					i++
					break
				}
				i++
			}
			tokens = append(tokens, runtimeToken{"<literal>", line})
			continue
		}
		start := i
		if c == '_' || c == '$' || unicode.IsLetter(rune(c)) {
			i++
			for i < len(source) && (source[i] == '_' || source[i] == '$' || unicode.IsLetter(rune(source[i])) || unicode.IsDigit(rune(source[i]))) {
				i++
			}
		} else {
			i++
		}
		tokens = append(tokens, runtimeToken{source[start:i], line})
	}
	return tokens
}
