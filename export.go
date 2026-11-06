// export.go — the CSV writer for the dissemination layer.
//
// pod used to provide this helper; pod is a service now, so the one piece the
// export path needs is local. A UTF-8 byte-order mark is written by default so
// spreadsheet applications open non-ASCII analyst text correctly, and
// encoding/csv quotes anything that needs it.
package main

import (
	"encoding/csv"
	"io"
)

// writeCSVTable writes a BOM, a header row and the rows.
func writeCSVTable(w io.Writer, header []string, rows [][]string) error {
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, row := range rows {
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
