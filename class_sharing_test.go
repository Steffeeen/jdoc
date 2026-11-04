package main

import (
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"
)

func classSharingBuild(t *testing.T, l *loader, version int, types, members []indexEntry) *versionIndex {
	t.Helper()
	idx, err := l.buildIndex(version, fmt.Sprintf("https://docs.oracle.com/en/java/javase/%d/docs/api/", version), types, members, nil)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func classSharingAliases(t *testing.T, idx *versionIndex, want *javaType) {
	t.Helper()
	if len(idx.Types) != 3 {
		t.Fatalf("alias count = %d; want 3", len(idx.Types))
	}
	for _, alias := range []string{"p.outer.inner", "outer.inner", "inner"} {
		got := idx.Types[idxTestID(t, idx.Strings, alias)]
		if len(got) != 1 || got[0] != want {
			t.Fatalf("version %d alias %q = %v; want pointer %p", idx.Version, alias, got, want)
		}
	}
}

func TestClassSharingAcrossVersionsAndAliases(t *testing.T) {
	l := &loader{}
	types := []indexEntry{{Package: "p", Label: "Outer.Inner", Module: "m"}}
	members := []indexEntry{
		{Package: "p", Class: "Outer.Inner", Label: "f()"},
		{Package: "p", Class: "Outer.Inner", Label: "f(int)"},
		{Package: "p", Class: "Outer.Inner", Label: "VALUE"},
	}
	first := classSharingBuild(t, l, 11, types, members)
	typ := first.Types[idxTestID(t, first.Strings, "inner")][0]
	for _, version := range []int{11, 17, 25} {
		idx := first
		if version != 11 {
			// Reverse insertion of distinct keys, preserving overload order.
			reordered := []indexEntry{members[2], members[0], members[1]}
			idx = classSharingBuild(t, l, version, types, reordered)
		}
		classSharingAliases(t, idx, typ)
		want := []result{{Label: "p.Outer.Inner#f(int)", URL: idx.Root + "m/p/Outer.Inner.html#f(int)"}}
		if got := idx.search(query{Class: "inner", Member: "f(int)"}); !reflect.DeepEqual(got, want) {
			t.Errorf("version %d search = %+v; want %+v", version, got, want)
		}
	}
	if got := len(l.classes[typ.FQN]); got != 1 {
		t.Errorf("identical builds cached %d variants; want 1", got)
	}
}

func TestClassSharingChangedBuildsAndPriorVariants(t *testing.T) {
	for _, change := range []string{"path", "signature", "anchor", "member key", "overload count", "overload order", "added member", "removed member"} {
		t.Run(change, func(t *testing.T) {
			l := &loader{}
			types := []indexEntry{{Package: "p", Label: "Outer.Inner", URL: "original.html"}}
			members := []indexEntry{
				{Package: "p", Class: "Outer.Inner", Label: "f()", URL: "first"},
				{Package: "p", Class: "Outer.Inner", Label: "f(int)", URL: "second"},
				{Package: "p", Class: "Outer.Inner", Label: "VALUE", URL: "value"},
			}
			first := classSharingBuild(t, l, 11, types, members)
			inner := idxTestID(t, first.Strings, "inner")
			old := first.Types[inner][0]
			wantMembers := make(map[stringID][]member, len(old.Members))
			for key, overloads := range old.Members {
				wantMembers[key] = slices.Clone(overloads)
			}
			wantSearch := first.search(query{Class: "inner"})
			changedTypes, changedMembers := slices.Clone(types), slices.Clone(members)
			switch change {
			case "path":
				changedTypes[0].URL = "changed.html"
			case "signature":
				changedMembers[1].Label = "f(long)"
			case "anchor":
				changedMembers[0].URL = "changed"
			case "member key":
				changedMembers[2].Label = "OTHER"
			case "overload count":
				changedMembers = append(changedMembers, indexEntry{Package: "p", Class: "Outer.Inner", Label: "f(long)"})
			case "overload order":
				changedMembers[0], changedMembers[1] = changedMembers[1], changedMembers[0]
			case "added member":
				changedMembers = append(changedMembers, indexEntry{Package: "p", Class: "Outer.Inner", Label: "g()"})
			case "removed member":
				changedMembers = changedMembers[:2]
			}
			c := &catalog{}
			c.publish(first)
			second := classSharingBuild(t, l, 17, changedTypes, changedMembers)
			changed := second.Types[inner][0]
			if changed == old {
				t.Fatal("changed class reused the old pointer")
			}
			classSharingAliases(t, second, changed)
			c.publish(second)
			third := classSharingBuild(t, l, 21, types, members)
			classSharingAliases(t, third, old)
			fourth := classSharingBuild(t, l, 25, changedTypes, changedMembers)
			classSharingAliases(t, fourth, changed)
			if len(l.classes[old.FQN]) != 2 {
				t.Fatalf("cached variants = %d; want 2", len(l.classes[old.FQN]))
			}
			published, _ := c.get(11)
			if published != first || old.Path != idxTestID(t, first.Strings, "original.html") || !reflect.DeepEqual(old.Members, wantMembers) {
				t.Fatal("future builds mutated the older published class")
			}
			classSharingAliases(t, first, old)
			if got := first.search(query{Class: "inner"}); !reflect.DeepEqual(got, wantSearch) {
				t.Fatalf("old search changed: %+v; want %+v", got, wantSearch)
			}
		})
	}
}

func TestClassSharingExactEmptyMapsAndFQN(t *testing.T) {
	for _, tc := range []struct {
		name     string
		first    map[stringID][]member
		second   map[stringID][]member
		otherFQN bool
		share    bool
	}{
		{name: "empty maps", first: map[stringID][]member{}, second: map[stringID][]member{}, share: true},
		{name: "nil and empty maps", second: map[stringID][]member{}, share: true},
		{name: "same empty key", first: map[stringID][]member{3: nil}, second: map[stringID][]member{3: {}}, share: true},
		{name: "different empty keys", first: map[stringID][]member{3: nil}, second: map[stringID][]member{4: nil}},
		{name: "missing empty key", first: map[stringID][]member{3: nil}, second: map[stringID][]member{}},
		{name: "different FQN", first: map[stringID][]member{3: {{Signature: 4, Anchor: 5}}}, second: map[stringID][]member{3: {{Signature: 4, Anchor: 5}}}, otherFQN: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &loader{}
			first := &javaType{FQN: 1, Path: 2, Members: tc.first}
			second := &javaType{FQN: 1, Path: 2, Members: tc.second}
			if tc.otherFQN {
				second.FQN = 6
			}
			if got := l.shareClass(first); got != first {
				t.Fatal("first candidate was not cached as-is")
			}
			want := second
			if tc.share {
				want = first
			}
			if got := l.shareClass(second); got != want {
				t.Fatalf("shareClass = %p; want %p", got, want)
			}
		})
	}
}

func TestClassSharingFailedBuildDoesNotCache(t *testing.T) {
	for _, failure := range []string{"late invalid type", "late invalid anchor", "no members"} {
		for _, seeded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/seeded=%v", failure, seeded), func(t *testing.T) {
				l := &loader{}
				types := []indexEntry{{Package: "p", Label: "Outer.Inner", URL: "type.html"}}
				members := []indexEntry{{Package: "p", Class: "Outer.Inner", Label: "f()"}}
				if seeded {
					classSharingBuild(t, l, 11, types, members)
				}
				before := make(map[stringID][]*javaType, len(l.classes))
				for fqn, variants := range l.classes {
					before[fqn] = slices.Clone(variants)
				}
				// Include a new class so even a partially shared build would grow the cache.
				types = append(types, indexEntry{Package: "p", Label: "New", URL: "new.html"})
				members = append(members, indexEntry{Package: "p", Class: "New", Label: "g()"})
				switch failure {
				case "late invalid type":
					types = append(types, indexEntry{Package: "p", Label: "Bad", URL: "../bad.html"})
				case "late invalid anchor":
					members = append(members, indexEntry{Package: "p", Class: "New", Label: "bad()", URL: "%zz"})
				case "no members":
					members = nil
				}
				idx, err := l.buildIndex(17, "https://docs.oracle.com/api/17/", types, members, nil)
				if err == nil || idx != nil {
					t.Fatalf("failed build = %v, %v", idx, err)
				}
				if len(l.classes) != len(before) {
					t.Fatal("failed build added cached classes")
				}
				for fqn, variants := range before {
					if !slices.Equal(l.classes[fqn], variants) {
						t.Fatal("failed build changed cached variants")
					}
				}
			})
		}
	}
}

func TestClassSharingConcurrentPublishedSearches(t *testing.T) {
	l := &loader{}
	types := []indexEntry{{Package: "p", Label: "Outer.Inner", Module: "m"}}
	members := []indexEntry{{Package: "p", Class: "Outer.Inner", Label: "f()"}}
	old := classSharingBuild(t, l, 11, types, members)
	inner := idxTestID(t, old.Strings, "inner")
	original := old.Types[inner][0]
	c := &catalog{}
	c.publish(old)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 1000; i++ {
				idx, _ := c.get(11)
				if idx != old || idx.Types[inner][0] != original {
					t.Error("old published index or class was replaced")
					return
				}
				latest, _ := c.get(0)
				for _, idx := range []*versionIndex{idx, latest} {
					anchor := "f()"
					if idx.Version%2 == 0 {
						anchor = "changed"
					}
					want := []result{{Label: "p.Outer.Inner#f()", URL: idx.Root + "m/p/Outer.Inner.html#" + anchor}}
					if got := idx.search(query{Class: "inner", Member: "f()"}); !reflect.DeepEqual(got, want) {
						t.Errorf("version %d search = %+v; want %+v", idx.Version, got, want)
						return
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		var changed *javaType
		for version := 12; version <= 75; version++ {
			entries := slices.Clone(members)
			want := original
			if version%2 == 0 {
				entries[0].URL = "changed"
				want = changed
			}
			idx, err := l.buildIndex(version, fmt.Sprintf("https://docs.oracle.com/api/%d/", version), types, entries, nil)
			if err != nil {
				t.Errorf("build version %d: %v", version, err)
				return
			}
			candidate := idx.Types[inner][0]
			if version == 12 {
				changed, want = candidate, candidate
				if changed == original {
					t.Error("changed shape shared original pointer")
					return
				}
			}
			classSharingAliases(t, idx, want)
			c.publish(idx)
			runtime.Gosched()
		}
	}()
	close(start)
	wg.Wait()
	if latest, version := c.get(0); latest == nil || version != 75 {
		t.Fatalf("latest version = %d; want 75", version)
	}
	if len(l.classes[original.FQN]) != 2 {
		t.Fatalf("cached variants = %d; want 2", len(l.classes[original.FQN]))
	}
	classSharingAliases(t, old, original)
	if len(original.Members) != 1 || !reflect.DeepEqual(old.search(query{Class: "inner", Member: "f()"}), []result{{Label: "p.Outer.Inner#f()", URL: old.Root + "m/p/Outer.Inner.html#f()"}}) {
		t.Fatal("future builds mutated the original published class")
	}
}
