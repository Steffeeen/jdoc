package main

import (
	"strings"
	"sync"
)

type stringID uint32

// ID zero represents the empty string. Published IDs and their values never change.
type stringTable struct {
	mu     sync.RWMutex
	ids    map[string]stringID
	values []string
}

func (t *stringTable) intern(s string) stringID {
	if s == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if id, ok := t.ids[s]; ok {
		return id
	}
	if t.ids == nil {
		t.ids = make(map[string]stringID)
		t.values = []string{""}
	}
	if uint64(len(t.values)) > 1<<32-1 {
		panic("string table exhausted")
	}
	// A short substring must not retain a larger temporary signature or URL.
	s = strings.Clone(s)
	id := stringID(len(t.values))
	t.values = append(t.values, s)
	t.ids[s] = id
	return id
}

func (t *stringTable) value(id stringID) string {
	if id == 0 {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.values[id]
}
