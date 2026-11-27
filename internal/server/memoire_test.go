// Test de la V0.5 : un très gros fichier passe sans remplir la mémoire du serveur.
package server

import (
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// lecteurZeros : un « fichier » de zéros de la taille voulue, sans rien stocker.
type lecteurZeros struct{ reste int64 }

func (l *lecteurZeros) Read(p []byte) (int, error) {
	if l.reste == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > l.reste {
		n = l.reste
	}
	clear(p[:n])
	l.reste -= n
	return int(n), nil
}

func (l *lecteurZeros) Close() error { return nil }

// TestGrosFichierMemoireBornee : 256 Mio envoyés, et la mémoire du tas reste sous 50 Mio.
// Si le serveur chargeait le fichier en entier, on verrait au moins 256 Mio.
func TestGrosFichierMemoireBornee(t *testing.T) {
	if testing.Short() {
		t.Skip("test long, sauté avec -short")
	}
	const taille = 256 << 20
	addr := lancer(t, &Server{Handler: func(*http1.Request) *http1.Response {
		return &http1.Response{Status: 200, File: &lecteurZeros{reste: taille}, Size: taille}
	}})

	// Une goroutine mesure le tas toutes les 20 ms et garde le maximum.
	var maxTas atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > maxTas.Load() {
					maxTas.Store(m.HeapInuse)
				}
			}
		}
	}()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(60 * time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: a\r\nConnection: close\r\n\r\n")
	n, err := io.Copy(io.Discard, conn) // le client lit et jette au fur et à mesure
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	if n < taille {
		t.Fatalf("seulement %d octets reçus sur %d", n, taille)
	}
	if m := maxTas.Load(); m > 50<<20 {
		t.Fatalf("tas au maximum à %d Mio pendant l'envoi : le fichier est chargé en mémoire", m>>20)
	}
	t.Logf("256 Mio envoyés, tas maximum : %d Mio", maxTas.Load()>>20)
}
