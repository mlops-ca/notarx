package schemaparity

import (
	"database/sql"
	"fmt"
	"strings"
)

// The logical types of the catalogue and the physical types they stand for. This
// table is the mapping ADR 0022 asks for; mysqlLogicalType and postgresLogicalType
// are its two directions, and compatible says where MySQL may differ on purpose.
//
//	logical      PostgreSQL                         MySQL 8.4
//	uuid         uuid                               CHAR(36) ascii_bin
//	text         text, varchar, char                CHAR, VARCHAR, *TEXT
//	bool         boolean                            TINYINT(1)
//	int16        smallint                           SMALLINT
//	int32        integer                            INT, MEDIUMINT
//	int64        bigint                             BIGINT
//	decimal      numeric                            DECIMAL
//	float32      real                               FLOAT
//	float64      double precision                   DOUBLE
//	timestamp    timestamptz, timestamp             DATETIME, TIMESTAMP
//	date         date                               DATE
//	time         time, timetz                       TIME
//	interval     interval                           (none: the column must be remapped)
//	json         json, jsonb                        JSON
//	bytes        bytea                              BINARY, VARBINARY, *BLOB
//	array<T>     T[]                                JSON (an array of T), or a child table
//
// A type that is not listed is an error, so a new column type forces a decision.

const (
	typeUUID      = "uuid"
	typeText      = "text"
	typeBool      = "bool"
	typeTimestamp = "timestamp"
	typeJSON      = "json"
	typeBytes     = "bytes"
)

var postgresTypes = map[string]string{
	"uuid": typeUUID, "text": typeText, "character varying": typeText, "varchar": typeText, "character": typeText, "bpchar": typeText,
	"boolean": typeBool, "bool": typeBool,
	"smallint": "int16", "int2": "int16", "integer": "int32", "int4": "int32", "bigint": "int64", "int8": "int64",
	"numeric": "decimal", "real": "float32", "float4": "float32", "double precision": "float64", "float8": "float64",
	"timestamp with time zone": typeTimestamp, "timestamp without time zone": typeTimestamp, "timestamptz": typeTimestamp, "timestamp": typeTimestamp,
	"date": "date", "time without time zone": "time", "time with time zone": "time", "time": "time", "timetz": "time",
	"interval": "interval",
	"jsonb":    typeJSON, "json": typeJSON,
	"bytea": typeBytes,
}

// postgresLogicalType maps information_schema.columns.data_type (and udt_name,
// the array element type, which carries a leading underscore) to a logical type.
func postgresLogicalType(dataType, udtName string) (string, error) {
	if dataType == "ARRAY" {
		element, ok := postgresTypes[strings.TrimPrefix(udtName, "_")]
		if !ok {
			return "", fmt.Errorf("no logical type for the PostgreSQL array of %q", udtName)
		}
		return "array<" + element + ">", nil
	}
	logical, ok := postgresTypes[dataType]
	if !ok {
		return "", fmt.Errorf("no logical type for the PostgreSQL type %q (%s)", dataType, udtName)
	}
	return logical, nil
}

// uuidLength is the width of CHAR(36), the MySQL form of a UUID.
const uuidLength = 36

// mysqlLogicalType maps information_schema.columns to a logical type.
func mysqlLogicalType(dataType, columnType string, charLength sql.NullInt64, collation sql.NullString) (string, error) {
	switch dataType {
	case "char":
		if charLength.Valid && charLength.Int64 == uuidLength && collation.String == "ascii_bin" {
			return typeUUID, nil
		}
		return typeText, nil
	case "varchar", "tinytext", "text", "mediumtext", "longtext":
		return typeText, nil
	case "tinyint":
		if columnType == "tinyint(1)" {
			return typeBool, nil
		}
	case "smallint":
		return "int16", nil
	case "int", "mediumint":
		return "int32", nil
	case "bigint":
		return "int64", nil
	case "decimal":
		return "decimal", nil
	case "float":
		return "float32", nil
	case "double":
		return "float64", nil
	case "datetime", "timestamp":
		return typeTimestamp, nil
	case "date":
		return "date", nil
	case "time":
		return "time", nil
	case "json":
		return typeJSON, nil
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return typeBytes, nil
	}
	return "", fmt.Errorf("no logical type for the MySQL type %q", columnType)
}

// compatible reports whether a MySQL column of logical type got may stand for the
// catalogue type want. A PostgreSQL array is a JSON array on MySQL (ADR 0022); the
// per-column decision to use a child table instead is an override of slice 3c.
func compatible(want, got string) bool {
	return want == got || (strings.HasPrefix(want, "array<") && got == typeJSON)
}
