// Test de la V0.7 côté fichiers : pas de script dans les pages HTML et SVG partagées.
package files

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSandboxHTMLetSVG : un .html ou un .svg du dossier partagé pourrait contenir du JavaScript.
// Ouvert depuis le téléphone, ce script tournerait avec l'accès à tous les fichiers partagés (le
// navigateur a déjà le mot de passe) et pourrait les envoyer ailleurs. « sandbox » l'interdit.
// Les PDF et les vidéos n'en ont pas besoin, et le lecteur PDF du navigateur ne marcherait plus.
func TestSandboxHTMLetSVG(t *testing.T) {
	dir := preparer(t)
	os.WriteFile(filepath.Join(dir, "logo.svg"), []byte("<svg/>"), 0o644)
	h := ouvrir(t, dir)
	cas := map[string]bool{"/page.html": true, "/logo.svg": true, "/site/": true, "/a.txt": false, "/cours/td1.pdf": false}
	for cible, attendu := range cas {
		resp, _ := demander(t, h, "GET", cible)
		if got := resp.Headers["Content-Security-Policy"] == "sandbox"; got != attendu {
			t.Errorf("%s : CSP sandbox = %v, attendu %v (en-tête %q)", cible, got, attendu, resp.Headers["Content-Security-Policy"])
		}
	}
}
