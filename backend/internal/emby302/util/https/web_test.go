package https

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyPassReturnsResponseCopyErrors(t *testing.T) {
	writeErr := errors.New("client write failed")
	for _, tt := range []struct {
		name     string
		writeErr error
		wantErr  error
	}{
		{name: "upstream_read", wantErr: io.ErrUnexpectedEOF},
		{name: "client_write", writeErr: writeErr, wantErr: writeErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "10")
				_, _ = io.WriteString(w, "part")
			}))
			defer origin.Close()
			var writer http.ResponseWriter = httptest.NewRecorder()
			if tt.writeErr != nil {
				writer = proxyFailingWriter{ResponseWriter: writer, err: tt.writeErr}
			}
			err := ProxyPass(httptest.NewRequest(http.MethodGet, "/stream", nil), writer, origin.URL)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("copy error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

type proxyFailingWriter struct {
	http.ResponseWriter
	err error
}

func (w proxyFailingWriter) Write([]byte) (int, error) {
	return 0, w.err
}
