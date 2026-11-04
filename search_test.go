package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name, input string
		want        query
	}{
		{"simple", "String", query{Class: "string"}},
		{"prefix", "17 OptionalLong#orElse", query{Version: 17, Class: "optionallong", Member: "orelse"}},
		{"suffix", "OPTIONALLONG#ORELSE 21", query{Version: 21, Class: "optionallong", Member: "orelse"}},
		{"minimum version", "9 String", query{Version: 9, Class: "string"}},
		{"qualified", "Java.Lang.String#ValueOf(int)", query{Class: "java.lang.string", Member: "valueof(int)"}},
		{"nested", "Java.Util.Map.Entry", query{Class: "java.util.map.entry"}},
		{"nested simple", "Map.Entry#getKey()", query{Class: "map.entry", Member: "getkey()"}},
		{"dollar nested", "Map$Entry", query{Class: "map$entry"}},
		{"identifier", "_Class2#$member_1", query{Class: "_class2", Member: "$member_1"}},
		{"parameters", "String#valueOf( char[], int, int ) 17", query{Version: 17, Class: "string", Member: "valueof(char[],int,int)"}},
		{"qualified parameters", "17 List#of(java.lang.String, java.lang.String...)", query{Version: 17, Class: "list", Member: "of(java.lang.string,java.lang.string...)"}},
		{"whitespace", " \t21\nString#valueOf( int ) \r\n", query{Version: 21, Class: "string", Member: "valueof(int)"}},
		{"max length", strings.Repeat("a", 1024), query{Class: strings.Repeat("a", 1024)}},
		{"length after trimming", "  " + strings.Repeat("a", 1024) + "\t", query{Class: strings.Repeat("a", 1024)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseQuery(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("parseQuery(%q) = %#v, %v; want %#v, nil", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestParseQueryInvalid(t *testing.T) {
	tests := []struct{ name, input string }{
		{"empty", ""}, {"blank", " \n\t"},
		{"too long", strings.Repeat("a", 1025)},
		{"old prefix", "8 String"}, {"old suffix", "String 8"},
		{"zero version", "0 String"}, {"overflow version", strings.Repeat("9", 30) + " String"},
		{"duplicate same version", "17 String 17"}, {"duplicate different versions", "17 String 21"},
		{"version only", "17"}, {"negative version", "-1 String"},
		{"decimal version", "17.0 String"}, {"version in middle", "String 17 #length"},
		{"two classes", "String Integer"}, {"empty package", "java..String"},
		{"leading dot", ".String"}, {"trailing dot", "String."},
		{"digit first", "1String"}, {"slash", "java/lang/String"},
		{"missing class", "#length"}, {"missing member", "String#"},
		{"extra hash", "String#length#other"}, {"qualified member", "String#java.length"},
		{"member space", "String#long name"}, {"space before signature", "String#length ()"},
		{"missing close", "String#valueOf(int"}, {"extra close", "String#valueOf(int))"},
		{"nested parentheses", "String#valueOf((int))"},
		{"signature suffix", "String#valueOf(int)junk"}, {"semicolon", "String#valueOf(int;)"},
		{"empty parameter", "String#valueOf(int,,int)"},
		{"trailing comma", "String#valueOf(int,)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := parseQuery(tt.input); err == nil {
				t.Errorf("parseQuery(%q) = %#v, nil; want an error", tt.input, got)
			}
		})
	}
}

func TestVersionIndexSearch(t *testing.T) {
	table := &stringTable{}
	id := table.intern
	str := &javaType{FQN: id("java.lang.String"), Path: id("String.html"), Members: map[stringID][]member{
		id("length"): {{Signature: id("length()"), Anchor: id("length()")}},
		id("valueof"): {
			{Signature: id("valueOf(int)"), Anchor: id("valueOf(int)")},
			{Signature: id("valueOf(char[], int, int)"), Anchor: id("valueOf(char[],int,int)")},
			{Signature: id("valueOf(int)"), Anchor: id("valueOf(int)")},
		},
	}}
	utilDate := &javaType{FQN: id("java.util.Date"), Path: id("util/Date.html"), Members: map[stringID][]member{
		id("gettime"): {{Signature: id("getTime()"), Anchor: id("getTime()")}},
	}}
	sqlDate := &javaType{FQN: id("java.sql.Date"), Path: id("sql/Date.html"), Members: map[stringID][]member{
		id("gettime"): {{Signature: id("getTime()"), Anchor: id("getTime()")}},
	}}
	entry := &javaType{FQN: id("java.util.Map.Entry"), Path: id("Map.Entry.html")}
	idx := &versionIndex{Root: "https://docs.example/", Strings: table, Types: map[stringID][]*javaType{
		id("string"): {str, str}, id("java.lang.string"): {str},
		id("date"): {utilDate, sqlDate, utilDate}, id("java.sql.date"): {sqlDate},
		id("map.entry"): {entry}, id("java.util.map.entry"): {entry},
	}}
	tests := []struct {
		input string
		want  []result
	}{
		{"STRING", []result{{"java.lang.String", "https://docs.example/String.html"}}},
		{"java.lang.String", []result{{"java.lang.String", "https://docs.example/String.html"}}},
		{"String#length", []result{{"java.lang.String#length()", "https://docs.example/String.html#length()"}}},
		{"String#valueOf", []result{
			{"java.lang.String#valueOf(char[], int, int)", "https://docs.example/String.html#valueOf(char[],int,int)"},
			{"java.lang.String#valueOf(int)", "https://docs.example/String.html#valueOf(int)"},
		}},
		{"String#VALUEOF( INT )", []result{{"java.lang.String#valueOf(int)", "https://docs.example/String.html#valueOf(int)"}}},
		{"String#valueOf(char[],int,int)", []result{{"java.lang.String#valueOf(char[], int, int)", "https://docs.example/String.html#valueOf(char[],int,int)"}}},
		{"Date", []result{{"java.sql.Date", "https://docs.example/sql/Date.html"}, {"java.util.Date", "https://docs.example/util/Date.html"}}},
		{"Date#getTime()", []result{
			{"java.sql.Date#getTime()", "https://docs.example/sql/Date.html#getTime()"},
			{"java.util.Date#getTime()", "https://docs.example/util/Date.html#getTime()"},
		}},
		{"java.sql.Date", []result{{"java.sql.Date", "https://docs.example/sql/Date.html"}}},
		{"MAP.ENTRY", []result{{"java.util.Map.Entry", "https://docs.example/Map.Entry.html"}}},
		{"java.util.Map.Entry", []result{{"java.util.Map.Entry", "https://docs.example/Map.Entry.html"}}},
		{"Missing", nil}, {"String#missing", nil}, {"String#valueOf(long)", nil},
		{"String#length(int)", nil}, {"String#value", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			q, err := parseQuery(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := idx.search(q); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("search(%#v) = %#v; want %#v", q, got, tt.want)
			}
		})
	}
	if got := (&versionIndex{}).search(query{Class: "string"}); len(got) != 0 {
		t.Errorf("empty index search = %#v; want no results", got)
	}
}
