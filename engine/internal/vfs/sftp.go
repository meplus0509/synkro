package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPConfig décrit une connexion SFTP.
type SFTPConfig struct {
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user"`
	Password      string `json:"password,omitempty"`
	KeyFile       string `json:"keyFile,omitempty"`
	KeyPassphrase string `json:"keyPassphrase,omitempty"`
	Path          string `json:"path"`
	// HostKey est l'empreinte SHA256 de la clé du serveur. Vide = confiance au
	// premier contact : l'empreinte vue est renvoyée via OnNewHostKey.
	HostKey string `json:"hostKey,omitempty"`
}

// SFTP est un backend sur un serveur distant via SSH.
type SFTP struct {
	cfg  SFTPConfig
	root string
	// connexion courante, remplacée en cas de reconnexion (accès concurrents)
	sshc   atomic.Pointer[ssh.Client]
	client atomic.Pointer[sftp.Client]
	mu     sync.Mutex // une seule reconnexion à la fois
	closed atomic.Bool
}

func (s *SFTP) c() *sftp.Client { return s.client.Load() }

// ErrHostKeyMismatch signale que la clé du serveur a changé depuis le premier contact.
var ErrHostKeyMismatch = errors.New("la clé d'hôte du serveur SFTP a changé : connexion refusée (attaque possible ou serveur réinstallé)")

// DialSFTP ouvre une connexion. onNewHostKey est appelée avec l'empreinte quand
// cfg.HostKey est vide, pour que l'appelant l'enregistre.
func DialSFTP(cfg SFTPConfig, onNewHostKey func(fp string)) (*SFTP, error) {
	conn, cl, seen, err := dial(cfg, onNewHostKey)
	if err != nil {
		return nil, err
	}
	s, err := newSFTP(cl, cfg)
	if err != nil {
		cl.Close()
		conn.Close()
		return nil, err
	}
	if s.cfg.HostKey == "" {
		s.cfg.HostKey = seen // une reconnexion exigera la même clé de serveur
	}
	s.sshc.Store(conn)
	return s, nil
}

// Reconnect rouvre la connexion après une coupure (même serveur, même clé exigée).
func (s *SFTP) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return errors.New("connexion fermée")
	}
	conn, cl, _, err := dial(s.cfg, nil)
	if err != nil {
		return err
	}
	oldC, oldS := s.client.Swap(cl), s.sshc.Swap(conn)
	if oldC != nil {
		oldC.Close()
	}
	if oldS != nil {
		oldS.Close()
	}
	return nil
}

func dial(cfg SFTPConfig, onNewHostKey func(fp string)) (*ssh.Client, *sftp.Client, string, error) {
	var seen string
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	var auths []ssh.AuthMethod
	if cfg.KeyFile != "" {
		pem, err := os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, nil, "", fmt.Errorf("lecture de la clé privée : %w", err)
		}
		var signer ssh.Signer
		if cfg.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(cfg.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, nil, "", fmt.Errorf("clé privée invalide : %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auths = append(auths, ssh.Password(cfg.Password))
		auths = append(auths, ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = cfg.Password
			}
			return ans, nil
		}))
	}
	if len(auths) == 0 {
		return nil, nil, "", errors.New("SFTP : indiquez un mot de passe ou une clé privée")
	}
	hostKeyCb := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		seen = fp
		if cfg.HostKey == "" {
			if onNewHostKey != nil {
				onNewHostKey(fp)
			}
			return nil
		}
		if fp != cfg.HostKey {
			return ErrHostKeyMismatch
		}
		return nil
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	conn, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCb,
		Timeout:         20 * time.Second,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("connexion SSH à %s : %w", addr, err)
	}
	// 128 paquets de 32 Ko en vol par fichier (4 Mo) : le débit ne dépend plus
	// du temps d'aller-retour vers un serveur lointain
	cl, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true), sftp.MaxConcurrentRequestsPerFile(128))
	if err != nil {
		conn.Close()
		return nil, nil, "", fmt.Errorf("ouverture de la session SFTP : %w", err)
	}
	return conn, cl, seen, nil
}

// NewSFTPFromClient permet d'utiliser un client déjà ouvert (tests).
func NewSFTPFromClient(cl *sftp.Client, root string) (*SFTP, error) {
	return newSFTP(cl, SFTPConfig{Path: root, Host: "test"})
}

func newSFTP(cl *sftp.Client, cfg SFTPConfig) (*SFTP, error) {
	root := cfg.Path
	if root == "" {
		root = "."
	}
	if !strings.HasPrefix(root, "/") {
		wd, err := cl.Getwd()
		if err == nil {
			root = path.Join(wd, root)
		}
	}
	st, err := cl.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("dossier distant %s : %w", root, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s n'est pas un dossier", root)
	}
	sf := &SFTP{cfg: cfg, root: path.Clean(root)}
	sf.client.Store(cl)
	return sf, nil
}

func (s *SFTP) abs(p string) string { return path.Join(s.root, p) }

// walkWorkers est le nombre de dossiers lus en parallèle : sur SFTP chaque lecture
// coûte un aller-retour réseau, les paralléliser divise d'autant la durée d'analyse.
var walkWorkers = 16

type dirResult struct {
	dir   string
	infos []os.FileInfo
	err   error
}

// Walk parcourt l'arborescence distante en lisant plusieurs dossiers à la fois.
// fn est toujours appelée depuis la goroutine de l'appelant, jamais en parallèle.
func (s *SFTP) Walk(fn func(Entry) (bool, error)) error {
	jobs := make(chan string)
	results := make(chan dirResult)
	for i := 0; i < walkWorkers; i++ {
		go func() {
			for dir := range jobs {
				infos, err := s.c().ReadDir(path.Join(s.root, dir))
				results <- dirResult{dir, infos, err}
			}
		}()
	}
	defer close(jobs)

	queue := []string{""}
	inflight := 0
	var stopErr error
	for len(queue) > 0 || inflight > 0 {
		var send chan string
		var next string
		if len(queue) > 0 && stopErr == nil {
			send, next = jobs, queue[0]
		}
		if send == nil && inflight == 0 {
			break
		}
		select {
		case send <- next:
			queue = queue[1:]
			inflight++
		case res := <-results:
			inflight--
			if res.err != nil {
				if res.dir == "" && stopErr == nil {
					stopErr = res.err
				}
				continue // dossier illisible : ignoré
			}
			if stopErr != nil {
				continue // on vide les réponses en cours avant de sortir
			}
			for _, info := range res.infos {
				if info.Mode()&os.ModeSymlink != 0 {
					continue
				}
				rel := info.Name()
				if res.dir != "" {
					rel = res.dir + "/" + info.Name()
				}
				e := Entry{Path: rel, IsDir: info.IsDir(), ModTime: info.ModTime(), Hidden: strings.HasPrefix(info.Name(), ".")}
				if !info.IsDir() {
					if !info.Mode().IsRegular() {
						continue
					}
					e.Size = info.Size()
				}
				skip, err := fn(e)
				if err != nil {
					stopErr = err
					break
				}
				if info.IsDir() && !skip {
					queue = append(queue, rel)
				}
			}
		}
	}
	return stopErr
}

func (s *SFTP) Stat(p string) (Entry, error) {
	info, err := s.c().Stat(s.abs(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Entry{}, ErrNotExist
		}
		return Entry{}, err
	}
	e := Entry{Path: p, IsDir: info.IsDir(), ModTime: info.ModTime()}
	if !info.IsDir() {
		e.Size = info.Size()
	}
	return e, nil
}

func (s *SFTP) Open(p string) (io.ReadCloser, error) { return s.c().Open(s.abs(p)) }

func (s *SFTP) Create(p string) (io.WriteCloser, error) {
	// On tente directement : créer le dossier parent à chaque fois coûterait un
	// aller-retour réseau de plus par fichier.
	f, err := s.c().Create(s.abs(p))
	if err == nil {
		return f, nil
	}
	if err := s.c().MkdirAll(path.Dir(s.abs(p))); err != nil {
		return nil, err
	}
	return s.c().Create(s.abs(p))
}

func (s *SFTP) MkdirAll(p string) error { return s.c().MkdirAll(s.abs(p)) }

func (s *SFTP) Remove(p string) error { return s.c().Remove(s.abs(p)) }

func (s *SFTP) Rename(o, n string) error {
	// PosixRename écrase la cible ; tous les serveurs ne la supportent pas.
	if err := s.c().PosixRename(s.abs(o), s.abs(n)); err == nil {
		return nil
	}
	if err := s.c().MkdirAll(path.Dir(s.abs(n))); err != nil {
		return err
	}
	if err := s.c().PosixRename(s.abs(o), s.abs(n)); err == nil {
		return nil
	}
	_ = s.c().Remove(s.abs(n))
	return s.c().Rename(s.abs(o), s.abs(n))
}

func (s *SFTP) Chtimes(p string, t time.Time) error { return s.c().Chtimes(s.abs(p), t, t) }

func (s *SFTP) Describe() string {
	// sans le nom d'utilisateur (ce texte peut finir dans les journaux)
	host := s.cfg.Host
	if s.cfg.Port != 0 && s.cfg.Port != 22 {
		host += fmt.Sprintf(":%d", s.cfg.Port)
	}
	return "sftp://" + host + s.root
}

func (s *SFTP) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.c().Close()
	if c := s.sshc.Load(); c != nil {
		c.Close()
	}
	return err
}

// SetWalkWorkers règle le nombre de dossiers SFTP lus en parallèle (tests, réglages).
func SetWalkWorkers(n int) {
	if n > 0 {
		walkWorkers = n
	}
}

func (s *SFTP) RemoveAll(p string) error {
	if p == "" {
		return errors.New("refus de supprimer la racine")
	}
	return s.c().RemoveAll(s.abs(p))
}

// SFTPListing : contenu d'un dossier distant, pour choisir le dossier d'un job.
type SFTPListing struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"`
	Dirs   []string `json:"dirs"`
	Links  []string `json:"links"` // liens symboliques vers un dossier
	Note   string   `json:"note,omitempty"`
}

// BrowseSFTP se connecte et liste les sous-dossiers de dir (chemin absolu ou
// relatif au dossier d'accueil ; vide = dossier du job, sinon dossier d'accueil).
// Si le dossier n'existe pas (encore), on remonte jusqu'au premier qui existe.
func BrowseSFTP(cfg SFTPConfig, dir string) (SFTPListing, error) {
	cl, err := browseClient(cfg)
	if err != nil {
		return SFTPListing{}, err
	}
	home, _ := cl.Getwd()
	if dir == "" {
		dir = cfg.Path
	}
	if dir == "" {
		dir = home
	}
	if !strings.HasPrefix(dir, "/") {
		dir = path.Join(home, dir)
	}
	dir = path.Clean(dir)
	var out SFTPListing
	for {
		if st, err := cl.Stat(dir); err == nil && st.IsDir() {
			break
		}
		if dir == "/" {
			return out, errors.New("aucun dossier lisible sur le serveur")
		}
		out.Note = "« " + dir + " » n'existe pas : affichage du dossier parent"
		dir = path.Dir(dir)
	}
	ents, err := cl.ReadDir(dir)
	if err != nil {
		return out, fmt.Errorf("lecture de %s : %w", dir, err)
	}
	out.Path, out.Parent, out.Dirs, out.Links = dir, path.Dir(dir), []string{}, []string{}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		switch {
		case e.IsDir():
			out.Dirs = append(out.Dirs, name)
		case e.Mode()&os.ModeSymlink != 0:
			if st, err := cl.Stat(path.Join(dir, name)); err == nil && st.IsDir() {
				out.Dirs = append(out.Dirs, name)
				out.Links = append(out.Links, name)
			}
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool { return strings.ToLower(out.Dirs[i]) < strings.ToLower(out.Dirs[j]) })
	return out, nil
}

// SFTPInfo : ce que le serveur dit de lui-même (affiché à côté du job).
type SFTPInfo struct {
	Host     string    `json:"host,omitempty"`     // nom de la machine (si le serveur accepte de le donner)
	Software string    `json:"software,omitempty"` // ex. « OpenSSH_10.0p2 Debian-7+deb13u4 »
	Protocol string    `json:"protocol"`           // ex. « SFTP-3 »
	Free     uint64    `json:"free,omitempty"`     // octets disponibles dans le dossier du job
	Total    uint64    `json:"total,omitempty"`
	At       time.Time `json:"at"`
}

// Info interroge le serveur : version du logiciel, espace libre (extension
// statvfs d'OpenSSH) et nom de la machine. Chaque élément est facultatif.
func (s *SFTP) Info() SFTPInfo {
	in := SFTPInfo{Protocol: "SFTP-3", At: time.Now()}
	if c := s.sshc.Load(); c != nil {
		in.Software = strings.TrimPrefix(string(c.ServerVersion()), "SSH-2.0-")
		// nom de la machine : simple commande « hostname », souvent refusée
		// sur les comptes réservés au SFTP (on s'en passe alors)
		done := make(chan string, 1)
		go func() {
			sess, err := c.NewSession()
			if err != nil {
				done <- ""
				return
			}
			defer sess.Close()
			out, err := sess.Output("hostname")
			if err != nil {
				done <- ""
				return
			}
			done <- strings.TrimSpace(string(out))
		}()
		select {
		case h := <-done:
			if h != "" && len(h) < 100 && !strings.ContainsAny(h, " \n\t") {
				in.Host = h
			}
		case <-time.After(5 * time.Second):
		}
	}
	if st, err := s.c().StatVFS(s.root); err == nil {
		in.Free = st.Frsize * st.Bavail
		in.Total = st.TotalSpace()
	}
	return in
}

// Connexions gardées ouvertes pendant qu'on parcourt un serveur : ouvrir une
// session SSH prend plusieurs secondes, il ne faut pas le refaire à chaque clic.
// Fermées après browseIdle sans utilisation.
const browseIdle = 2 * time.Minute

type browseConn struct {
	conn  io.Closer
	cl    *sftp.Client
	timer *time.Timer
}

var (
	browseMu    sync.Mutex
	browseConns = map[string]*browseConn{}
)

func browseKey(c SFTPConfig) string {
	h := sha256.Sum256([]byte(strings.Join([]string{c.Host, fmt.Sprint(c.Port), c.User, c.Password, c.KeyFile, c.KeyPassphrase, c.HostKey}, "\x00")))
	return hex.EncodeToString(h[:])
}

func browseClient(cfg SFTPConfig) (*sftp.Client, error) {
	key := browseKey(cfg)
	browseMu.Lock()
	if b, ok := browseConns[key]; ok {
		if _, err := b.cl.Getwd(); err == nil { // toujours vivante
			b.timer.Reset(browseIdle)
			browseMu.Unlock()
			return b.cl, nil
		}
		b.timer.Stop()
		b.cl.Close()
		b.conn.Close()
		delete(browseConns, key)
	}
	browseMu.Unlock()
	conn, cl, _, err := dial(cfg, nil)
	if err != nil {
		return nil, err
	}
	b := &browseConn{conn: conn, cl: cl}
	b.timer = time.AfterFunc(browseIdle, func() {
		browseMu.Lock()
		if browseConns[key] == b {
			delete(browseConns, key)
		}
		browseMu.Unlock()
		cl.Close()
		conn.Close()
	})
	browseMu.Lock()
	if old, ok := browseConns[key]; ok { // ouverte en parallèle : on garde la plus récente
		old.timer.Stop()
		old.cl.Close()
		old.conn.Close()
	}
	browseConns[key] = b
	browseMu.Unlock()
	return cl, nil
}
