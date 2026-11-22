// Tests du serveur liés à la V0.2 : HEAD et fermeture des fichiers ouverts par le handler.
package server

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// fichierEspion compte ses fermetures, pour vérifier que le serveur ferme bien les fichiers.
// Un fichier jamais fermé garde un descripteur ouvert : après quelques milliers de requêtes,
// le système refuse d'en ouvrir d'autres.
type fichierEspion struct {
	*strings.Reader
	fermetures *atomic.Int32 // atomic : le compteur est lu par le test et écrit par la goroutine du serveur
}

func (f fichierEspion) Close() error { f.fermetures.Add(1); return nil }

func demarrerAvec(t *testing.T, h Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go (&Server{Handler: h}).Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// TestHeadSansCorps : HEAD reçoit les mêmes en-têtes que GET, sans le corps, et le fichier est fermé.
func TestHeadSansCorps(t *testing.T) {
	var fermetures atomic.Int32
	addr := demarrerAvec(t, func(*http1.Request) *http1.Response {
		return &http1.Response{Status: 200, File: fichierEspion{strings.NewReader("Hello"), &fermetures}, Size: 5}
	})

	get := echanger(t, addr, "GET / HTTP/1.1\r\nHost: a\r\n\r\n")
	head := echanger(t, addr, "HEAD / HTTP/1.1\r\nHost: a\r\n\r\n")
	if !strings.HasSuffix(get, "\r\n\r\nHello") {
		t.Fatalf("GET : %q", get)
	}
	if !strings.Contains(head, "Content-Length: 5\r\n") || !strings.HasSuffix(head, "\r\n\r\n") {
		t.Fatalf("HEAD : %q", head)
	}
	if n := fermetures.Load(); n != 2 {
		t.Fatalf("%d fichier(s) fermé(s) sur 2", n)
	}
}
