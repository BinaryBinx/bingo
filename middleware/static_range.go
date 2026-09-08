package middleware

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

// WeakStaticETag is an optional metadata validator. File changes must update
// size or modification time. It supports cache validation, not strong If-Match
// or If-Range comparisons. No file-body scan is needed.
func WeakStaticETag(info os.FileInfo) string {
	var scratch [80]byte
	value := append(scratch[:0], "W/\""...)
	value = strconv.AppendInt(value, info.ModTime().UnixNano(), 16)
	value = append(value, '-')
	value = strconv.AppendInt(value, info.Size(), 16)
	value = append(value, '"')
	return string(value)
}

func staticETag(cfg StaticConfig, info os.FileInfo) string {
	if cfg.ETag == nil {
		return ""
	}
	value := cfg.ETag(info)
	if tag, rest := scanStaticETag(value); tag != "" && rest == "" {
		return value
	}
	return "" // Omit malformed tags, including header injection from a callback.
}

// Quoted opaque tags may contain commas. Backslash is not an escape.
func scanStaticETag(value string) (tag, rest string) {
	start := 0
	if strings.HasPrefix(value, "W/") {
		start = 2
	}
	if len(value) <= start || value[start] != '"' {
		return "", value
	}
	for i := start + 1; i < len(value); i++ {
		if value[i] == '"' {
			return value[:i+1], value[i+1:]
		}
		if value[i] < 0x21 || value[i] == 0x7f {
			return "", value
		}
	}
	return "", value
}

func staticTagMatch(tag, current string, strong bool) bool {
	if tag == "" || current == "" {
		return false
	}
	if strong {
		return !strings.HasPrefix(tag, "W/") && tag == current
	}
	return strings.TrimPrefix(tag, "W/") == strings.TrimPrefix(current, "W/")
}

func staticETagListMatches(values [][]byte, current string, strong bool) bool {
	if staticWildcard(values) {
		return true
	}
	matched := false
	for _, line := range values {
		remaining := strings.TrimSpace(string(line))
		for remaining != "" {
			if remaining[0] == ',' {
				remaining = strings.TrimSpace(remaining[1:])
				continue
			}
			tag, rest := scanStaticETag(remaining)
			if tag == "" {
				return false
			}
			matched = matched || staticTagMatch(tag, current, strong)
			remaining = strings.TrimSpace(rest)
			if remaining != "" {
				if remaining[0] != ',' {
					return false
				}
				remaining = strings.TrimSpace(remaining[1:])
			}
		}
	}
	return matched
}

type staticByteRange struct{ start, length int64 }

// Select one byte range. Multiple/repeated/invalid ranges and unknown units are
// ignored, yielding a full response rather than allocating multipart output.
// Only valid but unsatisfiable ranges return 416. Preconditions run first.
func selectStaticRange(ctx *fasthttp.RequestCtx, cfg StaticConfig, modified time.Time, size int64, etag string) (staticByteRange, bool) {
	var selected staticByteRange
	if cfg.DisableRange {
		return selected, false
	}
	values := ctx.Request.Header.PeekAll("Range")
	if len(values) != 1 {
		return selected, false
	}
	// PeekAll reuses its result slice on the next lookup. Keep the field bytes
	// before looking up If-Range so that its value cannot replace Range here.
	value := bytes.TrimSpace(values[0])
	if conditions := ctx.Request.Header.PeekAll("If-Range"); len(conditions) > 0 {
		if len(conditions) != 1 {
			return selected, false
		}
		condition := strings.TrimSpace(string(conditions[0]))
		if tag, rest := scanStaticETag(condition); tag != "" && rest == "" {
			if !staticTagMatch(tag, etag, true) {
				return selected, false
			}
		} else {
			date, err := http.ParseTime(condition)
			// Match Last-Modified exactly, as net/http's file server does.
			if err != nil || modified.IsZero() || !date.Equal(modified.Truncate(time.Second)) {
				return selected, false
			}
		}
	}
	unit, span, ok := bytes.Cut(value, []byte("="))
	if !ok || !bytes.EqualFold(unit, []byte("bytes")) || bytes.IndexByte(span, ',') >= 0 {
		return selected, false
	}
	first, last, ok := bytes.Cut(bytes.TrimSpace(span), []byte("-"))
	if !ok {
		return selected, false
	}
	if len(first) == 0 {
		length, valid := staticRangeNumber(last)
		if !valid {
			return selected, false
		}
		selected.length = min(length, size)
		selected.start = size - selected.length
	} else {
		start, valid := staticRangeNumber(first)
		if !valid {
			return selected, false
		}
		end := size - 1
		if len(last) > 0 {
			var valid bool
			end, valid = staticRangeNumber(last)
			if !valid || end < start {
				return selected, false
			}
			end = min(end, size-1)
		}
		selected = staticByteRange{start, max(int64(0), end-start+1)}
	}
	if selected.length == 0 || size == 0 {
		staticPreconditionResponse(ctx, fasthttp.StatusRequestedRangeNotSatisfiable)
		ctx.Response.Header.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		return selected, true
	}
	ctx.SetStatusCode(fasthttp.StatusPartialContent)
	ctx.Response.Header.Set("Content-Range", "bytes "+strconv.FormatInt(selected.start, 10)+"-"+strconv.FormatInt(selected.start+selected.length-1, 10)+"/"+strconv.FormatInt(size, 10))
	return selected, true
}

// Saturate valid large numbers: a huge end/suffix covers the available bytes,
// while a huge first offset is unsatisfiable. Never overflow.
func staticRangeNumber(value []byte) (int64, bool) {
	if len(value) == 0 {
		return 0, false
	}
	const limit = int64(1<<63 - 1)
	var result int64
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		if result > (limit-int64(digit-'0'))/10 {
			result = limit
		} else {
			result = result*10 + int64(digit-'0')
		}
	}
	return result, true
}

// A Content-Length alone does not limit fasthttp's stream copy. This reader
// bounds bytes on every path, including Response.Body, and owns the descriptor.
type staticLimitedFile struct {
	*io.LimitedReader
	file *os.File
}

func (r *staticLimitedFile) Close() error { return r.file.Close() }

// Keep the limited *os.File visible to TCP ReaderFrom for sendfile. fasthttp's
// BodyWriterTo contract avoids its generic copy buffer for this owned wrapper.
func (r *staticLimitedFile) SupportsBodyWriteTo() bool { return true }
func (r *staticLimitedFile) WriteTo(w io.Writer) (int64, error) {
	if flusher, ok := w.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return 0, err
		}
	}
	return io.Copy(w, r.LimitedReader)
}

func streamStaticRange(ctx *fasthttp.RequestCtx, file *os.File, selected staticByteRange) {
	if _, err := file.Seek(selected.start, io.SeekStart); err != nil {
		file.Close()
		resetErrorResponse(ctx, fasthttp.StatusInternalServerError, "Internal Server Error")
		return
	}
	length := -1 // chunked fallback for ranges larger than int on 32-bit systems
	if uint64(selected.length) <= uint64(^uint(0)>>1) {
		length = int(selected.length)
	}
	ctx.SetBodyStream(&staticLimitedFile{&io.LimitedReader{R: file, N: selected.length}, file}, length)
}
