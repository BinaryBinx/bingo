package middleware

import (
	"strconv"
	"strings"

	"github.com/valyala/fasthttp"
)

const (
	cacheHeaderPoolEntries = 64
	cacheHeaderPoolBytes   = 16 << 10
	cacheHeaderPoolFields  = 256
)

// Header snapshots are scratch space shared across caches, not retained response
// entries. A bounded non-blocking pool caps idle storage at 64 objects, each with
// at most 16 KiB of bytes and 256 field indexes. Oversized snapshots are discarded.
// No RequestCtx, request buffer or hidden fasthttp high-water allocation is kept.
var requestHeaderSnapshots = make(cacheHeaderPool, cacheHeaderPoolEntries)

type cacheHeaderPool chan *cacheRequestHeaders

func (p cacheHeaderPool) get() *cacheRequestHeaders {
	select {
	case snapshot := <-p:
		return snapshot
	default:
		return new(cacheRequestHeaders)
	}
}

func (p cacheHeaderPool) put(snapshot *cacheRequestHeaders) {
	if cap(snapshot.data) > cacheHeaderPoolBytes || cap(snapshot.fields) > cacheHeaderPoolFields {
		return
	}
	snapshot.data = snapshot.data[:0]
	snapshot.fields = snapshot.fields[:0]
	select {
	case p <- snapshot:
	default: // Keep request completion independent of other pool users.
	}
}

// Offsets into one owned byte slice preserve repeated/empty fields and spelling
// without allocating two separate byte slices for every request header. The
// previous field's valueEnd is the next field's start.
type cacheRequestField struct{ keyEnd, valueEnd int }

type cacheRequestHeaders struct {
	data   []byte
	fields []cacheRequestField
}

func (s *cacheRequestHeaders) copyFrom(h *fasthttp.RequestHeader) {
	s.data, s.fields = s.data[:0], s.fields[:0]
	for key, value := range h.All() {
		s.data = append(s.data, key...)
		keyEnd := len(s.data)
		s.data = append(s.data, value...)
		s.fields = append(s.fields, cacheRequestField{keyEnd, len(s.data)})
	}
}

func (s *cacheRequestHeaders) variant(fields []string) string {
	var key strings.Builder
	for _, field := range fields {
		writeCacheVariantFrame(&key, field)
		start := 0
		for _, item := range s.fields {
			if strings.EqualFold(string(s.data[start:item.keyEnd]), field) {
				writeCacheVariantFrame(&key, string(s.data[item.keyEnd:item.valueEnd]))
			}
			start = item.valueEnd
		}
		key.WriteByte(';')
	}
	// Builder owns the key; returning the snapshot to the pool cannot change it.
	return key.String()
}

func writeCacheVariantFrame(key *strings.Builder, value string) {
	key.WriteString(strconv.Itoa(len(value)))
	key.WriteByte(':')
	key.WriteString(value)
}
