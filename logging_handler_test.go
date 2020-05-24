package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLoggingHandler_ServeHTTP(t *testing.T) {
	datestr := time.Now().Format(`02/Jan/2006:15:04`)

	tests := []struct {
		Format,
		Expected string
	}{
		{defaultRequestLoggingFormat,
			`127.0.0.1 - - \[` + datestr + `:\d{2} [+-]\d{4}\] test-server GET - "/foo/bar" HTTP/1.1 "" 200 4 0.\d{3}` + "\n"},
		{"{{.RequestMethod}}", "GET\n"},
	}

	for _, test := range tests {
		buf := bytes.NewBuffer(nil)
		handler := func(w http.ResponseWriter, req *http.Request) {
			_, ok := w.(http.Hijacker)
			if !ok {
				t.Error("http.Hijacker is not available")
			}

			w.Write([]byte("test"))
		}

		h := LoggingHandler(buf, http.HandlerFunc(handler), true, "", test.Format)
		r, _ := http.NewRequest("GET", "/foo/bar", nil)
		r.RemoteAddr = "127.0.0.1"
		r.Host = "test-server"

		h.ServeHTTP(httptest.NewRecorder(), r)

		re := regexp.MustCompile(test.Expected)
		assert.Regexp(t, re, buf.String())
	}
}
