package discovery

import (
	"fmt"
	"regexp"
	"strings"
)

type Token struct {
	Text  string
	Start int
	End   int
}

// Tokens retains original UTF-8 byte locations. Case folding is used only for
// comparison; source text is never rewritten to manufacture an evidence span.
func Tokens(text string) []Token {
	result := []Token{}
	word := regexp.MustCompile(`^[\p{L}\p{N}\p{M}]$`)
	start := -1
	flush := func(end int) {
		if start >= 0 {
			result = append(result, Token{strings.ToLower(text[start:end]), start, end})
			start = -1
		}
	}
	for i, r := range text {
		if word.MatchString(string(r)) || (start >= 0 && (r == '+' || r == '#')) {
			if start < 0 {
				start = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(text))
	return result
}

// Query concepts are retrieval hints, not requirements or a personal profile.
// The language-neutral path preserves Unicode terms; this small grammatical
// English stop set removes task scaffolding, not industries or user traits.
func QueryConcepts(objective string) []Concept {
	stop := map[string]bool{"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "to": true, "for": true, "with": true, "that": true, "which": true, "find": true, "show": true, "me": true, "please": true, "company": true, "companies": true}
	seen := map[string]bool{}
	out := []Concept{}
	for _, t := range Tokens(objective) {
		if !stop[t.Text] && !seen[t.Text] {
			seen[t.Text] = true
			out = append(out, Concept{ID: "query:" + t.Text, Terms: []string{t.Text}, Relations: []Relation{}})
		}
	}
	return out
}
func ValidateConcepts(values []Concept) error {
	seen := map[string]bool{}
	for _, c := range values {
		if c.ID == "" || seen[c.ID] || len(c.Terms) == 0 {
			return fmt.Errorf("discovery: concept needs unique id and expressions")
		}
		seen[c.ID] = true
		for _, term := range c.Terms {
			if len(Tokens(term)) == 0 {
				return fmt.Errorf("discovery: empty concept expression")
			}
		}
		for _, r := range c.Relations {
			if len(Tokens(r.Term)) == 0 || r.Provenance == "" {
				return fmt.Errorf("discovery: relation needs expression and provenance")
			}
			switch r.Kind {
			case "equivalent", "broader", "narrower", "associated":
			default:
				return fmt.Errorf("discovery: unsupported concept relation %q", r.Kind)
			}
		}
	}
	return nil
}
func FindMatches(concepts []Concept, observation Observation) []Match {
	out := []Match{}
	fields := []struct{ name, text string }{{"title", observation.Title}, {observation.Field, observation.Text}}
	for _, field := range fields {
		if field.name == "" {
			field.name = "snippet"
		}
		tokens := Tokens(field.text)
		for _, concept := range concepts {
			terms := []Relation{}
			for _, t := range concept.Terms {
				terms = append(terms, Relation{Term: t, Kind: "direct", Provenance: "request"})
			}
			terms = append(terms, concept.Relations...)
			for _, term := range terms {
				pattern := Tokens(term.Term)
				if len(pattern) == 0 {
					continue
				}
				for i := 0; i+len(pattern) <= len(tokens); i++ {
					matches := true
					for j := range pattern {
						if tokens[i+j].Text != pattern[j].Text {
							matches = false
							break
						}
					}
					if !matches {
						continue
					}
					start, end := tokens[i].Start, tokens[i+len(pattern)-1].End
					negated := false
					if i > 0 {
						switch tokens[i-1].Text {
						case "not", "no", "never", "without":
							negated = true
						}
					}
					out = append(out, Match{ConceptID: concept.ID, Term: term.Term, Relation: term.Kind, Provenance: term.Provenance, ObservationID: observation.ID, Field: field.name, StartByte: start, EndByte: end, Quote: field.text[start:end], Negated: negated})
				}
			}
		}
	}
	return out
}
func Relevant(matches []Match) bool {
	for _, m := range matches {
		if !m.Negated {
			return true
		}
	}
	return false
}

// A local adjudication never changes vocabulary or another observation.
func RelevantWithFeedback(matches []Match, feedback []Feedback) bool {
	for _, m := range matches {
		suppressed := false
		for _, f := range feedback {
			if f.Kind == "retrieval" && f.ObservationID == m.ObservationID {
				suppressed = true
			}
		}
		if !suppressed && !m.Negated {
			return true
		}
	}
	return false
}
