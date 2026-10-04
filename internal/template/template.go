package template

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var variablePattern = regexp.MustCompile(`{{\s*([A-Za-z0-9_.-]+)\s*}}`)

type Row struct {
	Email  string
	Values map[string]string
	Number int
}

func Render(src string, values map[string]string) (string, error) {
	missing := map[string]bool{}
	out := variablePattern.ReplaceAllStringFunc(src, func(match string) string {
		key := variablePattern.FindStringSubmatch(match)[1]
		val, ok := values[key]
		if !ok {
			missing[key] = true
		}
		return val
	})
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		return "", fmt.Errorf("missing template variable(s): %s", strings.Join(keys, ", "))
	}
	return out, nil
}

func ReadCSV(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open CSV %q: %w", path, err)
	}
	defer f.Close()
	return ParseCSV(f)
}
func ParseCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	headers, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("invalid CSV header: %w", err)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("CSV must have a header row")
	}
	idx := map[string]int{}
	for i, h := range headers {
		h = strings.TrimSpace(h)
		if h == "" {
			return nil, fmt.Errorf("CSV header column %d is empty", i+1)
		}
		if _, ok := idx[h]; ok {
			return nil, fmt.Errorf("duplicate CSV column %q", h)
		}
		idx[h] = i
	}
	emailIndex, ok := idx["email"]
	if !ok {
		return nil, fmt.Errorf("CSV must include an email column")
	}
	var rows []Row
	seen := map[string]bool{}
	line := 1
	for {
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		line++
		if e != nil {
			return nil, fmt.Errorf("malformed CSV at row %d: %w", line, e)
		}
		if len(rec) != len(headers) {
			return nil, fmt.Errorf("malformed CSV at row %d: expected %d columns, got %d", line, len(headers), len(rec))
		}
		email := strings.TrimSpace(rec[emailIndex])
		if email == "" {
			return nil, fmt.Errorf("CSV row %d has an empty email", line)
		}
		key := strings.ToLower(email)
		if seen[key] {
			continue
		}
		seen[key] = true
		values := map[string]string{}
		for i, h := range headers {
			values[h] = strings.TrimSpace(rec[i])
		}
		rows = append(rows, Row{Email: email, Values: values, Number: line})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CSV contains no recipient rows")
	}
	return rows, nil
}
