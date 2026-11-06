// codec.go — record encoding between Go structs and pod's map[string]string.
//
// pod stores every row as a plain XML file whose fields are name/value text
// pairs, so a record type has to be flattened to map[string]string and rebuilt.
// The mapping is generic — each exported field's JSON name becomes the pod
// field name, omitempty is honoured, and slices/structs are stored as compact
// JSON. Since pod is a remote service now, the codec lives in podclient.go; the
// names here keep the rest of the platform reading as the information model
// rather than as pod plumbing.
package main

// fieldsOf flattens a record struct into pod's field map.
func fieldsOf(v any) (map[string]string, error) { return structFields(v) }

// applyFields rebuilds a record struct from pod's field map.
func applyFields(m map[string]string, v any) error { return applyStruct(m, v) }

// columnNames derives pod column definitions from a record struct, so a table's
// declared schema always matches the type that fills it.
func columnNames(sample any) ([]string, error) { return structColumns(sample) }
