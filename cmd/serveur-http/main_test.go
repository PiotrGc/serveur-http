// Tests du programme : l'adresse d'écoute selon -lan.
package main

import (
	"net"
	"testing"
)

// TestAdresseEcoute : sans -lan, seulement 127.0.0.1 (aucune autre machine ne peut se connecter) ;
// avec -lan, toutes les interfaces (0.0.0.0), donc le Wi-Fi.
func TestAdresseEcoute(t *testing.T) {
	if got := listenAddr(false, 8080); got != "127.0.0.1:8080" {
		t.Errorf("sans -lan : %q", got)
	}
	if got := listenAddr(true, 8080); got != "0.0.0.0:8080" {
		t.Errorf("avec -lan : %q", got)
	}
}

// TestAdressesLAN : on n'affiche que des IPv4 privées (192.168.x, 10.x, 172.16-31.x), jamais
// 127.0.0.1 ni une adresse publique.
func TestAdressesLAN(t *testing.T) {
	for _, ip := range lanIPs() {
		p := net.ParseIP(ip)
		if p == nil || p.To4() == nil || !p.IsPrivate() || p.IsLoopback() {
			t.Errorf("adresse affichée invalide : %q", ip)
		}
	}
}
