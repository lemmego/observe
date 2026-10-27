package provider

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// Flush forwards to the wrapped writer.
//
// Through an http.ResponseController built on the *wrapped* writer rather
// than on the recorder: building one on the recorder would follow Unwrap
// straight back into these same methods and recurse.
func (w *recordingWriter) Flush() {
	//nolint:errcheck // a writer that cannot flush is not this wrapper's failure.
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack forwards to the wrapped writer, so a WebSocket upgrade works through
// this middleware.
func (w *recordingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, errors.New("observe: the response writer cannot be hijacked: " + err.Error())
	}
	// Hijacked: the status this wrapper would report is meaningless from here
	// on, and recording a 200 for a connection that became a WebSocket would
	// be worse than recording nothing.
	w.wroteHeader = true
	return conn, buffered, nil
}
