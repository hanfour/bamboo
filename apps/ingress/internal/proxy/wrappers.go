// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

// recorder captures status and bytes out, and counts writes as activity.
// status is written only on the handler goroutine (ReverseProxy writes the
// response there); n is atomic because report reads it after the
// transport goroutine may have run.
type recorder struct {
	http.ResponseWriter
	status int
	n      atomic.Int64
	idle   *idleTimer
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.n.Add(int64(n))
	if r.idle != nil {
		r.idle.Touch()
	}
	return n, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// countingBody counts bytes in, marks activity, and remembers when the
// body cap tripped (the transport may wrap the MaxBytesError).
type countingBody struct {
	rc       io.ReadCloser
	n        atomic.Int64
	tooLarge atomic.Bool
	idle     *idleTimer
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.n.Add(int64(n))
	if n > 0 {
		b.idle.Touch()
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		b.tooLarge.Store(true)
	}
	return n, err
}

func (b *countingBody) Close() error { return b.rc.Close() }
