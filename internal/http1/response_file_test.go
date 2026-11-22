// Tests des réponses dont le corps est lu en continu depuis un fichier (V0.2), et de HEAD.
package http1

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestWriteResponseFichier : le corps vient de File, et Content-Length vaut Size.
func TestWriteResponseFichier(t *testing.T) {
	resp := &Response{Status: 200, File: io.NopCloser(strings.NewReader("contenu")), Size: 7}
	var buf bytes.Buffer
	if err := WriteResponse(&buf, resp, dateFixe); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Content-Length: 7\r\n") || !strings.HasSuffix(buf.String(), "\r\n\r\ncontenu") {
		t.Fatalf("obtenu %q", buf.String())
	}
}

// TestWriteResponseFichierTropCourt : si le fichier a rétréci entre le stat et la lecture,
// on a promis plus d'octets que ce qu'on envoie. Il faut une erreur, pour que le serveur coupe la
// connexion : sinon le client attendrait indéfiniment les octets manquants.
func TestWriteResponseFichierTropCourt(t *testing.T) {
	resp := &Response{Status: 200, File: io.NopCloser(strings.NewReader("abc")), Size: 10}
	if err := WriteResponse(io.Discard, resp, dateFixe); err == nil {
		t.Fatal("un fichier plus court que Size doit donner une erreur")
	}
}

// TestWriteResponseOmitBody (HEAD) : mêmes en-têtes, même Content-Length, aucun octet de corps.
func TestWriteResponseOmitBody(t *testing.T) {
	for _, resp := range []*Response{
		{Status: 200, Body: []byte("Hello"), OmitBody: true},
		{Status: 200, File: io.NopCloser(strings.NewReader("Hello")), Size: 5, OmitBody: true},
	} {
		var buf bytes.Buffer
		if err := WriteResponse(&buf, resp, dateFixe); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "Content-Length: 5\r\n") || !strings.HasSuffix(buf.String(), "\r\n\r\n") {
			t.Fatalf("obtenu %q", buf.String())
		}
	}
}
