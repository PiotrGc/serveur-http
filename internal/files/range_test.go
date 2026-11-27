// Tests de la V0.5 côté fichiers : Range (206, 416), Accept-Ranges, Last-Modified, 304, If-Range.
package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// dateFichier : la date de modification posée sur le fichier de test.
var dateFichier = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

const lastModified = "Fri, 20 Nov 2026 10:00:00 GMT"

// preparerVideo crée « clip.mp4 » de 1000 octets (0123456789 répété) daté de dateFichier.
func preparerVideo(t *testing.T) func(*http1.Request) *http1.Response {
	t.Helper()
	dir := preparer(t)
	p := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(p, []byte(strings.Repeat("0123456789", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, dateFichier, dateFichier); err != nil {
		t.Fatal(err)
	}
	return ouvrir(t, dir)
}

// avecEntetes appelle le handler avec des en-têtes (noms en minuscules, comme après le parser).
func avecEntetes(t *testing.T, h func(*http1.Request) *http1.Response, method, target string, headers map[string]string) (*http1.Response, string) {
	t.Helper()
	headers["host"] = "a"
	resp := h(&http1.Request{Method: method, Target: target, Proto: "HTTP/1.1", Headers: headers})
	corps := string(resp.Body)
	if resp.File != nil {
		b := make([]byte, resp.Size)
		n, _ := resp.File.Read(b)
		for n < len(b) { // Read peut rendre moins que demandé : on continue jusqu'à Size
			m, err := resp.File.Read(b[n:])
			if err != nil {
				break
			}
			n += m
		}
		resp.File.Close()
		corps = string(b[:n])
	}
	return resp, corps
}

// TestRange : 206 avec Content-Range et seulement les octets demandés.
func TestRange(t *testing.T) {
	h := preparerVideo(t)
	cas := []struct{ rng, contentRange, corps string }{
		{"bytes=0-9", "bytes 0-9/1000", "0123456789"},
		{"bytes=995-", "bytes 995-999/1000", "56789"},
		{"bytes=-3", "bytes 997-999/1000", "789"},
		{"bytes=12-14", "bytes 12-14/1000", "234"},
	}
	for _, c := range cas {
		resp, corps := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"range": c.rng})
		if resp.Status != 206 || resp.Headers["Content-Range"] != c.contentRange || corps != c.corps || resp.Size != int64(len(c.corps)) {
			t.Errorf("%s : statut %d, Content-Range %q, corps %q, Size %d", c.rng, resp.Status, resp.Headers["Content-Range"], corps, resp.Size)
		}
	}
}

// TestRangeImpossible : 416 avec « Content-Range: bytes */taille » (RFC 9110 §15.5.17).
func TestRangeImpossible(t *testing.T) {
	h := preparerVideo(t)
	resp, _ := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"range": "bytes=5000-"})
	if resp.Status != 416 || resp.Headers["Content-Range"] != "bytes */1000" {
		t.Fatalf("statut %d, Content-Range %q", resp.Status, resp.Headers["Content-Range"])
	}
	if resp.File != nil {
		t.Fatal("une 416 ne doit pas garder le fichier ouvert")
	}
}

// TestRangeIgnore : une plage mal écrite ou multiple donne le fichier entier en 200.
func TestRangeIgnore(t *testing.T) {
	h := preparerVideo(t)
	for _, rng := range []string{"bytes=abc", "bytes=0-1,5-6", "bytes=5-2"} {
		resp, corps := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"range": rng})
		if resp.Status != 200 || len(corps) != 1000 {
			t.Errorf("%s : statut %d, %d octets", rng, resp.Status, len(corps))
		}
	}
}

// TestEntetesDeFichier : Accept-Ranges et Last-Modified sur chaque fichier servi.
func TestEntetesDeFichier(t *testing.T) {
	h := preparerVideo(t)
	resp, _ := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{})
	if resp.Headers["Accept-Ranges"] != "bytes" || resp.Headers["Last-Modified"] != lastModified {
		t.Fatalf("Accept-Ranges %q, Last-Modified %q", resp.Headers["Accept-Ranges"], resp.Headers["Last-Modified"])
	}
}

// TestIfModifiedSince : 304 si le fichier n'a pas changé depuis la date donnée, sinon 200.
func TestIfModifiedSince(t *testing.T) {
	h := preparerVideo(t)
	cas := map[string]int{
		lastModified:                    304, // même date
		"Sat, 21 Nov 2026 10:00:00 GMT": 304, // le client a une copie plus récente que le fichier
		"Thu, 19 Nov 2026 10:00:00 GMT": 200, // le fichier a changé depuis
		"pas une date":                  200, // date invalide : ignorée
	}
	for ims, attendu := range cas {
		resp, _ := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"if-modified-since": ims})
		if resp.Status != attendu {
			t.Errorf("If-Modified-Since %q : statut %d, attendu %d", ims, resp.Status, attendu)
		}
		if resp.Status == 304 && (resp.File != nil || resp.Headers["Last-Modified"] != lastModified) {
			t.Errorf("304 : fichier ouvert ou Last-Modified absent")
		}
	}
}

// TestIfRange : la plage n'est servie que si le fichier n'a pas changé depuis la date donnée.
// Sinon, le client recollerait un morceau du nouveau fichier à la fin de l'ancien.
func TestIfRange(t *testing.T) {
	h := preparerVideo(t)
	resp, _ := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"range": "bytes=0-9", "if-range": lastModified})
	if resp.Status != 206 {
		t.Errorf("If-Range identique : statut %d, attendu 206", resp.Status)
	}
	resp, corps := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"range": "bytes=0-9", "if-range": "Thu, 19 Nov 2026 10:00:00 GMT"})
	if resp.Status != 200 || len(corps) != 1000 {
		t.Errorf("If-Range différent : statut %d, %d octets, attendu 200 et le fichier entier", resp.Status, len(corps))
	}
}

// TestRangeSurListing : Range ne s'applique qu'aux fichiers, pas à la page de listing.
func TestRangeSurListing(t *testing.T) {
	h := preparerVideo(t)
	if resp, _ := avecEntetes(t, h, "GET", "/", map[string]string{"range": "bytes=0-9"}); resp.Status != 200 {
		t.Fatalf("listing avec Range : statut %d", resp.Status)
	}
}

// TestRangeIgnoreSurHEAD : la RFC 9110 §14.2 impose d'ignorer Range pour toute méthode autre que GET.
// HEAD rend donc les en-têtes du fichier entier (200, Content-Length complet).
func TestRangeIgnoreSurHEAD(t *testing.T) {
	h := preparerVideo(t)
	resp, _ := avecEntetes(t, h, "HEAD", "/clip.mp4", map[string]string{"range": "bytes=0-9"})
	if resp.Status != 200 || resp.Size != 1000 {
		t.Fatalf("HEAD avec Range : statut %d, Size %d, attendu 200 et 1000", resp.Status, resp.Size)
	}
}

// TestIfModifiedSinceAvecNanosecondes : un vrai fichier a une date à la nanoseconde près, alors que
// les dates HTTP sont à la seconde. Sans troncature, le fichier paraîtrait toujours « modifié »
// après la date renvoyée par le navigateur, et la 304 ne marcherait jamais.
func TestIfModifiedSinceAvecNanosecondes(t *testing.T) {
	dir := preparer(t)
	p := filepath.Join(dir, "clip.mp4")
	os.WriteFile(p, []byte("x"), 0o644)
	avecNs := dateFichier.Add(123456789 * time.Nanosecond)
	if err := os.Chtimes(p, avecNs, avecNs); err != nil {
		t.Fatal(err)
	}
	h := ouvrir(t, dir)
	// Le navigateur renvoie exactement le Last-Modified qu'il a reçu (à la seconde).
	resp, _ := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{})
	resp2, _ := avecEntetes(t, h, "GET", "/clip.mp4", map[string]string{"if-modified-since": resp.Headers["Last-Modified"]})
	if resp2.Status != 304 {
		t.Fatalf("statut %d, attendu 304 (date du fichier avec des nanosecondes)", resp2.Status)
	}
}
