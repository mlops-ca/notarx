package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
)

// Listing totals (ADR 0023 section 8). A list never counts a whole table: the count is taken over at most
// ListTotalCap+1 rows, so a total is exact up to the cap and a lower bound or a planner estimate above it.

// ListTotalRelation says how a ListTotal value relates to the true count.
type ListTotalRelation string

const (
	// TotalExact is the exact count (the relation "eq").
	TotalExact ListTotalRelation = "eq"
	// TotalAtLeast means the count is at least Value (the relation "gte").
	TotalAtLeast ListTotalRelation = "gte"
	// TotalEstimate is the planner estimate (the relation "estimate").
	TotalEstimate ListTotalRelation = "estimate"
)

// ListTotalCap is the largest count reported exactly.
const ListTotalCap = 10000

// ListTotal is the {value, relation} total of a listing.
type ListTotal struct {
	Value    int64
	Relation ListTotalRelation
}

// BoundedListTotal turns a count taken over at most ListTotalCap+1 rows into a ListTotal.
func BoundedListTotal(count int64) ListTotal {
	if count > ListTotalCap {
		return ListTotal{Value: ListTotalCap + 1, Relation: TotalAtLeast}
	}
	return ListTotal{Value: count, Relation: TotalExact}
}

// boundedCountSQL wraps a FROM clause (table, joins and WHERE) in a count that stops at ListTotalCap+1 rows.
func boundedCountSQL(from string) string {
	return "SELECT count(*) FROM (SELECT 1 FROM " + from + " LIMIT " + strconv.Itoa(ListTotalCap+1) + ") bounded"
}

// boundedTotal counts the rows of from (args bind its placeholders) up to the cap. When the cap is hit and
// estimateTable is not empty (the listing has no filter), the planner estimate of that table replaces the lower bound.
func boundedTotal(ctx context.Context, q sqlRunner, estimateTable, from string, args ...any) (ListTotal, error) {
	var count int64
	if err := q.QueryRowContext(ctx, boundedCountSQL(from), args...).Scan(&count); err != nil {
		return ListTotal{}, err
	}
	total := BoundedListTotal(count)
	if total.Relation == TotalExact || estimateTable == "" {
		return total, nil
	}
	var estimate int64
	err := q.QueryRowContext(ctx, `SELECT reltuples::bigint FROM pg_class WHERE oid=to_regclass($1)`, estimateTable).Scan(&estimate)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && estimate <= ListTotalCap) {
		return total, nil
	}
	if err != nil {
		return ListTotal{}, err
	}
	return ListTotal{Value: estimate, Relation: TotalEstimate}, nil
}

// ErrInvalidPageToken reports a page token that is malformed or was issued for different filters.
var ErrInvalidPageToken = errors.New("page token is invalid for this query")

type keysetToken struct {
	Filter string   `json:"f"`
	Keys   []string `json:"k"`
}

// EncodeKeysetToken builds the opaque cursor of a keyset page. The filter string binds the token to the
// query that produced it; keys are the sort-key values of the last row served.
func EncodeKeysetToken(filter string, keys ...string) string {
	raw, _ := json.Marshal(keysetToken{Filter: filter, Keys: keys})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeKeysetToken returns the keys of a token issued for filter and wantKeys sort keys.
func DecodeKeysetToken(token, filter string, wantKeys int) ([]string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalidPageToken
	}
	var decoded keysetToken
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.Filter != filter || len(decoded.Keys) != wantKeys {
		return nil, ErrInvalidPageToken
	}
	return decoded.Keys, nil
}
