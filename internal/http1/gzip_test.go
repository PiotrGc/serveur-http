// Tests de la V0.6 côté protocole : Accept-Encoding, encodage chunked écrit à la main, gzip.
package http1

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestAcceptsGzip : lecture de l'en-tête Accept-Encoding, avec ses poids q (RFC 9110 §12.5.3).
func TestAcceptsGzip(t *testing.T) {
	cas := map[string]bool{
		"":                  false,
		"gzip":              true,
		"GZIP":              true, // insensible à la casse
		"deflate, gzip, br": true,
		"gzip;q=0.5":        true,
		"gzip; q=0.5":       true,
		"gzip;q=0":          false, // q=0 veut dire « surtout pas »
		"gzip; q=0.000":     false,
		"br":                false,
		"identity":          false,
		"*":                 true, // « n'importe quel encodage »
		"*;q=0":             false,
		"gzip;q=0, *":       false, // un refus explicite de gzip l'emporte sur *
		"x-gzip":            false,
	}
	for entete, attendu := range cas {
		if got := AcceptsGzip(entete); got != attendu {
			t.Errorf("AcceptsGzip(%q) = %v, attendu %v", entete, got, attendu)
		}
	}
}

// decoupeChunked lit un corps « chunked » OCTET PAR OCTET, en vérifiant le format exact :
// taille en hexadécimal, CRLF, données, CRLF… puis le morceau final « 0\r\n\r\n ».
// Rend les données recollées. Échoue au moindre écart.
func decoupeChunked(t *testing.T, corps string) []byte {
	t.Helper()
	var out []byte
	for {
		ligne, reste, ok := strings.Cut(corps, "\r\n")
		if !ok {
			t.Fatalf("taille de morceau sans CRLF : %q", corps)
		}
		n, err := strconv.ParseUint(ligne, 16, 32)
		if err != nil {
			t.Fatalf("taille de morceau invalide %q", ligne)
		}
		if n == 0 {
			if reste != "\r\n" {
				t.Fatalf("après le morceau final, attendu « \\r\\n », obtenu %q", reste)
			}
			return out
		}
		if uint64(len(reste)) < n+2 || reste[n:n+2] != "\r\n" {
			t.Fatalf("morceau de %d octets mal terminé", n)
		}
		out = append(out, reste[:n]...)
		corps = reste[n+2:]
	}
}

// degzip décompresse des données gzip.
func degzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip invalide : %v", err)
	}
	s, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip tronqué : %v", err)
	}
	return string(s)
}

// TestWriteResponseGzip : en-têtes, format chunked exact, et contenu retrouvé après décompression,
// pour un corps en mémoire comme pour un fichier lu en continu.
func TestWriteResponseGzip(t *testing.T) {
	texte := strings.Repeat("Bonjour le serveur HTTP. ", 2000) // 50 Ko, très compressible
	for _, resp := range []*Response{
		{Status: 200, Body: []byte(texte), Compress: true},
		{Status: 200, File: io.NopCloser(strings.NewReader(texte)), Size: int64(len(texte)), Compress: true},
	} {
		var buf bytes.Buffer
		if err := WriteResponse(&buf, resp, dateFixe); err != nil {
			t.Fatal(err)
		}
		entetes, corps, _ := strings.Cut(buf.String(), "\r\n\r\n")
		if !strings.Contains(entetes, "Content-Encoding: gzip") || !strings.Contains(entetes, "Transfer-Encoding: chunked") {
			t.Fatalf("en-têtes : %q", entetes)
		}
		// Avec chunked, la taille n'est pas connue d'avance : pas de Content-Length (RFC 9112 §6.3).
		if strings.Contains(entetes, "Content-Length") {
			t.Fatalf("Content-Length envoyé avec chunked : %q", entetes)
		}
		compresse := decoupeChunked(t, corps)
		if got := degzip(t, compresse); got != texte {
			t.Fatalf("contenu différent après décompression (%d octets au lieu de %d)", len(got), len(texte))
		}
		if len(compresse) > len(texte)/10 {
			t.Errorf("compression faible : %d octets pour %d", len(compresse), len(texte))
		}
	}
}

// TestWriteResponseGzipLuParClientOfficiel : le lecteur de réponses de net/http comprend notre chunked.
func TestWriteResponseGzipLuParClientOfficiel(t *testing.T) {
	var buf bytes.Buffer
	WriteResponse(&buf, &Response{Status: 200, Body: []byte("abc"), Compress: true, KeepAlive: true}, dateFixe)
	buf.WriteString("SUITE") // les octets d'après ne doivent pas être lus comme faisant partie du corps
	br := bufio.NewReader(&buf)
	// http.ReadResponse retire l'encodage chunked mais ne décompresse pas : on reçoit le gzip brut.
	rep, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	compresse, err := io.ReadAll(rep.Body)
	if err != nil {
		t.Fatalf("corps chunked illisible : %v", err)
	}
	if got := degzip(t, compresse); got != "abc" {
		t.Fatalf("obtenu %q", got)
	}
	if reste, _ := io.ReadAll(br); string(reste) != "SUITE" {
		t.Fatalf("le corps chunked ne s'arrête pas au bon endroit, reste %q", reste)
	}
}

// TestWriteResponseGzipHEAD : HEAD garde les mêmes en-têtes que GET, sans corps.
func TestWriteResponseGzipHEAD(t *testing.T) {
	var buf bytes.Buffer
	WriteResponse(&buf, &Response{Status: 200, Body: []byte("abc"), Compress: true, OmitBody: true}, dateFixe)
	s := buf.String()
	if !strings.Contains(s, "Content-Encoding: gzip\r\n") || !strings.HasSuffix(s, "\r\n\r\n") {
		t.Fatalf("HEAD compressé : %q", s)
	}
}

// TestWriteResponseGzipVide : un corps vide compressé reste un gzip valide (en-tête et fin gzip).
func TestWriteResponseGzipVide(t *testing.T) {
	var buf bytes.Buffer
	WriteResponse(&buf, &Response{Status: 200, Compress: true}, dateFixe)
	_, corps, _ := strings.Cut(buf.String(), "\r\n\r\n")
	if got := degzip(t, decoupeChunked(t, corps)); got != "" {
		t.Fatalf("obtenu %q", got)
	}
}

// TestChunkedWriterIgnoreLeVide : écrire 0 octet ne doit RIEN produire. Un morceau « 0\r\n\r\n »
// au milieu du corps dirait au client « c'est fini » et couperait la réponse.
func TestChunkedWriterIgnoreLeVide(t *testing.T) {
	var buf bytes.Buffer
	if n, err := (chunkedWriter{w: &buf}).Write(nil); n != 0 || err != nil || buf.Len() != 0 {
		t.Fatalf("Write(vide) a produit %q (n=%d, err=%v)", buf.String(), n, err)
	}
}

// TestTransferEncodingDuHandlerIgnore : seul WriteResponse décide du Transfer-Encoding.
// Un « Transfer-Encoding: chunked » posé par un handler sur un corps normal ferait lire au client
// un format qui n'est pas celui envoyé : toute la suite de la connexion serait mal découpée.
func TestTransferEncodingDuHandlerIgnore(t *testing.T) {
	var buf bytes.Buffer
	WriteResponse(&buf, &Response{Status: 200, Headers: map[string]string{"Transfer-Encoding": "chunked"}, Body: []byte("abc")}, dateFixe)
	if s := buf.String(); strings.Contains(s, "Transfer-Encoding") || !strings.Contains(s, "Content-Length: 3\r\n") {
		t.Fatalf("Transfer-Encoding du handler recopié : %q", s)
	}
}
