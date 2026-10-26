package server

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A stream that clears its deadlines survives the server's WriteTimeout,
// including behind the statusRecorder middleware.
func TestClearStreamDeadlinesOutlivesWriteTimeout(t *testing.T) {
	const writeTimeout = 200 * time.Millisecond

	handler := func(clear bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w = &statusRecorder{ResponseWriter: w}
			if clear {
				clearStreamDeadlines(w)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			f := w.(http.Flusher)
			f.Flush()
			time.Sleep(3 * writeTimeout)
			_, _ = w.Write([]byte("data: late\n\n"))
			f.Flush()
		})
	}

	late := func(clear bool) bool {
		srv := httptest.NewUnstartedServer(handler(clear))
		srv.Config.WriteTimeout = writeTimeout
		srv.Start()
		defer srv.Close()

		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		return strings.HasPrefix(line, "data: late")
	}

	if late(false) {
		t.Fatal("control: the write timeout should have cut the stream")
	}
	if !late(true) {
		t.Fatal("stream was cut by the write timeout despite clearStreamDeadlines")
	}
}
