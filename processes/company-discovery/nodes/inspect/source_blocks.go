package inspect

import (
	"fmt"
	"strings"
)

func ResolveSourceBlocks(r Request, definitions []SourceBlockDefinition) (Request, error) {
	seen := map[string]bool{}
	for _, source := range r.Sources {
		if source.Block != nil || source.BlockKey != "" {
			return r, fmt.Errorf("discovery: source Block configuration is Process-owned; select its exact Block reference")
		}
		if seen[source.ID] {
			return r, fmt.Errorf("discovery: duplicate source ID")
		}
		seen[source.ID] = true
	}
	for _, selection := range r.SourceBlocks {
		found := false
		for _, definition := range definitions {
			if selection.Block != definition.Block {
				continue
			}
			found = true
			source := definition.Source
			if seen[source.ID] {
				return r, fmt.Errorf("discovery: duplicate source Block selection")
			}
			seen[source.ID] = true
			if selection.Pages < 0 {
				return r, fmt.Errorf("discovery: source Block pages must be nonnegative")
			}
			if selection.Pages > 0 && (source.Pagination != nil || strings.Contains(source.URLTemplate, "{page}") || strings.Contains(source.URLTemplate, "{offset}")) {
				source.Pages = selection.Pages
			}
			if len(selection.Queries) > 0 {
				source.Queries = selection.Queries
			}
			source.SourceDigest = SourceRevision(source)
			ref := definition.Block
			source.Block = &ref
			source.BlockKey = definition.Key
			r.Sources = append(r.Sources, source)
		}
		if !found {
			return r, fmt.Errorf("discovery: unknown or stale source Block %s@%s", selection.Block.Name, selection.Block.Digest)
		}
	}
	return r, nil
}
func BindSourceBlock(s State, bound Source) (State, error) {
	if s.CurrentAction < 0 || s.CurrentAction >= len(s.Actions) {
		return s, fmt.Errorf("discovery: source Block requires a selected action")
	}
	action := s.Actions[s.CurrentAction]
	for _, source := range s.Request.Sources {
		if source.ID != action.SourceID {
			continue
		}
		if source.ID != bound.ID || source.Block == nil || source.BlockKey != bound.BlockKey || SourceRevision(source) != SourceRevision(bound) {
			return s, fmt.Errorf("discovery: source definition differs from pinned Block configuration")
		}
		return s, nil
	}
	return s, fmt.Errorf("discovery: source Block configuration absent")
}
