package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type query struct {
	Version int
	Class   string
	Member  string
}

func parseQuery(input string) (query, error) {
	var q query
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 1024 {
		return q, fmt.Errorf("enter a class or class#member, optionally preceded or followed by a JDK version")
	}
	fields := strings.Fields(input)
	// Only standalone edge tokens are versions; parameter types may contain spaces.
	for _, first := range []bool{true, false} {
		if len(fields) < 2 {
			break
		}
		i := len(fields) - 1
		if first {
			i = 0
		}
		token := fields[i]
		if strings.IndexFunc(token, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
			continue
		}
		v, err := strconv.Atoi(token)
		if err != nil || v < 9 {
			return q, fmt.Errorf("JDK versions must be integers starting at 9")
		}
		if q.Version != 0 {
			return q, fmt.Errorf("specify a JDK version only once")
		}
		q.Version = v
		fields = append(fields[:i:i], fields[i+1:]...)
	}
	text := strings.Join(fields, " ")
	class, method, hasMember := strings.Cut(text, "#")
	if !validName(class) {
		return q, fmt.Errorf("invalid class name; use a simple name or a fully qualified name")
	}
	if hasMember {
		if method == "" || strings.Contains(method, "#") {
			return q, fmt.Errorf("enter a member name after #")
		}
		name, params, signature := strings.Cut(method, "(")
		if !validName(name) || strings.Contains(name, ".") {
			return q, fmt.Errorf("invalid member name")
		}
		if signature {
			if !strings.HasSuffix(params, ")") || strings.ContainsAny(strings.TrimSuffix(params, ")"), "()#;\r\n") {
				return q, fmt.Errorf("invalid member signature")
			}
			if params = strings.TrimSpace(strings.TrimSuffix(params, ")")); params != "" {
				for _, param := range strings.Split(params, ",") {
					if strings.TrimSpace(param) == "" {
						return q, fmt.Errorf("member signatures cannot contain empty parameters")
					}
				}
			}
		} else if strings.ContainsAny(method, " )\t") {
			return q, fmt.Errorf("invalid member name")
		}
	}
	q.Class = strings.ToLower(class)
	q.Member = normalizeSignature(method)
	return q, nil
}

func validName(s string) bool {
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for i, r := range part {
			if !(unicode.IsLetter(r) || r == '_' || r == '$' || (i > 0 && unicode.IsDigit(r))) {
				return false
			}
		}
	}
	return true
}

func normalizeSignature(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s))
}

type result struct {
	Label string
	URL   string
}

func (idx *versionIndex) search(q query) []result {
	var results []result
	if len(idx.Types) == 0 {
		return results
	}
	// Keep both table directions stable for the lookup and result construction.
	table := idx.Strings
	table.mu.RLock()
	defer table.mu.RUnlock()
	classID, ok := table.ids[q.Class]
	if !ok {
		return results
	}
	name, _, signature := strings.Cut(q.Member, "(")
	memberID := table.ids[name]
	seen := make(map[string]bool)
	add := func(label, address string) {
		if !seen[address] {
			results = append(results, result{Label: label, URL: address})
			seen[address] = true
		}
	}
	for _, t := range idx.Types[classID] {
		if q.Member == "" {
			add(table.values[t.FQN], idx.Root+table.values[t.Path])
			continue
		}
		members := t.Members[memberID]
		if len(members) == 0 && !signature {
			// Expand prefixes only when this class has no exact member-name match.
			members = nil
			for id, overloads := range t.Members {
				if strings.HasPrefix(table.values[id], name) {
					members = append(members, overloads...)
				}
			}
		}
		for _, m := range members {
			label := table.values[m.Signature]
			if signature && q.Member != normalizeSignature(label) {
				continue
			}
			add(table.values[t.FQN]+"#"+label, idx.Root+table.values[t.Path]+"#"+table.values[m.Anchor])
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Label < results[j].Label })
	return results
}
