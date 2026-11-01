package export_test

import (
	"compress/gzip"
	"io"
	"net/http"
)

// gzipReader decompresses a request body when it says it is compressed.
func gzipReader(r *http.Request) (io.Reader, error) {
	if r.Header.Get("Content-Encoding") != "gzip" {
		return r.Body, nil
	}
	return gzip.NewReader(r.Body)
}
