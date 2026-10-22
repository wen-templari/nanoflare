package nanoflare

import (
	"errors"
	"strings"
)

var ErrSQLPolicy = errors.New("SQL operation is unavailable under the database isolation policy")

const maxSQLBytes = 100 << 10

type sqlPolicyToken struct {
	text    string
	quoted  bool
	literal bool
}

// InspectSQL validates the supported SQLite statement subset and returns its
// effect count. It is deliberately a lexical gate, not a general SQL parser.
// Ambiguous trigger/virtual-table programs are rejected rather than undercounted.
func InspectSQL(query string) (int, error) {
	if len(query) == 0 || len(query) > maxSQLBytes {
		return 0, ErrSQLPolicy
	}
	tokens, err := sqlPolicyTokens(query)
	if err != nil {
		return 0, err
	}
	statements := [][]sqlPolicyToken{}
	start := 0
	for i, t := range tokens {
		if t.text == ";" && !t.quoted && !t.literal {
			if i > start {
				statements = append(statements, tokens[start:i])
			}
			start = i + 1
		}
	}
	if start < len(tokens) {
		statements = append(statements, tokens[start:])
	}
	if len(statements) == 0 {
		return 0, ErrSQLPolicy
	}
	for _, statement := range statements {
		if err := validateSQLStatement(statement); err != nil {
			return 0, err
		}
	}
	return len(statements), nil
}
func validateSQLStatement(tokens []sqlPolicyToken) error {
	keyword := func(t sqlPolicyToken) string {
		if t.quoted || t.literal {
			return ""
		}
		return strings.ToUpper(t.text)
	}
	first := keyword(tokens[0])
	command := 0
	if first == "EXPLAIN" {
		command++
		if command < len(tokens) && keyword(tokens[command]) == "QUERY" {
			command += 2
		}
		if command >= len(tokens) {
			return ErrSQLPolicy
		}
		first = keyword(tokens[command])
	}
	switch first {
	case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "ALTER", "DROP":
	case "PRAGMA":
		index := command + 1
		if index+1 < len(tokens) && tokens[index+1].text == "." {
			if strings.ToLower(tokens[index].text) != "main" && strings.ToLower(tokens[index].text) != "temp" {
				return ErrSQLPolicy
			}
			index += 2
		}
		if index >= len(tokens) {
			return ErrSQLPolicy
		}
		if !allowedSQLPragma(strings.ToLower(tokens[index].text)) {
			return ErrSQLPolicy
		}
	default:
		return ErrSQLPolicy
	}
	for i, t := range tokens {
		word := strings.ToUpper(t.text)
		name := strings.ToLower(t.text)
		if !t.literal && strings.HasPrefix(name, "pragma_") && !allowedSQLPragma(strings.TrimPrefix(name, "pragma_")) {
			return ErrSQLPolicy
		}
		if !t.literal && !t.quoted && (word == "TRIGGER" || word == "VIRTUAL") && first == "CREATE" {
			return ErrSQLPolicy
		}
		if i+1 < len(tokens) && tokens[i+1].text == "(" {
			name := strings.ToLower(t.text)
			if strings.HasPrefix(name, "pragma_") && !allowedSQLPragma(strings.TrimPrefix(name, "pragma_")) {
				return ErrSQLPolicy
			}
			switch name {
			case "load_extension", "readfile", "writefile", "eval", "fts3_tokenizer", "zipfile", "fsdir":
				return ErrSQLPolicy
			}
		}
	}
	return nil
}
func sqlPolicyTokens(query string) ([]sqlPolicyToken, error) {
	out := []sqlPolicyToken{}
	for i := 0; i < len(query); {
		c := query[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v' {
			i++
			continue
		}
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			i += 2
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(query) && query[i+1] == '*' {
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return nil, ErrSQLPolicy
			}
			i += end + 4
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			var value strings.Builder
			closed := false
			for i < len(query) {
				if query[i] == end {
					if i+1 < len(query) && query[i+1] == end && c != '[' {
						value.WriteByte(end)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(query[i])
				i++
			}
			if !closed {
				return nil, ErrSQLPolicy
			}
			out = append(out, sqlPolicyToken{text: value.String(), quoted: c != '\'', literal: c == '\''})
			continue
		}
		if sqlPolicyWord(c) {
			start := i
			for i < len(query) && sqlPolicyWord(query[i]) {
				i++
			}
			out = append(out, sqlPolicyToken{text: query[start:i]})
			continue
		}
		out = append(out, sqlPolicyToken{text: string(c)})
		i++
	}
	return out, nil
}
func sqlPolicyWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c > 127
}

func allowedSQLPragma(name string) bool {
	switch name {
	case "table_info", "table_xinfo", "index_list", "index_info", "index_xinfo", "foreign_key_list", "foreign_keys", "defer_foreign_keys", "user_version":
		return true
	}
	return false
}
