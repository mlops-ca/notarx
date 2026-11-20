package migrate

import (
	"database/sql/driver"

	"github.com/mlops-ca/notarx/internal/testsupport/fakesql"
)

type (
	fakeServer = fakesql.Server
	fakeResult = fakesql.Result
)

func one(columns string, values ...driver.Value) fakeResult { return fakesql.One(columns, values...) }
func none() fakeResult                                      { return fakesql.None() }
