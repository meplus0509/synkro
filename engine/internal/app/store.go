// Package app regroupe le stockage des jobs, l'exécution et l'API HTTP locale.
package app

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"synkro/internal/drives"
	"synkro/internal/syncer"
	"synkro/internal/vfs"
)

// Endpoint est un côté d'un job.
type Endpoint struct {
	Type string          `json:"type"` // "local", "usb" ou "sftp"
	Path string          `json:"path"` // dossier local, ou dossier à l'intérieur de la clé pour "usb"
	SFTP *vfs.SFTPConfig `json:"sftp,omitempty"`
	// Pour "usb" : la clé est reconnue par son numéro de série (et son nom),
	// quelle que soit la lettre de lecteur qu'elle reçoit.
	Volume *drives.Volume `json:"volume,omitempty"`
}

func (e Endpoint) String() string {
	switch {
	case e.Type == "sftp" && e.SFTP != nil:
		// sans le nom d'utilisateur : ce texte part dans les journaux
		host := e.SFTP.Host
		if e.SFTP.Port != 0 && e.SFTP.Port != 22 {
			host += fmt.Sprintf(":%d", e.SFTP.Port)
		}
		return "sftp://" + host + e.SFTP.Path
	case e.Type == "usb" && e.Volume != nil:
		return "clé " + e.Volume.String() + " " + usbSub(e.Path)
	}
	return e.Path
}

// sideKey identifie un côté (utilisateur SFTP compris) : changer de côté oublie l'état mémorisé.
func sideKey(e Endpoint) string {
	if e.Type == "sftp" && e.SFTP != nil {
		return e.String() + "|" + e.SFTP.User
	}
	return e.String()
}

// usbSub normalise le chemin à l'intérieur de la clé (« \Sauvegarde » devient « /Sauvegarde »).
func usbSub(p string) string {
	p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	return "/" + p
}

// Resolve renvoie le dossier réel : pour une clé, cherche sur quelle lettre elle est.
func (e Endpoint) Resolve() (string, error) {
	if e.Type != "usb" {
		return e.Path, nil
	}
	if e.Volume == nil {
		return "", errors.New("clé non définie")
	}
	d, err := drives.Find(*e.Volume)
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Root, filepath.FromSlash(strings.TrimPrefix(usbSub(e.Path), "/"))), nil
}

// Job est une paire de dossiers à synchroniser.
type Job struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Left        Endpoint       `json:"left"`
	Right       Endpoint       `json:"right"`
	Options     syncer.Options `json:"options"`
	AutoMinutes int            `json:"autoMinutes"` // 0 = manuel
	SyncOnPlug  bool           `json:"syncOnPlug"`  // à la connexion aux dossiers (clé branchée, serveur joignable…)
	// analyse et synchronisation automatiques
	OnChange    bool `json:"onChange,omitempty"`    // sur changement de fichier (côtés locaux)…
	ChangeDelay int  `json:"changeDelay,omitempty"` // …après ce délai sans nouveau changement (s)
	OnStart     bool `json:"onStart,omitempty"`     // au démarrage de Synkro
	OnLogoff    bool `json:"onLogoff,omitempty"`    // à la fermeture de session (Windows)
	// RightCoarse : le disque de droite arrondit les dates à 2 s (FAT, exFAT sous
	// Windows) ; appris en corrigeant les dates, les écarts ≤ 2 s sont alors normaux
	RightCoarse bool `json:"rightCoarse,omitempty"`
	// AutoClear : vider la liste après une synchro, ou une analyse sans changement
	AutoClear  bool      `json:"autoClear,omitempty"`
	LastRun    time.Time `json:"lastRun"`
	LastResult string    `json:"lastResult"`
	// Groups : groupes de tâches dont fait partie la tâche (elle est toujours dans « Tout »)
	Groups []string `json:"groups,omitempty"`
	// Group : ancien format (un seul groupe), converti en Groups à la lecture
	Group string `json:"group,omitempty"`
	// LastScan : nombre d'éléments lus à gauche et à droite lors de la dernière
	// analyse (sert à estimer le pourcentage de la suivante)
	LastScan [2]int `json:"lastScan,omitempty"`
}

// Store gère le dossier de données : data.db (jobs, chiffré), settings.json
// (réglages et clé de chiffrement), états, journaux.
type Store struct {
	// Warning : problème rencontré à l'ouverture (affiché et journalisé)
	Warning string
	Dir     string
	mu      sync.Mutex
	jobs    []*Job
	groups  []string   // groupes de tâches créés (dans l'ordre d'affichage)
	plans   []Schedule // planifications (tâches et groupes)
	key     []byte
	// FirstRun : pas de settings.json à l'ouverture (tout premier lancement) ;
	// l'interface ouvre alors les Options (voir FirstRunDone)
	FirstRun bool
}

const secretPrefix = "enc:"

// Masque renvoyé à l'interface à la place d'un secret existant.
const SecretMask = "••••••••"

// DataDir choisit le dossier de données : mode portable si un fichier
// "portable" est posé à côté de l'exécutable (version clé USB), sinon le dossier
// de configuration de l'utilisateur.
func DataDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "portable")); err == nil {
			return filepath.Join(dir, "synkro-data"), nil
		}
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "Synkro"), nil
}

func OpenStore(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "state"), filepath.Join(dir, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	s := &Store{Dir: dir}
	if _, err := os.Stat(s.settingsPath()); errors.Is(err, os.ErrNotExist) {
		s.FirstRun = true // loadKey va créer settings.json
	}
	k, err := s.loadKey()
	if err != nil {
		return nil, err
	}
	s.key = k
	// data.db (chiffré) ; à défaut l'ancien jobs.json, converti tout de suite
	legacy := false
	b, err := os.ReadFile(filepath.Join(dir, dataFile))
	if err == nil {
		if b, err = s.openBytes(b); err != nil {
			// clé différente (settings.json perdu ou remplacé) : on ne bloque pas
			// le démarrage ; l'ancien fichier est mis de côté, pas effacé
			aside := filepath.Join(dir, dataFile+".illisible-"+time.Now().Format("20060102-150405"))
			os.Rename(filepath.Join(dir, dataFile), aside)
			s.Warning = err.Error() + " ; mis de côté : " + filepath.Base(aside)
			b, err = nil, os.ErrNotExist
		}
	} else if b, err = os.ReadFile(filepath.Join(dir, "jobs.json")); err == nil {
		legacy = true
	}
	if err == nil {
		// format actuel : {"jobs": [...], "groups": [...]} ; ancien : la liste seule
		var db struct {
			Jobs   []*Job     `json:"jobs"`
			Groups []string   `json:"groups"`
			Plans  []Schedule `json:"schedules"`
		}
		var err error
		if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
			err = json.Unmarshal(t, &db)
		} else {
			err = json.Unmarshal(b, &db.Jobs)
		}
		if err != nil {
			return nil, fmt.Errorf("liste des jobs illisible : %w", err)
		}
		s.jobs, s.groups, s.plans = db.Jobs, db.Groups, db.Plans
		for _, j := range s.jobs { // ancien format : un seul groupe
			if j.Group != "" {
				if !hasGroup(j.Groups, j.Group) {
					j.Groups = append(j.Groups, j.Group)
				}
				j.Group = ""
			}
		}
		// utilisateur, hôte et port SFTP : déchiffrés en mémoire ; ceux encore en
		// clair (anciennes versions) sont chiffrés tout de suite dans le fichier
		plain := false
		for _, j := range s.jobs {
			for _, e := range []*Endpoint{&j.Left, &j.Right} {
				if e.SFTP == nil {
					continue
				}
				c := e.SFTP
				if (c.User != "" && !strings.HasPrefix(c.User, secretPrefix)) || (c.Host != "" && !strings.HasPrefix(c.Host, secretPrefix)) {
					plain = true
				}
				// valeur illisible (clé changée) : vide, à ressaisir dans le job
				if u, err := s.decrypt(c.User); err == nil {
					c.User = u
				} else {
					c.User = ""
				}
				if strings.HasPrefix(c.Host, secretPrefix) {
					hp, err := s.decrypt(c.Host)
					c.Host, c.Port = "", 0
					if i := strings.LastIndexByte(hp, ':'); err == nil && i > 0 {
						c.Host = hp[:i]
						c.Port, _ = strconv.Atoi(hp[i+1:])
					}
				}
			}
		}
		if plain || legacy {
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
		}
	}
	// états des jobs encore en clair (anciennes versions) : chiffrés maintenant,
	// avant toute synchro (sinon une conversion tardive écraserait un état neuf)
	s.encryptOldStates()
	return s, nil
}

func (s *Store) encrypt(plain string) string {
	if plain == "" || strings.HasPrefix(plain, secretPrefix) {
		return plain
	}
	block, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return secretPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil))
}

func (s *Store) decrypt(v string) (string, error) {
	if !strings.HasPrefix(v, secretPrefix) {
		return v, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, secretPrefix))
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(block)
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("secret corrompu")
	}
	out, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("impossible de déchiffrer le mot de passe (clé de settings.json changée ?)")
	}
	return string(out), nil
}

func (s *Store) saveLocked() error {
	// le nom d'utilisateur SFTP est chiffré dans le fichier (en clair en mémoire)
	out := make([]Job, len(s.jobs))
	for i, j := range s.jobs {
		out[i] = *j
		for _, e := range []*Endpoint{&out[i].Left, &out[i].Right} {
			if e.SFTP != nil {
				c := *e.SFTP
				c.User = s.encrypt(c.User)
				// hôte et port ensemble (« hôte:port »), chiffrés
				if c.Host != "" {
					port := c.Port
					if port == 0 {
						port = 22
					}
					c.Host, c.Port = s.encrypt(fmt.Sprintf("%s:%d", c.Host, port)), 0
				}
				e.SFTP = &c
			}
		}
	}
	b, _ := json.Marshal(struct {
		Jobs   []Job      `json:"jobs"`
		Groups []string   `json:"groups"`
		Plans  []Schedule `json:"schedules"`
	}{out, s.groups, s.plans})
	// tout le fichier est chiffré (data.db) avec la clé de settings.json
	tmp := filepath.Join(s.Dir, dataFile+".tmp")
	if err := os.WriteFile(tmp, s.sealBytes(b), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, dataFile)); err != nil {
		return err
	}
	os.Remove(filepath.Join(s.Dir, "jobs.json")) // ancien format, remplacé par data.db
	return nil
}

// dataFile : la liste des jobs, chiffrée (AES-256-GCM). Remplace jobs.json.
const dataFile = "data.db"

// dataMagic : en-tête de data.db (version du format).
var dataMagic = []byte("SYNKRO-DB1\n")

func (s *Store) sealBytes(plain []byte) []byte {
	block, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	out := append([]byte{}, dataMagic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, dataMagic)
}

func (s *Store) openBytes(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, dataMagic) {
		return nil, errors.New("data.db : format inconnu")
	}
	b = b[len(dataMagic):]
	block, _ := aes.NewCipher(s.key)
	gcm, _ := cipher.NewGCM(block)
	if len(b) < gcm.NonceSize() {
		return nil, errors.New("data.db : fichier tronqué")
	}
	out, err := gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], dataMagic)
	if err != nil {
		return nil, errors.New("data.db : impossible de le déchiffrer (settings.json remplacé ou fichier abîmé)")
	}
	return out, nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// List renvoie des copies des jobs avec les secrets masqués (pour l'interface).
func (s *Store) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, masked(*j))
	}
	return out
}

func masked(j Job) Job {
	for _, e := range []*Endpoint{&j.Left, &j.Right} {
		if e.SFTP != nil {
			c := *e.SFTP
			if c.Password != "" {
				c.Password = SecretMask
			}
			if c.KeyPassphrase != "" {
				c.KeyPassphrase = SecretMask
			}
			e.SFTP = &c
		}
	}
	return j
}

// Get renvoie le job avec les secrets en clair (usage interne).
func (s *Store) Get(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id || strings.EqualFold(j.Name, id) {
			c := *j
			for _, e := range []*Endpoint{&c.Left, &c.Right} {
				if e.SFTP != nil {
					sc := *e.SFTP
					var err error
					if sc.Password, err = s.decrypt(sc.Password); err != nil {
						return c, err
					}
					if sc.KeyPassphrase, err = s.decrypt(sc.KeyPassphrase); err != nil {
						return c, err
					}
					e.SFTP = &sc
				}
			}
			return c, nil
		}
	}
	return Job{}, fmt.Errorf("job %q introuvable", id)
}

func validate(j *Job) error {
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" {
		return errors.New("le nom est obligatoire")
	}
	for side, e := range map[string]*Endpoint{"gauche": &j.Left, "droit": &j.Right} {
		switch e.Type {
		case "local":
			if strings.TrimSpace(e.Path) == "" {
				return fmt.Errorf("côté %s : dossier obligatoire", side)
			}
			e.SFTP, e.Volume = nil, nil
		case "usb":
			if e.Volume == nil || (e.Volume.Serial == "" && strings.TrimSpace(e.Volume.Label) == "") {
				return fmt.Errorf("côté %s : choisissez une clé USB ou un disque", side)
			}
			e.SFTP = nil
		case "sftp":
			if e.SFTP == nil || e.SFTP.Host == "" || e.SFTP.User == "" {
				return fmt.Errorf("côté %s : hôte et utilisateur SFTP obligatoires", side)
			}
			e.Volume = nil
		default:
			return fmt.Errorf("côté %s : type %q inconnu", side, e.Type)
		}
	}
	switch j.Options.ConflictDefault {
	case "", "newer", "left", "right":
	default:
		return fmt.Errorf("règle de conflit %q inconnue", j.Options.ConflictDefault)
	}
	if j.Options.ConflictDefault == "" {
		j.Options.ConflictDefault = "newer"
	}
	switch j.Options.Mode {
	case syncer.TwoWay, syncer.Mirror, syncer.Backup:
	case "":
		j.Options.Mode = syncer.TwoWay
	default:
		return fmt.Errorf("mode %q inconnu", j.Options.Mode)
	}
	if j.AutoMinutes < 0 {
		j.AutoMinutes = 0
	}
	if j.ChangeDelay <= 0 {
		j.ChangeDelay = 10
	}
	if j.ChangeDelay > 86400 {
		j.ChangeDelay = 86400
	}
	if j.Options.MaxChanges < 0 {
		j.Options.MaxChanges = 0
	}
	return nil
}

// Save crée ou met à jour un job. Un secret égal au masque conserve l'ancienne valeur.
func (s *Store) Save(in Job) (Job, error) {
	if err := validate(&in); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing *Job
	for _, j := range s.jobs {
		if j.ID == in.ID && in.ID != "" {
			existing = j
		}
		if strings.EqualFold(j.Name, in.Name) && j.ID != in.ID {
			return Job{}, fmt.Errorf("un job nommé %q existe déjà", in.Name)
		}
	}
	sides := []struct{ in, old *Endpoint }{{&in.Left, nil}, {&in.Right, nil}}
	if existing != nil {
		sides[0].old, sides[1].old = &existing.Left, &existing.Right
	}
	for _, sd := range sides {
		if sd.in.SFTP == nil {
			continue
		}
		c := *sd.in.SFTP
		var old *vfs.SFTPConfig
		if sd.old != nil {
			old = sd.old.SFTP
		}
		if c.Password == SecretMask && old != nil {
			c.Password = old.Password
		} else {
			c.Password = s.encrypt(c.Password)
		}
		if c.KeyPassphrase == SecretMask && old != nil {
			c.KeyPassphrase = old.KeyPassphrase
		} else {
			c.KeyPassphrase = s.encrypt(c.KeyPassphrase)
		}
		// changement de serveur : on oublie l'empreinte mémorisée
		if old != nil && (old.Host != c.Host || old.Port != c.Port) {
			c.HostKey = ""
		} else if old != nil && c.HostKey == "" {
			c.HostKey = old.HostKey
		}
		sd.in.SFTP = &c
	}
	if existing != nil {
		in.LastRun, in.LastResult = existing.LastRun, existing.LastResult
		in.Groups = existing.Groups // les groupes se changent par le menu, pas le formulaire
		// changer les dossiers invalide l'état mémorisé
		if sideKey(existing.Left) != sideKey(in.Left) || sideKey(existing.Right) != sideKey(in.Right) {
			s.removeState(in.ID)
		} else {
			in.LastScan = existing.LastScan
		}
		if sideKey(existing.Right) == sideKey(in.Right) {
			in.RightCoarse = existing.RightCoarse // appris par Synkro, pas par le formulaire
		} else {
			in.RightCoarse = false
		}
		*existing = in
	} else {
		in.ID = newID()
		s.jobs = append(s.jobs, &in)
	}
	if err := s.saveLocked(); err != nil {
		return Job{}, err
	}
	return masked(in), nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID == id {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			s.removeState(id)
			s.dropPlans(func(p Schedule) bool { return p.Kind == "job" && p.Ref == id })
			return s.saveLocked()
		}
	}
	return fmt.Errorf("job %q introuvable", id)
}

// update applique une modification sur le job stocké (résultat, empreinte d'hôte…).
func (s *Store) update(id string, fn func(j *Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			fn(j)
			s.saveLocked()
			return
		}
	}
}

// État mémorisé de chaque job (liste des fichiers au dernier passage) :
// state/<id>.state, compressé puis chiffré avec la clé de settings.json.
// Les anciens state/<id>.json (en clair) sont lus puis remplacés.
func (s *Store) statePath(id string) string { return filepath.Join(s.Dir, "state", id+".state") }
func (s *Store) oldStatePath(id string) string {
	return filepath.Join(s.Dir, "state", id+".json")
}

// removeState efface l'état d'un job (nouveau et ancien format).
func (s *Store) removeState(id string) {
	os.Remove(s.statePath(id))
	os.Remove(s.oldStatePath(id))
}

func (s *Store) LoadState(id string) syncer.State {
	st := syncer.State{}
	if b, err := os.ReadFile(s.statePath(id)); err == nil {
		plain, err := s.openBytes(b)
		if err != nil {
			return st // illisible (clé changée) : comme un premier passage
		}
		zr, err := gzip.NewReader(bytes.NewReader(plain))
		if err != nil {
			return st
		}
		json.NewDecoder(zr).Decode(&st)
		return st
	}
	if b, err := os.ReadFile(s.oldStatePath(id)); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func (s *Store) SaveState(id string, st syncer.State) error {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err := json.NewEncoder(zw).Encode(st); err != nil {
		return err
	}
	zw.Close()
	tmp := s.statePath(id) + ".tmp"
	if err := os.WriteFile(tmp, s.sealBytes(buf.Bytes()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.statePath(id)); err != nil {
		return err
	}
	os.Remove(s.oldStatePath(id)) // ancien format en clair
	return nil
}

// encryptOldStates chiffre les états encore en clair (anciennes versions).
func (s *Store) encryptOldStates() {
	old, _ := filepath.Glob(filepath.Join(s.Dir, "state", "*.json"))
	for _, f := range old {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, err := os.Stat(s.statePath(id)); err == nil {
			os.Remove(f) // déjà converti
			continue
		}
		var st syncer.State
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &st) != nil {
			continue
		}
		s.SaveState(id, st)
	}
}

// AppendLog ajoute des lignes au journal du job.
func (s *Store) AppendLog(id string, lines ...string) {
	f, err := os.OpenFile(filepath.Join(s.Dir, "logs", id+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05")
	for _, l := range lines {
		fmt.Fprintf(f, "%s  %s\n", ts, l)
	}
}

// ReadLog renvoie la fin du journal (au plus maxBytes).
func (s *Store) ReadLog(id string, maxBytes int64) string {
	f, err := os.Open(filepath.Join(s.Dir, "logs", id+".log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	off := int64(0)
	if st.Size() > maxBytes {
		off = st.Size() - maxBytes
	}
	buf := make([]byte, st.Size()-off)
	f.ReadAt(buf, off)
	out := string(buf)
	if off > 0 {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out
}

// Reorder range les jobs dans l'ordre donné (ordre d'affichage). Les jobs
// absents de la liste gardent leur ordre relatif, à la fin.
func (s *Store) Reorder(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := map[string]int{}
	for i, id := range ids {
		if _, dup := pos[id]; !dup {
			pos[id] = i
		}
	}
	out := make([]*Job, 0, len(s.jobs))
	var rest []*Job
	for _, j := range s.jobs {
		if _, ok := pos[j.ID]; ok {
			out = append(out, j)
		} else {
			rest = append(rest, j)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return pos[out[a].ID] < pos[out[b].ID] })
	s.jobs = append(out, rest...)
	return s.saveLocked()
}

// Groups renvoie les groupes de tâches, dans l'ordre d'affichage.
func (s *Store) Groups() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.groups...)
}

// GroupAllName : le groupe qui contient toutes les tâches (pas un vrai groupe).
const GroupAllName = "Tout"

// EditGroup crée, renomme ou supprime un groupe. Supprimer un groupe ne
// supprime pas ses tâches : elles restent dans « Tout ».
func (s *Store) EditGroup(action, name, newName string) error {
	name, newName = strings.TrimSpace(name), strings.TrimSpace(newName)
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, g := range s.groups {
		if strings.EqualFold(g, name) {
			idx = i
		}
	}
	exists := func(n string) bool {
		if strings.EqualFold(n, GroupAllName) {
			return true
		}
		for _, g := range s.groups {
			if strings.EqualFold(g, n) {
				return true
			}
		}
		return false
	}
	switch action {
	case "create":
		if name == "" || len(name) > 80 {
			return errors.New("donnez un nom au groupe (80 caractères au plus)")
		}
		if exists(name) {
			return fmt.Errorf("le groupe « %s » existe déjà", name)
		}
		s.groups = append(s.groups, name)
	case "rename":
		if idx < 0 {
			return fmt.Errorf("groupe « %s » introuvable", name)
		}
		if newName == "" || len(newName) > 80 {
			return errors.New("donnez un nom au groupe (80 caractères au plus)")
		}
		if !strings.EqualFold(newName, name) && exists(newName) {
			return fmt.Errorf("le groupe « %s » existe déjà", newName)
		}
		old := s.groups[idx]
		s.groups[idx] = newName
		for _, j := range s.jobs {
			for k, g := range j.Groups {
				if g == old {
					j.Groups[k] = newName
				}
			}
		}
		for k := range s.plans {
			if s.plans[k].Kind == "group" && s.plans[k].Ref == old {
				s.plans[k].Ref = newName
			}
		}
	case "delete":
		if idx < 0 {
			return fmt.Errorf("groupe « %s » introuvable", name)
		}
		old := s.groups[idx]
		s.groups = append(s.groups[:idx], s.groups[idx+1:]...)
		for _, j := range s.jobs {
			j.Groups = withoutGroup(j.Groups, old)
		}
		s.dropPlans(func(p Schedule) bool { return p.Kind == "group" && p.Ref == old })
	default:
		return errors.New("action inconnue")
	}
	return s.saveLocked()
}

func hasGroup(list []string, g string) bool {
	for _, x := range list {
		if x == g {
			return true
		}
	}
	return false
}

func withoutGroup(list []string, g string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != g {
			out = append(out, x)
		}
	}
	return out
}

// JobGroup ajoute (add) ou retire (remove) une tâche d'un groupe. Une tâche
// peut être dans plusieurs groupes ; elle reste toujours dans « Tout ».
func (s *Store) JobGroup(id, action, group string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasGroup(s.groups, group) {
		return fmt.Errorf("groupe « %s » introuvable", group)
	}
	for _, j := range s.jobs {
		if j.ID != id {
			continue
		}
		switch action {
		case "add":
			if !hasGroup(j.Groups, group) {
				j.Groups = append(j.Groups, group)
			}
		case "remove":
			j.Groups = withoutGroup(j.Groups, group)
		default:
			return errors.New("action inconnue")
		}
		return s.saveLocked()
	}
	return errors.New("tâche introuvable")
}

// Réglages de l'interface (thème…), dans settings.json du dossier de données.
// Ils ne peuvent pas rester dans le navigateur : le port, donc l'origine de la
// page, change à chaque démarrage.
func (s *Store) settingsPath() string { return filepath.Join(s.Dir, "settings.json") }

// settingsKey : clé de chiffrement des secrets, rangée dans settings.json
// (jamais renvoyée à l'interface).
const settingsKey = "secretKey"

func (s *Store) readSettings() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(s.settingsPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func (s *Store) writeSettings(m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := s.settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.settingsPath())
}

// loadKey lit la clé de chiffrement dans settings.json. Une ancienne clé
// secret.key y est déplacée ; sans clé, une nouvelle est créée.
func (s *Store) loadKey() ([]byte, error) {
	m := s.readSettings()
	if v := m[settingsKey]; v != "" {
		k, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(k) != 32 {
			return nil, errors.New("clé de chiffrement invalide dans settings.json")
		}
		return k, nil
	}
	old := filepath.Join(s.Dir, "secret.key")
	k, err := os.ReadFile(old)
	switch {
	case err == nil && len(k) != 32:
		return nil, errors.New("secret.key invalide")
	case errors.Is(err, os.ErrNotExist):
		k = make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	m[settingsKey] = base64.StdEncoding.EncodeToString(k)
	if err := s.writeSettings(m); err != nil {
		return nil, err
	}
	os.Remove(old) // la clé est désormais dans settings.json
	return k, nil
}

// Settings renvoie les réglages de l'interface (sans la clé de chiffrement).
func (s *Store) Settings() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.readSettings()
	delete(m, settingsKey)
	return m
}

// SetSettings enregistre les réglages connus reçus (les autres sont ignorés).
func (s *Store) SetSettings(in map[string]string) (map[string]string, error) {
	onoff := []string{"on", "off"}
	allowed := map[string][]string{
		"theme": {"system", "light", "dark"},
		"lang":  {"fr", "en"},
		"units": {"kb", "bytes"}, // tailles en Ko/Mo/Go, ou en octets
		// fenêtre (Windows) : icône dans la barre des tâches, icône près de l'horloge,
		// fermeture = réduire près de l'horloge
		"taskbar": onoff, "tray": onoff, "closeToTray": onoff,
		// Volume Shadow Copy (Windows) : fichiers verrouillés, instantané avant sauvegarde
		"vssLocked": onoff, "vssSnapshot": onoff,
		// journal du moteur (logs/synkrod.log) : écrit ou non, et niveau de détail
		"logs": onoff, "logLevel": {"errors", "info", "debug"},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.readSettings()
	for k, v := range in {
		for _, ok := range allowed[k] {
			if v == ok {
				cur[k] = v
			}
		}
		// groupes repliés dans la barre latérale (liste JSON)
		if k == "groupsClosed" && len(v) <= 4000 && json.Valid([]byte(v)) {
			cur[k] = v
		}
		// largeur de la barre latérale, en pixels
		if n, err := strconv.Atoi(v); k == "sideWidth" && err == nil && n >= 180 && n <= 700 {
			cur[k] = v
		}
	}
	err := s.writeSettings(cur)
	delete(cur, settingsKey)
	ApplyLogSettings(cur)
	if OnSettings != nil {
		go OnSettings(copyMap(cur))
	}
	return cur, err
}

// CreateShortcut crée un raccourci vers Synkro sur le bureau ("desktop") ou dans
// le menu Démarrer ("startmenu") — Windows ; nil ailleurs.
var CreateShortcut func(where string) (string, error)

// ShortcutPath : chemin du raccourci (pour savoir s'il existe, ou le supprimer).
var ShortcutPath func(where string) (string, error)

// OnSettings est appelé après chaque changement de réglages (fenêtre Windows…).
var OnSettings func(map[string]string)

func copyMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// Setting lit un réglage (valeur par défaut si absent).
func (s *Store) Setting(k, def string) string {
	if v, ok := s.Settings()[k]; ok && v != "" {
		return v
	}
	return def
}
