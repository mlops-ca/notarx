package repository

import (
	"fmt"
	"time"
)

// intervalOf is the text of a duration as a PostgreSQL interval parameter ($n::interval), to the millisecond.
func intervalOf(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }
