// Tests de l'écriture des réponses (V0.1) : on compare les octets exacts envoyés au client.
package http1

import (
	"bytes"
	"testing"
	"time"
)

// dateFixe : une date connue, pour que l'en-tête Date soit prévisible dans les tests.
var dateFixe = time.Date(2026, 11, 22, 15, 4, 5, 0, time.UTC)

// TestWriteResponseOctets : la réponse complète, octet par octet.
// Les en-têtes sortent triés par nom, pour que la sortie soit toujours la même.
func TestWriteResponseOctets(t *testing.T) {
	resp := &Response{
		Status:  200,
		Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		Body:    []byte("Hello"),
	}
	var buf bytes.Buffer
	if err := WriteResponse(&buf, resp, dateFixe); err != nil {
		t.Fatal(err)
	}
	attendu := "HTTP/1.1 200 OK\r\n" +
		"Connection: close\r\n" +
		"Content-Length: 5\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Date: Sun, 22 Nov 2026 15:04:05 GMT\r\n" +
		"Server: serveur-http\r\n" +
		"\r\n" +
		"Hello"
	if buf.String() != attendu {
		t.Fatalf("obtenu :\n%q\nattendu :\n%q", buf.String(), attendu)
	}
}

// TestWriteResponseDateEnGMT : une heure locale (Paris) est convertie en GMT.
func TestWriteResponseDateEnGMT(t *testing.T) {
	paris := time.FixedZone("CET", 3600)
	var buf bytes.Buffer
	_ = WriteResponse(&buf, &Response{Status: 200}, time.Date(2026, 11, 22, 16, 4, 5, 0, paris))
	if !bytes.Contains(buf.Bytes(), []byte("Date: Sun, 22 Nov 2026 15:04:05 GMT\r\n")) {
		t.Fatalf("date non convertie en GMT :\n%s", buf.String())
	}
}

// TestWriteResponseIgnoreLesEnTetesAuto : un Content-Length faux fourni par l'appelant
// est remplacé par la vraie taille du corps. Un Content-Length faux ferait lire trop ou trop peu au client.
func TestWriteResponseIgnoreLesEnTetesAuto(t *testing.T) {
	resp := &Response{Status: 200, Headers: map[string]string{"content-length": "999"}, Body: []byte("abc")}
	var buf bytes.Buffer
	_ = WriteResponse(&buf, resp, dateFixe)
	if bytes.Contains(buf.Bytes(), []byte("999")) || !bytes.Contains(buf.Bytes(), []byte("Content-Length: 3\r\n")) {
		t.Fatalf("Content-Length mal géré :\n%s", buf.String())
	}
}

// TestWriteResponseRefuseInjection : une valeur d'en-tête avec CR ou LF permettrait d'injecter
// de faux en-têtes (« response splitting »). On refuse d'écrire la réponse.
func TestWriteResponseRefuseInjection(t *testing.T) {
	resp := &Response{Status: 200, Headers: map[string]string{"X-Nom": "a\r\nSet-Cookie: pirate=1"}}
	var buf bytes.Buffer
	if err := WriteResponse(&buf, resp, dateFixe); err == nil {
		t.Fatal("une valeur avec CRLF doit être refusée")
	}
	if buf.Len() != 0 {
		t.Fatalf("rien ne doit être écrit, obtenu %q", buf.String())
	}
}

// TestStatusText : les codes utilisés par le serveur ont leur texte standard (RFC 9110 §15).
func TestStatusText(t *testing.T) {
	cas := map[int]string{200: "OK", 400: "Bad Request", 414: "URI Too Long",
		431: "Request Header Fields Too Large", 500: "Internal Server Error",
		501: "Not Implemented", 505: "HTTP Version Not Supported"}
	for code, texte := range cas {
		if got := StatusText(code); got != texte {
			t.Errorf("StatusText(%d) = %q, attendu %q", code, got, texte)
		}
	}
}
