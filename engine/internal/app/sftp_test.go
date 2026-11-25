package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"synkro/internal/syncer"
	"synkro/internal/vfs"
)

// startSSH démarre un vrai serveur SSH+SFTP sur 127.0.0.1 (mot de passe "secret").
func startSSH(t *testing.T, root string) int {
	port, _ := startSSHConns(t, root)
	return port
}

// startSSHConns renvoie aussi une fonction qui coupe brutalement les connexions ouvertes.
func startSSHConns(t *testing.T, root string) (int, func()) {
	t.Helper()
	var mu sync.Mutex
	var conns []net.Conn
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) == "secret" {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, in, _ := nc.Accept()
					go func() {
						for r := range in {
							r.Reply(r.Type == "subsystem", nil)
							if r.Type == "subsystem" {
								srv, _ := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(root))
								go func() { srv.Serve(); ch.Close() }()
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() {
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		conns = nil
		mu.Unlock()
	}
}

// Coupure réseau pendant une synchro SFTP : Synkro se reconnecte et termine.
func TestSFTPReconnect(t *testing.T) {
	remote, local := t.TempDir(), t.TempDir()
	for i := 0; i < 5; i++ {
		os.WriteFile(local+"/f"+strconv.Itoa(i)+".txt", []byte("contenu"), 0o644)
	}
	port, kill := startSSHConns(t, remote)
	r, err := vfs.DialSFTP(vfs.SFTPConfig{Host: "127.0.0.1", Port: port, User: "demo", Password: "secret", Path: remote}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	l, _ := vfs.NewLocal(local)
	ls, _ := syncer.Scan(context.Background(), l, nil, nil)
	rs, _ := syncer.Scan(context.Background(), r, nil, nil)
	opt := syncer.Options{Mode: syncer.Mirror, RetrySeconds: 20}
	plan, _ := syncer.BuildPlan(ls, rs, syncer.State{}, opt)
	kill() // coupure juste avant la copie
	_, res := syncer.Execute(context.Background(), plan, l, r, syncer.State{}, opt, nil, nil)
	for _, x := range res {
		if x.Err != "" {
			t.Fatalf("%s : %s", x.Path, x.Err)
		}
	}
	if b, _ := os.ReadFile(remote + "/f3.txt"); string(b) != "contenu" {
		t.Fatal("fichier non copié après la reconnexion")
	}
}

func TestEndpointSFTP(t *testing.T) {
	root := t.TempDir()
	port := startSSH(t, root)
	for _, path := range []string{root, root + "/", ""} {
		msg, err := TestEndpoint(Endpoint{Type: "sftp", SFTP: &vfs.SFTPConfig{
			Host: "127.0.0.1", Port: port, User: "demo", Password: "secret", Path: path,
		}})
		if err != nil {
			t.Fatalf("chemin %q : %v", path, err)
		}
		t.Log(msg)
	}
	_, err := TestEndpoint(Endpoint{Type: "sftp", SFTP: &vfs.SFTPConfig{
		Host: "127.0.0.1", Port: port, User: "demo", Password: "mauvais", Path: root,
	}})
	if err == nil {
		t.Fatal("mauvais mot de passe accepté")
	}
	t.Log("erreur attendue : " + err.Error() + " port " + strconv.Itoa(port))
}

func TestBrowseSFTPAPI(t *testing.T) {
	remote := t.TempDir()
	os.MkdirAll(remote+"/Docs/Sous", 0o755)
	os.MkdirAll(remote+"/Photos", 0o755)
	os.MkdirAll(remote+"/.cache", 0o755)
	os.WriteFile(remote+"/fichier.txt", []byte("x"), 0o644)
	port := startSSH(t, remote)
	store, _ := OpenStore(t.TempDir())
	srv := &Server{Store: store, Runner: NewRunner(store), Token: "t"}
	h := srv.Handler()
	call := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "http://127.0.0.1/api/browse-sftp", strings.NewReader(body))
		req.Header.Set("X-Synkro-Token", "t")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	ep := `{"type":"sftp","sftp":{"host":"127.0.0.1","port":` + strconv.Itoa(port) + `,"user":"demo","password":"secret","path":"` + remote + `/Docs/Inexistant"}}`
	code, out := call(`{"endpoint":` + ep + `}`)
	if code != 200 || !strings.Contains(out, `"dirs":["Sous"]`) || !strings.Contains(out, "n'existe pas") {
		t.Fatalf("%d %s", code, out)
	}
	code, out = call(`{"endpoint":` + ep + `,"path":"` + remote + `"}`)
	if code != 200 || !strings.Contains(out, `"dirs":["Docs","Photos"]`) {
		t.Fatalf("%d %s", code, out)
	}
	bad := strings.Replace(ep, `"secret"`, `"faux"`, 1)
	if code, out = call(`{"endpoint":` + bad + `}`); code != 400 {
		t.Fatalf("mauvais mot de passe accepté : %d %s", code, out)
	}
}
