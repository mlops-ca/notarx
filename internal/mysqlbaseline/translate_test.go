package mysqlbaseline

import (
	"strings"
	"testing"
)

func TestTranslate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"(size >= 0)", "(`size` >= 0)"},
		{"singleton", "`singleton`"},
		{"(state = ANY (ARRAY['a'::text, 'b'::text]))", "(`state` IN ('a', 'b'))"},
		{"((visibility)::text = ANY ((ARRAY['private'::character varying, 'public'::character varying])::text[]))", "(`visibility` IN ('private', 'public'))"},
		{"(kind <> ALL (ARRAY['x'::text]))", "(`kind` NOT IN ('x'))"},
		{"((length(name) >= 1) AND (length(name) <= 128))", "((CHAR_LENGTH(`name`) >= 1) AND (CHAR_LENGTH(`name`) <= 128))"},
		{"(digest ~ '^sha256:[0-9a-f]{64}$'::text)", "REGEXP_LIKE(`digest`, '^sha256:[0-9a-f]{64}$', 'c')"},
		{"(path !~ '\\.\\.'::text)", "(NOT REGEXP_LIKE(`path`, '\\\\.\\\\.', 'c'))"},
		{"(name ~* 'x')", "REGEXP_LIKE(`name`, 'x', 'i')"},
		{"(object_name ~~ 'a%'::text)", "(`object_name` LIKE 'a%')"},
		{"(object_name !~~ 'a%')", "(`object_name` NOT LIKE 'a%')"},
		{"(object_name ~~* 'a%')", "(LOWER(`object_name`) LIKE LOWER('a%'))"},
		{"(object_key = ('blobs/'::text || substr(digest, 8)))", "(`object_key` = CONCAT('blobs/', SUBSTR(`digest`, 8)))"},
		{"(jsonb_typeof(evidence) = 'object'::text)", "(JSON_TYPE(`evidence`) = 'OBJECT')"},
		{"((a IS NULL) AND (b IS NOT NULL))", "((`a` IS NULL) AND (`b` IS NOT NULL))"},
		{"(a IS DISTINCT FROM b)", "(NOT (`a` <=> `b`))"},
		{"(a IS NOT DISTINCT FROM b)", "(`a` <=> `b`)"},
		{"(flag IS TRUE)", "(`flag` IS TRUE)"},
		{"(flag IS NOT FALSE)", "(`flag` IS NOT FALSE)"},
		{"(NOT negative)", "(NOT `negative`)"},
		{"(confidence >= (0)::numeric)", "(`confidence` >= 0)"},
		{"((state = ANY (ARRAY['a'::text, 'b'::text])) = (completed_at IS NOT NULL))", "((`state` IN ('a', 'b')) = (`completed_at` IS NOT NULL))"},
		{"(\"position\" >= 0)", "(`position` >= 0)"},
		{"((payload ->> 'format'::text) = 'x')", "(JSON_UNQUOTE(JSON_EXTRACT(`payload`, '$.\"format\"')) = 'x')"},
		{"(a + 1 > b * 2 - 3 / 4 % 5)", "((`a` + 1) > ((`b` * 2) - ((3 / 4) % 5)))"},
		{"(a > -1)", "(`a` > (-1))"},
		{"(a = 'it''s')", "(`a` = 'it''s')"},
		{"(coalesce(a, b) = 'x')", "(COALESCE(`a`, `b`) = 'x')"},
		{"(a = true OR b = false OR c IS NULL)", "(((`a` = TRUE) OR (`b` = FALSE)) OR (`c` IS NULL))"},
		{"(a::character varying(10) = 'x')", "(`a` = 'x')"},
		{"(roles <@ ARRAY['member'::text, 'admin'::text])", "JSON_CONTAINS(JSON_ARRAY('member', 'admin'), `roles`)"},
		{"(tags @> ARRAY['a'::text])", "JSON_CONTAINS(`tags`, JSON_ARRAY('a'))"},
		{"(event_types <@ '[\"a\"]'::jsonb)", "JSON_CONTAINS(CAST('[\"a\"]' AS JSON), `event_types`)"},
		{"(policy ?& ARRAY['k'::text, 'l'::text])", "JSON_CONTAINS_PATH(`policy`, 'all', '$.\"k\"', '$.\"l\"')"},
		{"(cardinality(scopes) > 0)", "(JSON_LENGTH(`scopes`) > 0)"},
		{"(jsonb_array_length(x) > 0)", "(JSON_LENGTH(`x`) > 0)"},
		{"(k = split_part(version, '+'::text, 1))", "(`k` = SUBSTRING_INDEX(`version`, '+', 1))"},
		{"(k = replace(n, '_'::text, '-'::text))", "(`k` = REPLACE(`n`, '_', '-'))"},
		{"(((p ->> 'a'::text))::integer < ((p ->> 'b'::text))::integer)", "(CAST(JSON_UNQUOTE(JSON_EXTRACT(`p`, '$.\"a\"')) AS SIGNED) < CAST(JSON_UNQUOTE(JSON_EXTRACT(`p`, '$.\"b\"')) AS SIGNED))"},
		{"((x)::numeric > 1)", "(CAST(`x` AS DECIMAL(30,10)) > 1)"},
		{"((ARRAY['x'::text])[1] = a)", ""},
	} {
		got, err := Translate(tc.in)
		if tc.want == "" {
			if err == nil {
				t.Errorf("Translate(%q) = %q; want an error", tc.in, got.SQL)
			}
			continue
		}
		if err != nil || got.SQL != tc.want {
			t.Errorf("Translate(%q) = %q, %v\nwant %q", tc.in, got.SQL, err, tc.want)
		}
	}
	got, err := Translate("((a = 1) AND (length(b) > 2) AND (a > 0))")
	if err != nil || strings.Join(got.Columns, ",") != "a,b" {
		t.Errorf("columns = %v, %v", got.Columns, err)
	}
}

func TestTranslateRejects(t *testing.T) {
	for in, want := range map[string]string{
		"(a = E'x')":                          "string form",
		"(a = 'x)":                            "unterminated string",
		"(\"a = 1)":                           "unterminated identifier",
		"(a = 1) extra":                       "unexpected",
		"(a = 1":                              "expected \")\"",
		"(a # 1)":                             "unexpected character",
		"(regexp_replace(a, 'x', 'y') = 'z')": "no MySQL translation",
		"(a = ANY (b))":                       "ARRAY",
		"(a = ANY (ARRAY[]::text[]))":         "non-empty",
		"(a < ANY (ARRAY['x'::text]))":        "not supported",
		"(a ->> b = 'x')":                     "string literal key",
		"(jsonb_typeof(a) = 'number'::text)":  "no MySQL JSON_TYPE",
		"(jsonb_typeof(a, b) = 'object')":     "one argument",
		"ARRAY['x'::text]":                    "bare ARRAY",
		"(a IS DISTINCT b)":                   "FROM",
		"(a IS SOMETHING)":                    "unsupported IS",
		"(a::)":                               "expected a type name",
		"(a =)":                               "unexpected",
		"(,)":                                 "unexpected",
		"":                                    "unexpected end",
	} {
		if _, err := Translate(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Translate(%q) error = %v; want it to contain %q", in, err, want)
		}
	}
}
