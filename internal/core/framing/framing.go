// Package framing is a small low-level append-only JSONL file utility
// shared by the transcript and audit stores. It carries no domain
// knowledge of records, sequence spaces, or retention: those stay
// independent per store, per the design spec.
package framing

import (
	"bufio"
	"encoding/json"
	"os"
)

// AppendJSONL appends v as one JSON line to the file at path, creating
// it with mode 0600 if it does not exist. Callers serialize their own
// writes; AppendJSONL does not lock.
func AppendJSONL(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(v)
}

// ReadAllJSONL decodes every line of the file at path into T. A
// missing file is treated as empty, not an error.
func ReadAllJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []T
	dec := json.NewDecoder(bufio.NewReader(f))
	for dec.More() {
		var v T
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
