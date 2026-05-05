// Package proto loads .proto files and exposes their enums via an in-memory index.
package proto

import (
	"sort"
	"strings"
)

// EnumValue is a single (name, number) pair inside an enum.
type EnumValue struct {
	Name   string `json:"name"`
	Number int32  `json:"number"`
}

// Enum is a fully-resolved protobuf enum.
//
// Name is the canonical fully-qualified name in proto convention:
//
//	<package>.<enclosing-messages>.<EnumName>
//
// Examples:
//
//	"my.pkg.ClientOperatingSystem"                // top-level, packaged
//	"my.pkg.ProxyResponseProto.Status"            // nested under one message
//	"my.pkg.Outer.Inner.Status"                   // nested under two
//	"BareEnum"                                    // no package, no nesting
//
// SimpleName is the trailing segment ("ClientOperatingSystem", "Status").
// Package is the proto package, or "" if none was declared.
type Enum struct {
	Name       string      `json:"name"`
	SimpleName string      `json:"simpleName"`
	Package    string      `json:"package"`
	Values     []EnumValue `json:"values"`
}

// EnumIndex is the in-memory lookup served by the API. Keys are FQNs, so the
// index can hold enums from many packages and many files without collision.
type EnumIndex struct {
	packages map[string]struct{}
	enums    map[string]Enum
}

// NewEnumIndex builds an index from a slice of Enums. Duplicate FQNs are
// resolved last-write-wins; LoadIndex callers should pre-deduplicate or
// reject duplicates if they have a preference.
func NewEnumIndex(enums []Enum) *EnumIndex {
	idx := &EnumIndex{
		packages: make(map[string]struct{}),
		enums:    make(map[string]Enum, len(enums)),
	}
	for _, e := range enums {
		idx.enums[e.Name] = e
		if e.Package != "" {
			idx.packages[e.Package] = struct{}{}
		}
	}
	return idx
}

// Len returns the number of enums in the index.
func (i *EnumIndex) Len() int { return len(i.enums) }

// Packages returns the sorted list of distinct proto package names seen.
func (i *EnumIndex) Packages() []string {
	out := make([]string, 0, len(i.packages))
	for p := range i.packages {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// List returns enum FQNs, optionally filtered by case-insensitive substring.
// Results are sorted ascending for stable output.
func (i *EnumIndex) List(filter string) []string {
	filter = strings.ToLower(filter)
	out := make([]string, 0, len(i.enums))
	for name := range i.enums {
		if filter == "" || strings.Contains(strings.ToLower(name), filter) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Get returns the enum with the given FQN.
func (i *EnumIndex) Get(name string) (Enum, bool) {
	e, ok := i.enums[name]
	return e, ok
}

// ResolveNumber finds the value name for a number within an enum.
func (i *EnumIndex) ResolveNumber(enumName string, number int32) (string, bool) {
	e, ok := i.enums[enumName]
	if !ok {
		return "", false
	}
	for _, v := range e.Values {
		if v.Number == number {
			return v.Name, true
		}
	}
	return "", false
}

// ResolveName finds the number for a value name within an enum.
func (i *EnumIndex) ResolveName(enumName, valueName string) (int32, bool) {
	e, ok := i.enums[enumName]
	if !ok {
		return 0, false
	}
	for _, v := range e.Values {
		if v.Name == valueName {
			return v.Number, true
		}
	}
	return 0, false
}
