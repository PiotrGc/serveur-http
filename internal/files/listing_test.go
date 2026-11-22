// Tests de la page de listing : contrastes des thèmes (WCAG AA), CSP et tailles lisibles.
package files

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// luminance : la luminance relative d'une couleur « #rrggbb », formule WCAG 2.2.
func luminance(hex string) float64 {
	canal := func(i int) float64 {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*canal(0) + 0.7152*canal(1) + 0.0722*canal(2)
}

// contraste : le rapport WCAG entre deux couleurs, de 1 (identiques) à 21 (noir sur blanc).
func contraste(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// TestContrastes : dans chaque thème, clair et sombre, le texte, le texte secondaire et les liens
// ont un contraste d'au moins 4,5:1 sur le fond (WCAG AA), et aussi sur le fond survolé.
func TestContrastes(t *testing.T) {
	// regexp : une règle « :root…{--fond:#…;…} ». Exemple qui passe : {--fond:#f4f4fa;--texte:#1b1b3a}
	bloc := regexp.MustCompile(`\{(--fond:[^}]*)\}`)
	jeton := regexp.MustCompile(`--(\w+):(#[0-9a-f]{6})`)
	themes := bloc.FindAllStringSubmatch(listingCSS, -1)
	if len(themes) != 6 {
		t.Fatalf("%d thèmes trouvés, attendu 6 (3 thèmes × clair/sombre)", len(themes))
	}
	for _, th := range themes {
		c := map[string]string{}
		for _, m := range jeton.FindAllStringSubmatch(th[1], -1) {
			c[m[1]] = m[2]
		}
		for _, fond := range []string{"fond", "survol"} {
			for _, role := range []string{"texte", "doux", "lien"} {
				if r := contraste(c[role], c[fond]); r < 4.5 {
					t.Errorf("%s sur %s : %.2f:1 < 4,5:1 dans %s", role, fond, r, th[1][:40])
				}
			}
		}
	}
}

// TestCSPCorrespondAuContenu : les empreintes de la CSP sont celles du CSS et du JS réellement
// écrits dans la page. Si quelqu'un modifie le CSS sans recalculer, le navigateur bloquerait le style.
func TestCSPCorrespondAuContenu(t *testing.T) {
	page := string(renderListing("/", nil))
	css := page[strings.Index(page, "<style>")+7 : strings.Index(page, "</style>")]
	js := page[strings.Index(page, "<script>")+8 : strings.Index(page, "</script>")]
	if !strings.Contains(cspHeader, cspHash(css)) || !strings.Contains(cspHeader, cspHash(js)) {
		t.Fatal("les empreintes CSP ne correspondent pas au CSS/JS écrits dans la page")
	}
	if !strings.Contains(page, "Ce dossier est vide.") {
		t.Error("état vide manquant")
	}
}

func TestHumanSize(t *testing.T) {
	cas := map[int64]string{0: "0 o", 1023: "1023 o", 1536: "1,5 Ko", 5 << 20: "5,0 Mo", 3 << 30: "3,0 Go"}
	for n, attendu := range cas {
		if got := humanSize(n); got != attendu {
			t.Errorf("humanSize(%d) = %q, attendu %q", n, got, attendu)
		}
	}
}
