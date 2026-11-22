// Handler qui sert un dossier en lecture seule : fichiers, index.html et page de listing.
// Utilisé par : cmd/serveur-http, branché sur le serveur (option -dir).
// Ne parle pas au réseau : il reçoit une Request déjà parsée et rend une Response.
// La règle de sécurité n°1 : aucune requête ne peut lire un fichier hors du dossier servi.
package files

import (
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/PiotrGc/serveur-http/internal/http1"
)

// Dir sert un dossier. On le crée avec Open et on le ferme avec Close.
type Dir struct {
	// os.OpenRoot (Go 1.24+) donne un *os.Root : un accès au système de fichiers « enfermé » dans le dossier.
	// Toute ouverture qui sortirait du dossier, par « .. » ou par un lien symbolique qui pointe dehors,
	// échoue au niveau du système, même si notre propre nettoyage du chemin avait un trou.
	// C'est une deuxième barrière derrière cleanPath : la défense en profondeur.
	root *os.Root
}

// Open ouvre le dossier à servir. Erreur si dir n'existe pas ou n'est pas un dossier.
func Open(dir string) (*Dir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Dir{root: root}, nil
}

// Close libère le dossier. Sur Windows, un dossier encore ouvert ne peut pas être supprimé ni renommé.
func (d *Dir) Close() error { return d.root.Close() }

// Serve répond à une requête : 405, 400, 404, 403, 301, un fichier, un index.html ou un listing.
// Sa signature correspond à server.Handler, donc on peut écrire Server{Handler: d.Serve}.
func (d *Dir) Serve(req *http1.Request) *http1.Response {
	root := d.root
	// Lecture seule : seules GET et HEAD sont acceptées (RFC 9110 §15.5.6 : 405 avec Allow).
	if req.Method != "GET" && req.Method != "HEAD" {
		resp := http1.ErrorResponse(405)
		resp.Headers["Allow"] = "GET, HEAD"
		return resp
	}

	urlPath, rel, status := cleanPath(req.Target)
	if status != 0 {
		return http1.ErrorResponse(status)
	}

	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return openError(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return http1.ErrorResponse(500)
	}

	if info.IsDir() {
		// Sans « / » final, le lien relatif « td1.pdf » d'une page /cours pointerait vers /td1.pdf.
		// On redirige donc /cours vers /cours/ (301), comme Apache ou nginx.
		if !strings.HasSuffix(urlPath, "/") {
			f.Close()
			resp := http1.ErrorResponse(301)
			// url.URL.EscapedPath ré-encode le chemin (espaces → %20…) pour qu'il soit valide dans l'en-tête.
			resp.Headers["Location"] = (&url.URL{Path: urlPath + "/"}).EscapedPath()
			return resp
		}
		return serveDir(root, f, urlPath, rel)
	}
	return serveFile(f, info)
}

// cleanPath transforme la cible brute (« /cours/mon%20td.pdf?x=1 ») en :
//   - urlPath : le chemin décodé, tel que le voit l'utilisateur (« /cours/mon td.pdf ») ;
//   - rel : le chemin relatif au dossier servi, avec des « / » (« cours/mon td.pdf », ou « . » pour la racine).
//
// status vaut 0 si tout va bien, sinon le code d'erreur à renvoyer.
func cleanPath(target string) (urlPath, rel string, status int) {
	// strings.Cut coupe au premier « ? » : la query string ne désigne pas un fichier, on l'ignore.
	raw, _, _ := strings.Cut(target, "?")

	// Décodage des %XX : « %20 » → espace, « %2e%2e » → « .. ». On décode AVANT de nettoyer,
	// sinon « %2e%2e%2f » passerait le nettoyage sous forme encodée et deviendrait « ../ » après.
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", "", 400 // « %zz » : encodage invalide
	}

	// Caractères refusés après décodage :
	//   \x00 : l'octet nul coupe les chaînes dans certaines API système (« a.txt\x00.html ») ;
	//   \    : le séparateur Windows, qui permettrait « ..\ » sans passer par « / » ;
	//   :    : « C:/… » (lecteur Windows) et « fichier::$DATA » (flux cachés NTFS).
	if strings.ContainsAny(decoded, "\x00\\:") {
		return "", "", 400
	}

	// path.Clean résout les « . » et « .. ». Comme le chemin commence par « / », un « .. » de trop
	// s'arrête à la racine : path.Clean("/../a") = "/a". On ne peut donc pas remonter au-dessus.
	clean := path.Clean(decoded)
	// path.Clean retire le « / » final : on le remet, car il compte pour les dossiers (redirection 301).
	if strings.HasSuffix(decoded, "/") && clean != "/" {
		clean += "/"
	}

	rel = strings.Trim(clean, "/")
	if rel == "" {
		return clean, ".", 0
	}
	// filepath.IsLocal refuse sur Windows les noms réservés (NUL, CON, COM1…), qui ouvriraient un
	// périphérique au lieu d'un fichier. C'est aussi un dernier contrôle contre « .. » et les chemins absolus.
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", "", 404
	}
	return clean, rel, 0
}

// openError transforme une erreur d'ouverture en code HTTP, sans jamais révéler le chemin réel.
func openError(err error) *http1.Response {
	if errors.Is(err, fs.ErrNotExist) {
		return http1.ErrorResponse(404)
	}
	// Le reste : droits insuffisants, ou tentative de sortie du dossier bloquée par os.Root.
	return http1.ErrorResponse(403)
}

// serveFile répond avec le contenu du fichier, lu en continu par le serveur (qui le fermera).
func serveFile(f *os.File, info fs.FileInfo) *http1.Response {
	// Seulement les fichiers normaux : pas de périphérique, de tube nommé ni de socket.
	if !info.Mode().IsRegular() {
		f.Close()
		return http1.ErrorResponse(403)
	}
	return &http1.Response{
		Status: 200,
		Headers: map[string]string{
			"Content-Type": contentType(info.Name()),
			// nosniff : le navigateur respecte notre Content-Type au lieu de « deviner ».
			// Sans ça, un .txt qui contient du HTML pourrait être exécuté comme une page.
			"X-Content-Type-Options": "nosniff",
		},
		File: f,
		Size: info.Size(),
	}
}

// serveDir sert index.html s'il existe dans le dossier, sinon la page de listing.
func serveDir(root *os.Root, dir *os.File, urlPath, rel string) *http1.Response {
	if idx, err := root.Open(filepath.FromSlash(path.Join(rel, "index.html"))); err == nil {
		if info, err := idx.Stat(); err == nil && info.Mode().IsRegular() {
			dir.Close()
			return serveFile(idx, info)
		}
		idx.Close()
	}

	// ReadDir(-1) : toutes les entrées du dossier d'un coup.
	list, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return http1.ErrorResponse(500)
	}
	entries := make([]entry, 0, len(list))
	for _, d := range list {
		info, err := d.Info()
		if err != nil {
			continue // fichier supprimé entre ReadDir et Info : on l'ignore
		}
		entries = append(entries, entry{Name: d.Name(), Dir: d.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return &http1.Response{
		Status: 200,
		Headers: map[string]string{
			"Content-Type":            "text/html; charset=utf-8",
			"X-Content-Type-Options":  "nosniff",
			"Content-Security-Policy": cspHeader,
		},
		Body: renderListing(urlPath, entries),
	}
}

// contentTypes : le type de chaque extension courante.
// Pourquoi une table et pas mime.TypeByExtension : sur Windows, le paquet mime lit le registre,
// qu'un logiciel installé peut modifier (on a déjà vu des .js servis en text/plain).
// Une table fixe donne le même résultat sur toutes les machines et dans les tests.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".json": "application/json", ".pdf": "application/pdf",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mkv": "video/x-matroska",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".zip": "application/zip",
}

// contentType rend le type d'un fichier d'après son extension (insensible à la casse : .PDF = .pdf).
// Extension inconnue : application/octet-stream, que le navigateur propose de télécharger.
func contentType(name string) string {
	if t, ok := contentTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}
