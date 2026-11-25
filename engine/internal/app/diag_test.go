package app

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestLevelWriter(t *testing.T) {
	lines := []string{
		"2026/11/25 09:29:27 === Synkro 0.1.0 démarré\n",
		"2026/11/25 09:33:06 API POST /api/jobs/x/fix-dates → 200 en 0s\n",
		"2026/11/25 09:34:27 job \"A\" : Synchronisation finie en 1m → done\n",
		"2026/11/25 09:37:36 [détail] job \"A\" : copy→ a.txt (564 o) OK\n",
		"2026/11/25 18:38:34 job \"A\" : échec delete→ _gsdata_ : dossier non vide\n",
		"2026/11/25 10:00:00 API POST /api/jobs/x/sync → 409 en 0s\n",
		"2026/11/25 10:00:01 API GET /api/jobs → 200 en 4.2s\n",
		"2026/11/25 10:00:02 API GET /api/jobs → 200 en 1.5s\n",
	}
	count := func(m map[string]string) int {
		ApplyLogSettings(m)
		var b bytes.Buffer
		w := LevelWriter{W: &b}
		for _, l := range lines {
			w.Write([]byte(l))
		}
		return bytes.Count(b.Bytes(), []byte("\n"))
	}
	defer ApplyLogSettings(map[string]string{})
	if n := count(map[string]string{"logs": "on", "logLevel": "errors"}); n != 3 {
		t.Fatalf("erreurs : %d lignes", n)
	}
	if n := count(map[string]string{"logs": "on"}); n != 5 || Debug {
		t.Fatalf("normal : %d lignes, debug %v", n, Debug)
	}
	if n := count(map[string]string{"logs": "on", "logLevel": "debug"}); n != 8 || !Debug {
		t.Fatalf("détaillé : %d lignes, debug %v", n, Debug)
	}
	if n := count(map[string]string{}); n != 0 || Debug { // par défaut : pas de journal
		t.Fatalf("par défaut : %d lignes", n)
	}
	if n := count(map[string]string{"logs": "off", "logLevel": "debug"}); n != 0 || Debug {
		t.Fatalf("sans journal : %d lignes", n)
	}
	DebugForced = true
	if n := count(map[string]string{"logs": "off"}); n != 8 {
		t.Fatalf("fichier debug : %d lignes", n)
	}
	DebugForced = false
}

func TestLogLevelChangeTraced(t *testing.T) {
	var b bytes.Buffer
	old := log.Writer()
	log.SetOutput(LevelWriter{W: &b})
	defer func() { log.SetOutput(old); logStarted.Store(false); ApplyLogSettings(map[string]string{}) }()
	ApplyLogSettings(map[string]string{"logs": "on"})
	logStarted.Store(true)
	ApplyLogSettings(map[string]string{"logs": "on", "logLevel": "debug"})
	ApplyLogSettings(map[string]string{"logs": "on", "logLevel": "debug"}) // inchangé : rien
	ApplyLogSettings(map[string]string{"logs": "off"})
	ApplyLogSettings(map[string]string{"logs": "on"})
	out := b.String()
	if strings.Count(out, "niveau « détaillé » choisi") != 1 || !strings.Contains(out, "journal : arrêté") || !strings.Contains(out, "journal : activé dans les Options (niveau « normal »)") {
		t.Fatalf("journal : %q", out)
	}
}
