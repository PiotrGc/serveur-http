// Test de la réponse 304 (V0.5) : jamais de corps, et pas de Content-Length: 0 trompeur.
package http1

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteResponse304(t *testing.T) {
	resp := &Response{Status: 304, Headers: map[string]string{"Last-Modified": "Sun, 22 Nov 2026 15:04:05 GMT"}, Body: []byte("ne doit pas partir")}
	var buf bytes.Buffer
	if err := WriteResponse(&buf, resp, dateFixe); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	// RFC 9110 §15.4.5 : une 304 n'a jamais de corps. Un Content-Length: 0 dirait au client
	// que le fichier fait 0 octet, ce qui est faux : on n'en envoie pas.
	if !strings.HasPrefix(s, "HTTP/1.1 304 Not Modified\r\n") || strings.Contains(s, "Content-Length") || !strings.HasSuffix(s, "\r\n\r\n") {
		t.Fatalf("304 mal formée : %q", s)
	}
}
