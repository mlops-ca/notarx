package readcache

import (
	"io"
	"strconv"
)

// WriteMetrics renders notarx_cache_hits_total, notarx_cache_misses_total,
// notarx_cache_evictions_total and notarx_cache_entries per cache.
func WriteMetrics(w io.Writer, stats []Stats) {
	type series struct {
		name, kind string
		value      func(Stats) uint64
	}
	for _, s := range []series{
		{"notarx_cache_hits_total", "counter", func(s Stats) uint64 { return s.Hits }},
		{"notarx_cache_misses_total", "counter", func(s Stats) uint64 { return s.Misses }},
		{"notarx_cache_evictions_total", "counter", func(s Stats) uint64 { return s.Evictions }},
		{"notarx_cache_entries", "gauge", func(s Stats) uint64 { return uint64(s.Entries) }},
	} {
		_, _ = io.WriteString(w, "# TYPE "+s.name+" "+s.kind+"\n")
		for _, st := range stats {
			_, _ = io.WriteString(w, s.name+"{cache=\""+st.Name+"\"} "+strconv.FormatUint(s.value(st), 10)+"\n")
		}
	}
}
