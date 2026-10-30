// codec.go — record encoding between Go structs and pod's map[string]string.
//
// pod stores every row as a plain XML file whose fields are name/value text
// pairs (pod.Field), so a record type has to be flattened to
// map[string]string and rebuilt. The mapping is generic — each exported
// field's JSON name becomes the pod field name, omitempty is honoured, and
// slices/structs are stored as compact JSON — so it now lives in pod itself
// (pod.StructFields, pod.ApplyStruct, pod.StructColumns).
//
// stenella keeps these thin names because the rest of the platform reads as
// the information model rather than as pod plumbing; there is no reflection
// here any more.
package main

import "azzurrotech/pod"

// fieldsOf flattens a record struct into pod's field map.
func fieldsOf(v any) (map[string]string, error) { return pod.StructFields(v) }

// applyFields rebuilds a record struct from pod's field map.
func applyFields(m map[string]string, v any) error { return pod.ApplyStruct(m, v) }

// columnNames derives pod column definitions from a record struct, so a table's
// declared schema always matches the type that fills it.
func columnNames(sample any) ([]string, error) { return pod.StructColumns(sample) }
