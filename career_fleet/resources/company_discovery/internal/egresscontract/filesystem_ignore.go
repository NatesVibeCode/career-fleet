package egresscontract

import (
	"fmt"
	"path"
	"regexp"
	"regexp/syntax"
	"strings"
)

// Repository rules are confined to .gitignore files beneath the bound root.
// Global Git configuration and directories outside that root are never read.
type workspaceIgnoreRule struct {
	base, source      string
	line              int
	negate, directory bool
	pattern           *regexp.Regexp
}

func workspaceIgnoreRules(base, source, content string) []workspaceIgnoreRule {
	var rules []workspaceIgnoreRule
	for line, pattern := range strings.Split(content, "\n") {
		pattern = strings.TrimSuffix(pattern, "\r")
		for strings.HasSuffix(pattern, " ") {
			n := len(pattern) - 2
			for n >= 0 && pattern[n] == '\\' {
				n--
			}
			if (len(pattern)-2-n)%2 == 1 {
				break
			}
			pattern = strings.TrimSuffix(pattern, " ")
		}
		if pattern == "" || pattern[0] == '#' {
			continue
		}
		rule := workspaceIgnoreRule{base: base, source: source, line: line + 1}
		if pattern[0] == '!' {
			rule.negate = true
			pattern = pattern[1:]
		}
		if pattern == "" {
			continue
		}
		rule.directory = strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		anchored := strings.Contains(pattern, "/")
		pattern = strings.TrimPrefix(pattern, "/")
		expression, ok := workspaceIgnoreExpression(pattern)
		if !ok {
			continue
		} // Like Git, malformed patterns never match.
		prefix := "^"
		if !anchored {
			prefix += "(?:.*/)?"
		}
		rule.pattern, _ = regexp.Compile(prefix + expression + "$")
		if rule.pattern != nil {
			rules = append(rules, rule)
		}
	}
	return rules
}

func workspaceIgnoreExpression(pattern string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
			if i == len(pattern) {
				return "", false
			}
			out.WriteString(regexp.QuoteMeta(string(rune(pattern[i]))))
		case '*':
			end := i
			for end+1 < len(pattern) && pattern[end+1] == '*' {
				end++
			}
			if end-i == 1 && (i == 0 || pattern[i-1] == '/') && (end+1 == len(pattern) || pattern[end+1] == '/') {
				if end+1 < len(pattern) {
					out.WriteString("(?:[^/]+/)*")
					end++
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
			i = end
		case '?':
			out.WriteString("[^/]")
		case '[':
			end := i + 1
			if end < len(pattern) && (pattern[end] == '!' || pattern[end] == '^') {
				end++
			}
			if end < len(pattern) && pattern[end] == ']' {
				end++
			}
			for end < len(pattern) && pattern[end] != ']' {
				if pattern[end] == '[' && end+1 < len(pattern) && pattern[end+1] == ':' {
					close := strings.Index(pattern[end+2:], ":]")
					if close < 0 {
						return "", false
					}
					end += close + 4
					continue
				}
				if pattern[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(pattern) {
				return "", false
			}
			class := pattern[i+1 : end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			if strings.HasPrefix(class, "]") {
				class = "\\]" + class[1:]
			}
			if strings.HasPrefix(class, "^]") {
				class = "^\\]" + class[2:]
			}
			parsed, err := syntax.Parse("["+workspaceIgnoreBytes(class)+"]", syntax.Perl)
			if err != nil {
				return "", false
			}
			if parsed.Op == syntax.OpLiteral {
				if strings.ContainsRune(string(parsed.Rune), '/') {
					return "", false
				}
				out.WriteString(regexp.QuoteMeta(string(parsed.Rune)))
			} else if parsed.Op == syntax.OpCharClass {
				// Git's wildcard classes never consume a path separator, even
				// when a negated class or character range contains '/'.
				var ranges strings.Builder
				add := func(lo, hi rune) {
					if lo <= hi {
						fmt.Fprintf(&ranges, "\\x{%X}-\\x{%X}", lo, hi)
					}
				}
				for j := 0; j < len(parsed.Rune); j += 2 {
					lo, hi := parsed.Rune[j], parsed.Rune[j+1]
					if lo <= '/' && hi >= '/' {
						add(lo, '/'-1)
						add('/'+1, hi)
					} else {
						add(lo, hi)
					}
				}
				if ranges.Len() == 0 {
					return "", false
				}
				out.WriteString("[" + ranges.String() + "]")
			} else {
				return "", false
			}
			i = end
		default:
			out.WriteString(regexp.QuoteMeta(string(rune(pattern[i]))))
		}
	}
	return out.String(), true
}

func workspaceIgnored(rules []workspaceIgnoreRule, name string, directory bool) (bool, workspaceIgnoreRule) {
	ignored := false
	var matching workspaceIgnoreRule
	for _, rule := range rules {
		rel := name
		if rule.base != "." {
			if !strings.HasPrefix(name, rule.base+"/") {
				continue
			}
			rel = strings.TrimPrefix(name, rule.base+"/")
		}
		if rule.directory && !directory {
			continue
		}
		if rule.pattern.MatchString(workspaceIgnoreBytes(rel)) {
			ignored = !rule.negate
			matching = rule
		}
	}
	return ignored, matching
}

// Git wildmatch operates on pathname bytes. Map bytes to individual regexp
// runes so '?' and bracket classes retain that behavior for UTF-8 names.
func workspaceIgnoreBytes(value string) string {
	ascii := true
	for i := 0; i < len(value); i++ {
		if value[i] >= 128 {
			ascii = false
			break
		}
	}
	if ascii {
		return value
	}
	runes := make([]rune, len(value))
	for i := 0; i < len(value); i++ {
		runes[i] = rune(value[i])
	}
	return string(runes)
}

func workspaceAncestors(name string) []string {
	var reversed []string
	for name != "." {
		reversed = append(reversed, name)
		name = path.Dir(name)
	}
	out := []string{"."}
	for i := len(reversed) - 1; i >= 0; i-- {
		out = append(out, reversed[i])
	}
	return out
}
