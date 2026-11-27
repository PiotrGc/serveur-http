// Tests de la V0.6 côté fichiers : quand compresser, et quand ne pas le faire.
package files

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// demanderGzip appelle le handler avec un Accept-Encoding et un protocole donnés.
func demanderGzip(t *testing.T, h func(*http1.Request) *http1.Response, proto, target string, headers map[string]string) *http1.Response {
	t.Helper()
	headers["host"] = "a"
	resp := h(&http1.Request{Method: "GET", Target: target, Proto: proto, Headers: headers})
	if resp.File != nil {
		resp.File.Close()
	}
	return resp
}

func TestQuandCompresser(t *testing.T) {
	dir := preparer(t)
	os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("pas du texte"), 0o644)
	h := ouvrir(t, dir)
	gz := func() map[string]string { return map[string]string{"accept-encoding": "gzip, deflate"} }

	cas := []struct {
		nom, proto, cible string
		headers           map[string]string
		compresse, vary   bool
	}{
		{"texte + gzip accepté", "HTTP/1.1", "/a.txt", gz(), true, true},
		{"HTML + gzip accepté", "HTTP/1.1", "/page.html", gz(), true, true},
		{"page de listing + gzip accepté", "HTTP/1.1", "/", gz(), true, true},
		{"texte sans Accept-Encoding", "HTTP/1.1", "/a.txt", map[string]string{}, false, true},
		{"PDF : déjà compressé", "HTTP/1.1", "/cours/td1.pdf", gz(), false, false},
		{"vidéo : déjà compressée", "HTTP/1.1", "/video.mp4", gz(), false, false},
		// Le chunked n'existe pas en HTTP/1.0 : sans lui, on ne peut pas envoyer une taille inconnue.
		{"HTTP/1.0", "HTTP/1.0", "/a.txt", gz(), false, true},
	}
	for _, c := range cas {
		resp := demanderGzip(t, h, c.proto, c.cible, c.headers)
		if resp.Compress != c.compresse {
			t.Errorf("%s : Compress = %v, attendu %v", c.nom, resp.Compress, c.compresse)
		}
		// Vary dit aux caches que la réponse dépend d'Accept-Encoding : sans lui, un cache pourrait
		// servir la version gzip à un client qui ne sait pas la lire.
		if got := resp.Headers["Vary"] == "Accept-Encoding"; got != c.vary {
			t.Errorf("%s : Vary présent = %v, attendu %v", c.nom, got, c.vary)
		}
	}
}

// TestPasDeGzipSurRange : un morceau (206) n'est jamais compressé. Les positions de Range
// désignent des octets du fichier original : les compresser n'aurait plus de sens.
func TestPasDeGzipSurRange(t *testing.T) {
	h := ouvrir(t, preparer(t))
	resp := demanderGzip(t, h, "HTTP/1.1", "/a.txt", map[string]string{"accept-encoding": "gzip", "range": "bytes=0-2"})
	if resp.Status != 206 || resp.Compress {
		t.Fatalf("statut %d, Compress %v : attendu 206 non compressé", resp.Status, resp.Compress)
	}
}
