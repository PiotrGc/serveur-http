// La page HTML qui liste le contenu d'un dossier (quand il n'a pas d'index.html).
// Utilisé par : serveDir dans files.go.
// Elle doit rester lisible sur un téléphone : une colonne Nom et Taille, la date cachée sur petit écran.
// Couleurs en variables CSS (thèmes Bleu Nuit, Ardoise, Forêt), clair ou sombre selon l'appareil,
// thème choisi par l'utilisateur et gardé dans le navigateur (DA de Piotr : couleurs modifiables).
package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"time"
)

// entry : ce qu'on affiche pour chaque élément du dossier.
type entry struct {
	Name    string
	Dir     bool
	Size    int64
	ModTime time.Time
}

// listingCSS : les jetons de couleur de chaque thème, puis la mise en page.
// Les contrastes (texte, texte secondaire, liens) sont vérifiés à ≥ 4,5:1 (WCAG AA) par TestContrastes.
const listingCSS = `
:root,:root[data-theme=nuit]{--fond:#f4f4fa;--texte:#1b1b3a;--doux:#55557a;--lien:#272757;--trait:#d9d9ea;--survol:#e8e8f4}
:root[data-theme=ardoise]{--fond:#f3f5f7;--texte:#1c2329;--doux:#52606b;--lien:#2f5670;--trait:#d6dde2;--survol:#e6ebef}
:root[data-theme=foret]{--fond:#f2f6f3;--texte:#18251d;--doux:#4f6457;--lien:#2e5a40;--trait:#d3ded6;--survol:#e3ece6}
@media (prefers-color-scheme:dark){
:root,:root[data-theme=nuit]{--fond:#14142b;--texte:#e6e6f2;--doux:#a3a3c2;--lien:#b4b4e8;--trait:#2e2e5c;--survol:#1f1f42}
:root[data-theme=ardoise]{--fond:#15191d;--texte:#e3e8ec;--doux:#9aa5ae;--lien:#9ec1d9;--trait:#2a3238;--survol:#1e2429}
:root[data-theme=foret]{--fond:#121a16;--texte:#e2ebe5;--doux:#98aa9f;--lien:#a3c9b0;--trait:#26332b;--survol:#1a251f}}
*{box-sizing:border-box}
body{margin:0;background:var(--fond);color:var(--texte);font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
header{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:16px;border-bottom:1px solid var(--trait)}
h1{margin:0;font-size:1.1rem;font-weight:600;overflow-wrap:anywhere}
select{font:inherit;color:var(--texte);background:var(--fond);border:1px solid var(--trait);border-radius:6px;padding:4px 8px}
main{max-width:900px;margin:0 auto;padding:8px 16px 32px}
table{width:100%;border-collapse:collapse}
th{text-align:left;font-weight:500;color:var(--doux);font-size:.85rem;padding:8px}
td{padding:0;border-top:1px solid var(--trait)}
td a,td span{display:block;padding:10px 8px}
tr:hover td{background:var(--survol)}
a{color:var(--lien);text-decoration:none;overflow-wrap:anywhere}
a:hover{text-decoration:underline}
a:focus-visible,select:focus-visible{outline:2px solid var(--lien);outline-offset:2px}
.num{text-align:right;color:var(--doux);font-variant-numeric:tabular-nums;white-space:nowrap}
.vide{color:var(--doux);padding:24px 8px}
@media (max-width:520px){.date{display:none}}
`

// listingJS : applique le thème choisi et le garde dans le navigateur (localStorage).
// try/catch : localStorage peut être bloqué (navigation privée), la page doit marcher quand même.
const listingJS = `
const s=document.getElementById("theme"),h=document.documentElement;
try{s.value=localStorage.getItem("theme")||"nuit"}catch(e){}
h.dataset.theme=s.value;
s.onchange=()=>{h.dataset.theme=s.value;try{localStorage.setItem("theme",s.value)}catch(e){}};
`

// cspHeader : la Content-Security-Policy de la page de listing.
// default-src 'none' interdit tout (scripts, images, requêtes…) sauf ce qu'on autorise ensuite :
// notre CSS et notre JS, reconnus à leur empreinte SHA-256. Même si un nom de fichier réussissait
// à injecter du HTML, son script n'aurait pas la bonne empreinte et le navigateur refuserait de le lancer.
var cspHeader = fmt.Sprintf("default-src 'none'; style-src '%s'; script-src '%s'; base-uri 'none'; form-action 'none'",
	cspHash(listingCSS), cspHash(listingJS))

// cspHash calcule l'empreinte au format attendu par la CSP : « sha256-<base64> ».
func cspHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// listingTmpl : le gabarit de la page.
// html/template échappe automatiquement selon le contexte : {{.Nom}} dans du texte devient &lt;…&gt;,
// et dans un attribut les guillemets sont échappés. C'est ce qui empêche un nom de fichier piégé
// d'injecter du HTML ou du JavaScript (XSS). Les types template.CSS et template.JS marquent
// notre propre code comme sûr, pour qu'il soit écrit tel quel (et garde la bonne empreinte CSP).
var listingTmpl = template.Must(template.New("listing").Parse(`<!doctype html>
<html lang="fr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Titre}}</title>
<style>{{.CSS}}</style>
</head>
<body>
<header>
<h1>{{.Titre}}</h1>
<select id="theme" aria-label="Thème">
<option value="nuit">Bleu Nuit</option>
<option value="ardoise">Ardoise</option>
<option value="foret">Forêt</option>
</select>
</header>
<main>
<table>
<thead><tr><th>Nom</th><th class="num">Taille</th><th class="num date">Modifié</th></tr></thead>
<tbody>
{{- if .Parent}}
<tr><td colspan="3"><a href="../">Dossier parent</a></td></tr>
{{- end}}
{{- range .Lignes}}
<tr><td><a href="{{.Href}}">{{.Nom}}</a></td><td class="num"><span>{{.Taille}}</span></td><td class="num date"><span>{{.Date}}</span></td></tr>
{{- end}}
</tbody>
</table>
{{- if not .Lignes}}
<p class="vide">Ce dossier est vide.</p>
{{- end}}
</main>
<script>{{.JS}}</script>
</body>
</html>
`))

// ligne : une entrée déjà mise en forme pour le gabarit.
type ligne struct{ Href, Nom, Taille, Date string }

// renderListing construit la page HTML du dossier urlPath (ex. « /cours/ »).
// Les dossiers viennent en premier, puis les fichiers, chacun trié par nom sans tenir compte de la casse.
func renderListing(urlPath string, entries []entry) []byte {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir // true (dossier) avant false (fichier)
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	lignes := make([]ligne, 0, len(entries))
	for _, e := range entries {
		// url.PathEscape encode tout ce qui a un sens spécial dans une URL : « % » → %25, « # » → %23,
		// « ? » → %3F, espace → %20, « " » → %22. Sans ça, « 100% #1.txt » serait lu comme une ancre.
		l := ligne{Href: url.PathEscape(e.Name), Nom: e.Name, Taille: humanSize(e.Size), Date: e.ModTime.Format("02/01/2006 15:04")}
		if e.Dir {
			l.Href += "/"
			l.Nom += "/"
			l.Taille = ""
		}
		lignes = append(lignes, l)
	}

	var buf bytes.Buffer
	// L'erreur ne peut venir que d'un gabarit cassé, ce que les tests verraient tout de suite.
	_ = listingTmpl.Execute(&buf, map[string]any{
		"Titre":  urlPath,
		"Parent": urlPath != "/",
		"Lignes": lignes,
		"CSS":    template.CSS(listingCSS),
		"JS":     template.JS(listingJS),
	})
	return buf.Bytes()
}

// humanSize affiche une taille lisible en unités françaises : « 812 o », « 1,4 Ko », « 2,3 Go ».
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d o", n)
	}
	units := []string{"Ko", "Mo", "Go", "To"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", v, units[i]), ".", ",", 1)
}
