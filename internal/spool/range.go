// Package spool holds the byte-range, staging, concurrency and single-flight
// helpers shared by formats that cache large upstream objects (Raw, Hugging
// Face). It has no format knowledge.
package spool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

const (
	maxRangeHeaderBytes = 8192
	maxRangeCount       = 16
	maxMultipartBytes   = 64 << 20
)

// Interval is an inclusive byte range.
type Interval struct{ Start, End int64 }

// ParseRanges bounds work before allocating interval metadata. Duplicate,
// overlapping and adjacent ranges coalesce, so a client cannot amplify bytes
// by repeating an interval. Invalid syntax rejects the entire field; valid
// non-overlapping intervals outside the representation are ignored.
func ParseRanges(value string, size int64) ([]Interval, bool) {
	if size <= 0 || len(value) > maxRangeHeaderBytes || !strings.HasPrefix(value, "bytes=") || strings.Count(value, ",") >= maxRangeCount {
		return nil, false
	}
	var intervals []Interval
	for _, member := range strings.Split(strings.TrimPrefix(value, "bytes="), ",") {
		member = strings.TrimSpace(member)
		parts := strings.SplitN(member, "-", 2)
		if len(parts) != 2 {
			return nil, false
		}
		// Validate even unsatisfiable members, including overflow and reversed
		// bounds, before deciding to omit them.
		if parts[0] != "" {
			start, err := parseRangeByte(parts[0])
			if err != nil {
				return nil, false
			}
			if parts[1] != "" {
				end, err := parseRangeByte(parts[1])
				if err != nil || end < start {
					return nil, false
				}
			}
			if start >= size {
				continue
			}
		}
		start, end, ok := ParseRange("bytes="+member, size)
		if !ok {
			return nil, false
		}
		intervals = append(intervals, Interval{start, end})
	}
	if len(intervals) == 0 {
		return nil, false
	}
	slices.SortFunc(intervals, func(a, b Interval) int {
		if a.Start < b.Start {
			return -1
		}
		if a.Start > b.Start {
			return 1
		}
		return 0
	})
	merged := intervals[:1]
	for _, interval := range intervals[1:] {
		last := &merged[len(merged)-1]
		if interval.Start <= last.End+1 {
			last.End = max(last.End, interval.End)
		} else {
			merged = append(merged, interval)
		}
	}
	// A single interval preserves the existing large single-range contract.
	// Only multipart responses have the aggregate 64 MiB payload limit.
	if len(merged) > 1 {
		var total int64
		for _, interval := range merged {
			length := interval.End - interval.Start + 1
			if length > maxMultipartBytes-total {
				return nil, false
			}
			total += length
		}
	}
	return merged, true
}

// RangeOpener opens length bytes at offset of one immutable representation and
// reports its total size.
type RangeOpener func(ctx context.Context, offset, length int64) (io.ReadCloser, int64, error)

// ErrUnavailable means the representation could not be opened before any
// response byte was written; the caller may still answer with an error status. It wraps the
// open error.
var ErrUnavailable = errors.New("range source unavailable")

// ErrTruncated means a later interval failed after the response was committed: the body is
// incomplete and its advertised length is never satisfied. It wraps the cause.
var ErrTruncated = errors.New("multipart response truncated")

// MaxMultipartBytes is the aggregate payload limit of a multipart/byteranges
// response.
const MaxMultipartBytes = maxMultipartBytes

// WriteMultipart writes a multipart/byteranges response for intervals. Only
// the first interval is opened before headers are committed; ErrUnavailable
// reports a failure at that point. Later failures truncate the body so the
// advertised Content-Length is never satisfied by a partial representation.
func WriteMultipart(w http.ResponseWriter, ctx context.Context, contentType string, size int64, intervals []Interval, open RangeOpener) error {
	boundary := multipart.NewWriter(io.Discard).Boundary()
	if contentType == "" || len(contentType) > 1024 || strings.ContainsAny(contentType, "\r\n") {
		contentType = "application/octet-stream"
	}
	prefixes := make([]string, len(intervals))
	var length int64
	for i, interval := range intervals {
		prefix := "--" + boundary + "\r\nContent-Range: bytes " + strconv.FormatInt(interval.Start, 10) + "-" + strconv.FormatInt(interval.End, 10) + "/" + strconv.FormatInt(size, 10) + "\r\nContent-Type: " + contentType + "\r\n\r\n"
		if i > 0 {
			prefix = "\r\n" + prefix
		}
		prefixes[i] = prefix
		length += int64(len(prefix)) + interval.End - interval.Start + 1
	}
	closing := "\r\n--" + boundary + "--\r\n"
	length += int64(len(closing))
	// Open only the first interval before committing headers. Later streams
	// open sequentially against this same immutable source, never the path.
	reader, sourceSize, err := open(ctx, intervals[0].Start, intervals[0].End-intervals[0].Start+1)
	if err != nil || sourceSize != size {
		if reader != nil {
			_ = reader.Close()
		}
		if err == nil {
			err = errors.New("representation size changed")
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	w.Header().Set("Content-Type", "multipart/byteranges; boundary="+boundary)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Del("Content-Range")
	w.WriteHeader(http.StatusPartialContent)
	for i, interval := range intervals {
		if i > 0 {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("%w: %w", ErrTruncated, err)
			}
			reader, sourceSize, err = open(ctx, interval.Start, interval.End-interval.Start+1)
			if err != nil || sourceSize != size {
				if reader != nil {
					_ = reader.Close()
				}
				if err == nil {
					err = errors.New("representation size changed")
				}
				return fmt.Errorf("%w: %w", ErrTruncated, err)
			}
		}
		if _, err := io.WriteString(w, prefixes[i]); err != nil {
			_ = reader.Close()
			return fmt.Errorf("%w: %w", ErrTruncated, err)
		}
		_, err = io.CopyN(w, ContextReader{Ctx: ctx, Reader: reader}, interval.End-interval.Start+1)
		_ = reader.Close()
		if err != nil {
			// Do not write a closing delimiter after a failed or short read.
			return fmt.Errorf("%w: %w", ErrTruncated, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrTruncated, err)
	}
	_, _ = io.WriteString(w, closing)
	return nil
}

// ContextReader fails reads once ctx is done.
type ContextReader struct {
	Ctx context.Context
	io.Reader
}

func (r ContextReader) Read(p []byte) (int, error) {
	if err := r.Ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// IfRangeMatches reports whether an If-Range validator (strong ETag form)
// permits serving a range for the representation tagged etag.
func IfRangeMatches(value, etag string) bool {
	if value == "" {
		return true
	}
	value = strings.TrimSpace(value)
	return etag != "" && value == etag && !strings.HasPrefix(value, "W/") && !strings.HasPrefix(etag, "W/")
}

// ParseRange parses a single satisfiable Range value, returning inclusive bounds.
func ParseRange(value string, size int64) (int64, int64, bool) {
	if size < 0 || !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 || strings.Contains(value, ",") {
		return 0, 0, false
	}
	start, end := int64(0), size-1
	if parts[0] == "" {
		suffix, err := parseRangeByte(parts[1])
		if err != nil || suffix <= 0 || size == 0 {
			return 0, 0, false
		}
		if suffix < size {
			start = size - suffix
		}
		return start, end, true
	}
	valueStart, err := parseRangeByte(parts[0])
	if err != nil || valueStart >= size {
		return 0, 0, false
	}
	start = valueStart
	if parts[1] != "" {
		valueEnd, err := parseRangeByte(parts[1])
		if err != nil || valueEnd < start {
			return 0, 0, false
		}
		end = valueEnd
		if end >= size {
			end = size - 1
		}
	}
	return start, end, true
}

func parseRangeByte(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("empty range")
	}
	var result int64
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, errors.New("invalid range")
		}
		digit := int64(char - '0')
		if result > ((1<<63-1)-digit)/10 {
			return 0, errors.New("range overflow")
		}
		result = result*10 + digit
	}
	return result, nil
}
