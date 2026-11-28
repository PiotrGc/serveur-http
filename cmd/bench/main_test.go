// Test du client de charge : une petite mesure, avec et sans keep-alive, doit servir toutes ses
// requêtes sans erreur. Il vérifie le branchement (serveur lancé, compteurs justes), pas la vitesse.
package main

import "testing"

func TestBench(t *testing.T) {
	url, stop, err := startServer(t.TempDir(), 20)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, keepAlive := range []bool{true, false} {
		r := bench(url, 200, 20, keepAlive)
		if r.ok != 200 || r.fail != 0 || r.elapsed <= 0 {
			t.Fatalf("keep-alive=%v : %d réussies, %d échecs, %v (attendu 200 réussies, 0 échec)",
				keepAlive, r.ok, r.fail, r.elapsed)
		}
	}
}
