package schemaparity

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// maxSeedRows bounds a seeded table: a freshly migrated database holds singleton
// rows, so a table with more rows is a migration that loads data.
const maxSeedRows = 20

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IntrospectSeeds reads the rows a migrated database holds, for the tables of
// catalogue. Values that differ between installations are left out: timestamp
// and uuid columns, generated columns, and text that looks like a UUID (the
// deployment identifier). NULLs are left out too.
func IntrospectSeeds(ctx context.Context, db *sql.DB, dialect string, catalogue Catalogue) (map[string][]map[string]string, error) {
	quote := func(name string) string { return "\"" + name + "\"" }
	if dialect == DialectMySQL {
		quote = func(name string) string { return "`" + name + "`" }
	}
	seeds := map[string][]map[string]string{}
	for _, name := range catalogue.TableNames() {
		table := catalogue.Tables[name]
		var found []map[string]string
		err := rows(ctx, db, fmt.Sprintf("SELECT * FROM %s LIMIT %d", quote(name), maxSeedRows+1), func(r *sql.Rows) error {
			columns, err := r.Columns()
			if err != nil {
				return err
			}
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := r.Scan(targets...); err != nil {
				return err
			}
			row := map[string]string{}
			for i, column := range columns {
				spec, known := table.Columns[column]
				if !known || spec.Generated || spec.Type == typeUUID || spec.Type == typeTimestamp || values[i] == nil {
					continue
				}
				text, err := seedValue(spec.Type, values[i])
				if err != nil {
					return fmt.Errorf("%s.%s: %w", name, column, err)
				}
				if spec.Type == typeText && uuidShape.MatchString(text) {
					continue
				}
				row[column] = text
			}
			found = append(found, row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read the rows of %s: %w", name, err)
		}
		if len(found) > maxSeedRows {
			return nil, fmt.Errorf("table %s holds more than %d rows after the migrations: a migration loads data", name, maxSeedRows)
		}
		if len(found) > 0 {
			slices.SortFunc(found, func(a, b map[string]string) int { return strings.Compare(rowKey(a), rowKey(b)) })
			seeds[name] = found
		}
	}
	return seeds, nil
}

func rowKey(row map[string]string) string {
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key + "=" + row[key] + ";")
	}
	return out.String()
}

// seedValue renders a scanned value in one form for both drivers.
func seedValue(logical string, value any) (string, error) {
	if raw, ok := value.([]byte); ok && logical != typeBytes {
		value = string(raw)
	}
	if strings.HasPrefix(logical, "array<") {
		return seedArray(value)
	}
	switch logical {
	case typeBool:
		switch v := value.(type) {
		case bool:
			return fmt.Sprint(v), nil
		case int64:
			return fmt.Sprint(v != 0), nil
		case string:
			return fmt.Sprint(v == "1" || v == "true" || v == "t"), nil
		}
	case typeJSON:
		var text []byte
		switch v := value.(type) {
		case string:
			text = []byte(v)
		default:
			var err error
			if text, err = json.Marshal(v); err != nil {
				return "", err
			}
		}
		var decoded any
		if err := json.Unmarshal(text, &decoded); err != nil {
			return "", fmt.Errorf("the value is not JSON: %w", err)
		}
		canonical, err := json.Marshal(decoded) // map keys are sorted
		return string(canonical), err
	case typeBytes:
		if raw, ok := value.([]byte); ok {
			return hex.EncodeToString(raw), nil
		}
	}
	return fmt.Sprint(value), nil
}

// seedArray renders an array in one form for both dialects: a JSON list of strings. PostgreSQL
// delivers the text form {a,b}, MySQL a JSON array.
func seedArray(value any) (string, error) {
	text := fmt.Sprint(value)
	var elements []string
	switch {
	case strings.HasPrefix(text, "{"):
		var err error
		if elements, err = parsePostgresArray(text); err != nil {
			return "", err
		}
	default:
		var decoded []any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return "", fmt.Errorf("the value is neither a PostgreSQL array nor a JSON array: %w", err)
		}
		for _, element := range decoded {
			elements = append(elements, fmt.Sprint(element))
		}
	}
	canonical, err := json.Marshal(append([]string{}, elements...))
	return string(canonical), err
}

// parsePostgresArray reads a one-dimensional array literal such as {a,"b,c"}.
func parsePostgresArray(text string) ([]string, error) {
	if !strings.HasSuffix(text, "}") {
		return nil, fmt.Errorf("%q is not an array literal", text)
	}
	body := text[1 : len(text)-1]
	var elements []string
	for i := 0; i < len(body); {
		var element strings.Builder
		if body[i] == '"' {
			i++
			for ; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' && i+1 < len(body) {
					i++
				}
				element.WriteByte(body[i])
			}
			if i >= len(body) {
				return nil, fmt.Errorf("%q has an unterminated element", text)
			}
			i++
		} else {
			for ; i < len(body) && body[i] != ','; i++ {
				element.WriteByte(body[i])
			}
		}
		elements = append(elements, element.String())
		if i < len(body) {
			if body[i] != ',' {
				return nil, fmt.Errorf("%q is not a one-dimensional array literal", text)
			}
			i++
		}
	}
	return elements, nil
}
