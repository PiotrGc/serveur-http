// Test de la V0.6 de bout en bout : le client officiel décompresse nos réponses gzip + chunked,
// sur une connexion keep-alive (le chunked doit dire exactement où finit chaque réponse).
package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

func TestGzipDeBoutEnBout(t *testing.T) {
	texte := strings.Repeat("ligne de cours compressible\n", 5000)
	addr := lancer(t, &Server{Handler: func(r *http1.Request) *http1.Response {
		return &http1.Response{Status: 200, Headers: map[string]string{"Content-Type": "text/plain"},
			Body: []byte(texte + r.Target), Compress: http1.AcceptsGzip(r.Headers["accept-encoding"])}
	}})

	// Le client officiel envoie « Accept-Encoding: gzip » tout seul et décompresse la réponse.
	client := &http.Client{Timeout: 5 * time.Second}
	var reutilisee []bool
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reutilisee = append(reutilisee, i.Reused) }}
	for _, chemin := range []string{"/a", "/b"} {
		req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), "GET", "http://"+addr+chemin, nil)
		rep, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		corps, err := io.ReadAll(rep.Body)
		rep.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		// rep.Uncompressed : le client a bien reçu du gzip et l'a décompressé lui-même.
		if !rep.Uncompressed || string(corps) != texte+chemin {
			t.Fatalf("%s : décompressé %v, %d octets", chemin, rep.Uncompressed, len(corps))
		}
	}
	if len(reutilisee) != 2 || !reutilisee[1] {
		t.Fatalf("la connexion n'a pas été réutilisée après une réponse chunked : %v", reutilisee)
	}
}
