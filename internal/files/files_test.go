// Tests du handler de fichiers (V0.2). La sécurité d'abord : aucune requête ne doit pouvoir
// lire un fichier hors du dossier servi. On appelle le handler directement, sans réseau.
package files

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

const secret = "CONTENU-SECRET-HORS-DU-DOSSIER"

// preparer crée cette arborescence dans un dossier temporaire et rend le dossier servi :
//
//	tmp/secret.txt            (hors du dossier servi : ne doit JAMAIS sortir)
//	tmp/partage/a.txt         « bonjour »
//	tmp/partage/page.html
//	tmp/partage/cours/td1.pdf
//	tmp/partage/site/index.html
func preparer(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir() // supprimé automatiquement à la fin du test
	ecrire := func(chemin, contenu string) {
		p := filepath.Join(tmp, filepath.FromSlash(chemin))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(contenu), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ecrire("secret.txt", secret)
	ecrire("partage/a.txt", "bonjour")
	ecrire("partage/page.html", "<p>page</p>")
	ecrire("partage/cours/td1.pdf", "%PDF-1.7 faux pdf")
	ecrire("partage/site/index.html", "<h1>accueil</h1>")
	return filepath.Join(tmp, "partage")
}

// ouvrir ouvre le dossier servi et le referme à la fin du test
// (sur Windows, un dossier encore ouvert ne peut pas être supprimé par t.TempDir).
func ouvrir(t *testing.T, dir string) func(*http1.Request) *http1.Response {
	t.Helper()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d.Serve
}

// demander appelle le handler et rend la réponse avec son corps lu en entier.
func demander(t *testing.T, h func(*http1.Request) *http1.Response, method, target string) (*http1.Response, string) {
	t.Helper()
	resp := h(&http1.Request{Method: method, Target: target, Proto: "HTTP/1.1", Headers: map[string]string{"host": "a"}})
	corps := string(resp.Body)
	if resp.File != nil {
		b, err := io.ReadAll(resp.File)
		if err != nil {
			t.Fatal(err)
		}
		resp.File.Close()
		corps = string(b)
	}
	return resp, corps
}

// TestTraverseeDeChemin : chaque tentative pour sortir du dossier échoue.
func TestTraverseeDeChemin(t *testing.T) {
	dir := preparer(t)
	h := ouvrir(t, dir)
	absolu := filepath.ToSlash(filepath.Join(filepath.Dir(dir), "secret.txt")) // ex. C:/Users/.../secret.txt
	attaques := []string{
		"/../secret.txt",
		"/cours/../../secret.txt",
		"/..%2fsecret.txt",
		"/%2e%2e/secret.txt",
		"/%2e%2e%2fsecret.txt",
		"/%2E%2E%2Fsecret.txt",
		"/..%5csecret.txt", // %5c = « \ », le séparateur Windows
		"/..\\secret.txt",
		"/" + absolu,
		"/C:/Windows/win.ini",
		"/C:%5cWindows%5cwin.ini",
		"//../secret.txt",
		"/a.txt%00.html", // octet nul : une vieille astuce pour tromper les vérifications d'extension
		"/a.txt::$DATA",  // flux de données alternatif NTFS
		"/NUL",           // nom de périphérique réservé de Windows
		"/cours/CON.txt",
	}
	for _, a := range attaques {
		resp, corps := demander(t, h, "GET", a)
		if strings.Contains(corps, secret) {
			t.Errorf("%q a lu le fichier secret !", a)
		}
		if resp.Status == 200 && a != "/../secret.txt" && a != "/cours/../../secret.txt" && a != "//../secret.txt" {
			t.Errorf("%q : statut 200 inattendu", a)
		}
	}
}

// TestPointPointResteDansLeDossier : « .. » ne peut pas remonter au-dessus de la racine servie,
// donc « /../a.txt » désigne simplement « /a.txt ».
func TestPointPointResteDansLeDossier(t *testing.T) {
	h := ouvrir(t, preparer(t))
	resp, corps := demander(t, h, "GET", "/../a.txt")
	if resp.Status != 200 || corps != "bonjour" {
		t.Fatalf("statut %d, corps %q", resp.Status, corps)
	}
}

// TestLienSymboliqueVersLExterieur : un lien dans le dossier qui pointe dehors est refusé.
// Sur Windows, créer un lien demande le mode développeur ou les droits admin : sinon le test est sauté.
func TestLienSymboliqueVersLExterieur(t *testing.T) {
	dir := preparer(t)
	if err := os.Symlink(filepath.Join(filepath.Dir(dir), "secret.txt"), filepath.Join(dir, "lien.txt")); err != nil {
		t.Skipf("impossible de créer un lien symbolique ici : %v", err)
	}
	h := ouvrir(t, dir)
	resp, corps := demander(t, h, "GET", "/lien.txt")
	if strings.Contains(corps, secret) || resp.Status == 200 {
		t.Fatalf("le lien a permis de sortir du dossier (statut %d)", resp.Status)
	}
}

// TestServirFichier : contenu, type et en-tête nosniff.
func TestServirFichier(t *testing.T) {
	h := ouvrir(t, preparer(t))
	cas := []struct{ cible, corps, ctype string }{
		{"/a.txt", "bonjour", "text/plain; charset=utf-8"},
		{"/page.html", "<p>page</p>", "text/html; charset=utf-8"},
		{"/cours/td1.pdf", "%PDF-1.7 faux pdf", "application/pdf"},
		{"/cours/td1.pdf?download=1", "%PDF-1.7 faux pdf", "application/pdf"}, // la query est ignorée
	}
	for _, c := range cas {
		resp, corps := demander(t, h, "GET", c.cible)
		if resp.Status != 200 || corps != c.corps {
			t.Errorf("%s : statut %d, corps %q", c.cible, resp.Status, corps)
		}
		if resp.Headers["Content-Type"] != c.ctype {
			t.Errorf("%s : Content-Type %q, attendu %q", c.cible, resp.Headers["Content-Type"], c.ctype)
		}
		if resp.Headers["X-Content-Type-Options"] != "nosniff" {
			t.Errorf("%s : nosniff manquant", c.cible)
		}
		if resp.Size != int64(len(c.corps)) {
			t.Errorf("%s : Size %d, attendu %d", c.cible, resp.Size, len(c.corps))
		}
	}
}

// TestNomAvecEspaceEncode : « %20 » est décodé en espace.
func TestNomAvecEspaceEncode(t *testing.T) {
	dir := preparer(t)
	os.WriteFile(filepath.Join(dir, "mon cours.txt"), []byte("espace"), 0o644)
	h := ouvrir(t, dir)
	if resp, corps := demander(t, h, "GET", "/mon%20cours.txt"); resp.Status != 200 || corps != "espace" {
		t.Fatalf("statut %d, corps %q", resp.Status, corps)
	}
}

// TestCodesDErreur : 404, 405 avec Allow, 400 pour un encodage cassé.
func TestCodesDErreur(t *testing.T) {
	h := ouvrir(t, preparer(t))
	if resp, _ := demander(t, h, "GET", "/existe-pas.txt"); resp.Status != 404 {
		t.Errorf("fichier absent : statut %d", resp.Status)
	}
	if resp, _ := demander(t, h, "GET", "/%zz"); resp.Status != 400 {
		t.Errorf("encodage cassé : statut %d", resp.Status)
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		resp, _ := demander(t, h, m, "/a.txt")
		if resp.Status != 405 || resp.Headers["Allow"] != "GET, HEAD" {
			t.Errorf("%s : statut %d, Allow %q", m, resp.Status, resp.Headers["Allow"])
		}
	}
}

// TestDossier : redirection vers « / » final, index.html, puis listing.
func TestDossier(t *testing.T) {
	h := ouvrir(t, preparer(t))

	// Sans « / » final, les liens relatifs de la page pointeraient au mauvais endroit : on redirige.
	resp, _ := demander(t, h, "GET", "/cours")
	if resp.Status != 301 || resp.Headers["Location"] != "/cours/" {
		t.Fatalf("/cours : statut %d, Location %q", resp.Status, resp.Headers["Location"])
	}
	resp, _ = demander(t, h, "GET", "/mon%20dossier")
	if resp.Status != 404 {
		t.Fatalf("dossier absent : statut %d", resp.Status)
	}

	if resp, corps := demander(t, h, "GET", "/site/"); resp.Status != 200 || corps != "<h1>accueil</h1>" {
		t.Fatalf("index.html : statut %d, corps %q", resp.Status, corps)
	}

	resp, corps := demander(t, h, "GET", "/")
	if resp.Status != 200 || !strings.HasPrefix(resp.Headers["Content-Type"], "text/html") {
		t.Fatalf("listing : statut %d, type %q", resp.Status, resp.Headers["Content-Type"])
	}
	for _, attendu := range []string{`href="a.txt"`, `href="cours/"`, `href="site/"`} {
		if !strings.Contains(corps, attendu) {
			t.Errorf("listing sans %s", attendu)
		}
	}
	// Les dossiers sont listés avant les fichiers.
	if strings.Index(corps, `href="cours/"`) > strings.Index(corps, `href="a.txt"`) {
		t.Error("les dossiers doivent venir avant les fichiers")
	}
}

// TestListingEchappeLesNoms : un nom de fichier piégé s'affiche en texte, sans s'exécuter (XSS).
// Windows interdit < > " dans les noms, donc on teste la fonction de rendu directement.
func TestListingEchappeLesNoms(t *testing.T) {
	page := string(renderListing("/<b>/", []entry{
		{Name: `<script>alert(1)</script>.txt`, Size: 1},
		{Name: `x" onmouseover="alert(1).txt`, Size: 1},
		{Name: `100% #1 ?.txt`, Size: 1},
	}))
	for _, interdit := range []string{"<script>alert", `" onmouseover="`, "<b>"} {
		if strings.Contains(page, interdit) {
			t.Errorf("le listing contient %q non échappé", interdit)
		}
	}
	// « % », « # » et « ? » doivent être encodés dans le lien, sinon le navigateur les lit
	// comme un encodage, une ancre ou une query.
	if !strings.Contains(page, `href="100%25%20%231%20%3F.txt"`) {
		t.Errorf("lien mal encodé :\n%s", page)
	}
}

// TestCleanPath teste la première barrière toute seule. Les attaques de TestTraverseeDeChemin
// sont aussi arrêtées par os.Root (deuxième barrière) : sans ce test, une barrière cassée
// passerait inaperçue tant que l'autre tient. Chaque barrière doit tenir seule.
func TestCleanPath(t *testing.T) {
	cas := []struct {
		target, urlPath, rel string
		status               int
	}{
		{"/", "/", ".", 0},
		{"/cours/td1.pdf?x=1", "/cours/td1.pdf", "cours/td1.pdf", 0},
		{"/cours/", "/cours/", "cours", 0},
		{"/mon%20cours.txt", "/mon cours.txt", "mon cours.txt", 0},
		{"/../a.txt", "/a.txt", "a.txt", 0},
		{"/%2e%2e%2fa.txt", "/a.txt", "a.txt", 0},
		{"/a/./b/../c", "/a/c", "a/c", 0},
		{"/%zz", "", "", 400},
		{"/a.txt%00.html", "", "", 400},
		{"/..%5csecret.txt", "", "", 400},
		{`/..\secret.txt`, "", "", 400},
		{"/C:/Windows/win.ini", "", "", 400},
		{"/a.txt::$DATA", "", "", 400},
		{"/NUL", "", "", 404},
		// Depuis Windows 11, seul « CON » sans extension ouvre un périphérique : « CON.txt » est un nom
		// de fichier normal, et filepath.IsLocal suit cette règle. Il passe donc cette barrière.
		{"/cours/CON.txt", "/cours/CON.txt", "cours/CON.txt", 0},
		{"/cours/CON", "", "", 404},
		{"/com1", "", "", 404},
	}
	for _, c := range cas {
		urlPath, rel, status := cleanPath(c.target)
		if status != c.status || urlPath != c.urlPath || rel != c.rel {
			t.Errorf("cleanPath(%q) = (%q, %q, %d), attendu (%q, %q, %d)",
				c.target, urlPath, rel, status, c.urlPath, c.rel, c.status)
		}
	}
}
