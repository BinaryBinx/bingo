package responsemeta

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestResetHTTPErrorHeadersDropsMixedCaseMetadataAndGoTrailers(t *testing.T) {
	h := http.Header{
		"cOnTeNt-EnCoDiNg": {"gzip"}, "content-digest": {"old"}, "sIgNaTuRe": {"old"},
		"content-type": {"old"}, "Content-Length": {"100"}, "eTaG": {`"old"`},
		"cache-control": {"public, max-age=3600"}, "Expires": {"old"}, "Age": {"100"},
		"tRaIlEr": {"X-Checksum, X-Second", "X-Third"}, "x-checksum": {"old"},
		"X-Second": {"old"}, "X-Third": {"old"}, http.TrailerPrefix + "X-Late": {"old"},
		"Set-Cookie": {"private"}, "X-Request-ID": {"request-1"},
		"Access-Control-Allow-Origin": {"https://client.test"}, "WWW-Authenticate": {"Bearer"},
		"Retry-After": {"3"}, "Sec-WebSocket-Version": {"13"},
	}
	ResetHTTPErrorHeaders(h)
	want := http.Header{
		"Cache-Control": {"no-store"}, "X-Request-ID": {"request-1"},
		"Access-Control-Allow-Origin": {"https://client.test"}, "WWW-Authenticate": {"Bearer"},
		"Retry-After": {"3"}, "Sec-WebSocket-Version": {"13"},
	}
	if len(h) != len(want) {
		t.Fatalf("retained stale headers: %v", h)
	}
	for name, values := range want {
		if got := h[name]; len(got) != 1 || got[0] != values[0] {
			t.Errorf("lost %s: %v", name, got)
		}
	}
}

func TestResetErrorRemovesRepresentationMetadataAndTrailers(t *testing.T) {
	for _, normalize := range []bool{true, false} {
		var c fasthttp.RequestCtx
		if !normalize {
			c.Response.Header.DisableNormalizing()
		}
		fields := []string{"Content-Digest", "Repr-Digest", "Digest", "Content-MD5", "Signature", "Signature-Input", "ETag", "Content-Location"}
		for _, name := range fields {
			if !normalize {
				name = strings.ToLower(name)
			}
			c.Response.Header.Set(name, "old")
		}
		c.Response.Header.AddTrailer("X-Checksum")
		c.Response.Header.Set("X-Checksum", "old")
		c.Response.Header.Set("X-Request-ID", "request-1")
		c.SetBodyString("signed old body")
		ResetError(&c, 500, "Internal Server Error")
		for key, _ := range c.Response.Header.All() {
			for _, name := range append(fields, "X-Checksum", "Trailer") {
				if strings.EqualFold(string(key), name) {
					t.Errorf("retained %s", key)
				}
			}
		}
		if len(c.Response.Header.PeekTrailerKeys()) != 0 {
			t.Fatal("retained trailer declaration")
		}
		if string(c.Response.Header.Peek("X-Request-ID")) != "request-1" || string(c.Response.Body()) != "Internal Server Error" {
			t.Fatal("lost public headers/body")
		}
		c.Response.Reset()
	}
}

func TestTimeoutSnapshotNeverAliasesWorkerHeaders(t *testing.T) {
	var c fasthttp.RequestCtx
	var snapshot TimeoutHeaders
	TrackTimeoutHeaders(&c, &snapshot)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			c.Response.Header.Set("X-Request-ID", "worker-request")
			c.Response.Header.Set("Set-Cookie", "private")
			PublishTimeoutHeaders(&c)
		}
	})
	var response fasthttp.Response
	snapshot.Apply(&response.Header)
	wg.Wait()
	before := string(response.Header.Header())
	c.Response.Header.Set("X-Request-ID", "late")
	PublishTimeoutHeaders(&c)
	if string(response.Header.Header()) != before {
		t.Fatal("late publication changed response")
	}
	if len(response.Header.Peek("Set-Cookie")) != 0 {
		t.Fatal("copied private metadata")
	}
}
