package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"sync"
)

// jsonBuf pairs a buffer with an encoder bound to it, so neither is
// allocated per response.
type jsonBuf struct {
	buf bytes.Buffer
	enc *json.Encoder
}

// bufPool reuses response buffers. Rules that keep sync.Pool safe:
//   - pool pointers, not values, so Get/Put don't allocate;
//   - Reset before reuse, and keep no reference after Put;
//   - drop oversized buffers, or one huge response pins its memory forever.
var bufPool = sync.Pool{New: func() any {
	b := new(jsonBuf)
	b.enc = json.NewEncoder(&b.buf)
	b.enc.SetEscapeHTML(false)
	return b
}}

const maxPooledBuffer = 64 << 10

// Shared header values: net/http never mutates header value slices, and with
// cap 1 an Add elsewhere copies instead of writing into them. Saves one
// allocation per header per response. Never assign through h[k][0] = v.
var (
	jsonContentType = []string{"application/json"}
	noSniff         = []string{"nosniff"}
)

// WriteJSON encodes v into a pooled buffer first, so encoding errors become a
// clean 500 instead of a half-written 200, and Content-Length can be set.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	b := bufPool.Get().(*jsonBuf) // the pool only ever holds *jsonBuf
	b.buf.Reset()
	defer func() {
		if b.buf.Cap() <= maxPooledBuffer {
			bufPool.Put(b)
		}
	}()

	if err := b.enc.Encode(v); err != nil {
		slog.ErrorContext(r.Context(), "encode response", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h["Content-Type"] = jsonContentType
	h["X-Content-Type-Options"] = noSniff // stop browsers sniffing JSON as HTML
	h.Set("Content-Length", strconv.Itoa(b.buf.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(b.buf.Bytes()) // the client may be gone; nothing useful to do
}

// DecodeJSON reads exactly one JSON value into dst and answers the client
// itself on failure (415, 413, 400), returning false. Unknown fields are
// rejected so typos in client payloads fail loudly instead of being ignored.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "malformed JSON body", http.StatusBadRequest)
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "body must contain a single JSON value", http.StatusBadRequest)
		return false
	}
	return true
}
