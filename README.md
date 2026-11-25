# Synkro

Logiciel de synchronisation et de sauvegarde de dossiers pour Windows : dossiers de ce PC ou partages réseau, clé USB ou disque externe, serveur distant en SFTP.

Un seul exécutable, `Synkro.exe` : un moteur écrit en Go qui sert une interface web, affichée dans une fenêtre Windows (WebView2).

- **Utilisateurs** : voir le manuel d'utilisation [`docs/Synkro-Manuel.pdf`](docs/Synkro-Manuel.pdf) (aussi en Word).
- **Installer** : télécharger `SynkroSetup-<version>.exe` dans les [Releases](../../releases) et le lancer (mode installé ou portable).

## Contenu du dossier

```
docs/                    manuel d'utilisation (PDF et Word)
engine/                  sources Go (moteur + interface)
installer/               script de l'installateur (NSIS)
```

## Fonctions principales

- Trois modes : **bidirectionnel**, **miroir →** (copie exacte, suppressions comprises), **sauvegarde →** (jamais de suppression).
- Bidirectionnel « à trois voies » : l'état du dernier passage distingue un fichier supprimé d'un fichier nouveau. Conflits : la plus récente gagne, ou toujours gauche, toujours droite, ou choix manuel.
- **Analyse avant synchro**, avec la liste détaillée des actions, aperçu et comparaison des fichiers, actions modifiables au clic droit.
- **Corbeille** `.synkro-trash` de chaque côté pour tout fichier supprimé ou écrasé.
- **Garde-fou** : synchro annulée si un côté est soudain vide ; limite optionnelle du nombre de modifications.
- Copie atomique (fichier temporaire puis renommage), dates de modification conservées, correction des dates à 1-2 s près (FAT/exFAT).
- Clés USB reconnues par nom et numéro de série de volume, quelle que soit la lettre de lecteur.
- **SFTP** par mot de passe ou clé privée, clé d'hôte mémorisée au premier contact, transferts en flux (128 requêtes en parallèle par fichier).
- Pause, reprise et **changement de vitesse pendant le transfert** ; réessai automatique après coupure.
- Filtres d'inclusion et d'exclusion ; exclusion des fichiers cachés, système, temporaires, dossiers vides.
- **Fichiers verrouillés** (Outlook…) lus dans un instantané Volume Shadow Copy (Synkro lancé en administrateur).
- Lancement automatique : sur changement de fichier, à la connexion d'une clé ou d'un serveur, au démarrage, périodiquement, à la fermeture de session ; **planifications** à date et heure ; **groupes** de tâches lancées à la suite.
- Interface en français et en anglais, thème clair ou sombre, icône près de l'horloge.
- Liste des tâches chiffrée au repos (`data.db`, AES-256-GCM ; clé dans `settings.json`).
- Journal par tâche et journal général `synkrod.log` (désactivé par défaut, trois niveaux de détail).

## Compiler

Prérequis : [Go](https://go.dev/dl/) 1.24 ou plus récent. L'icône de l'exécutable est dans `engine/cmd/synkrod/rsrc_windows_amd64.syso`.

```powershell
cd engine
$env:GOOS="windows"; $env:GOARCH="amd64"
go build -ldflags "-H windowsgui -X main.defaultCmd=window -X synkro/internal/app.Build=2026-11-25" -o Synkro.exe ./cmd/synkrod
```

Le numéro de version est `Version` dans `engine/internal/app/api.go` : il doit correspondre à celui de l'installateur.

## Créer l'installateur

Prérequis : [NSIS](https://nsis.sourceforge.io/) 3. Copier `Synkro.exe` dans `installer/`, puis :

```powershell
cd installer
makensis -DVERSION=0.1.1 -DEXE=Synkro.exe synkro.nsi
```

Résultat : `SynkroSetup-0.1.1.exe`. L'installateur propose le mode installé (`C:\Program Files\Synkro`, avec autorisation administrateur) ou portable (`Bureau\Synkro`), détecte une installation existante pour la mettre à jour, et s'inscrit dans Paramètres › Applications pour la désinstallation.

## Tests

```powershell
cd engine
go test ./...
```

Les tests couvrent la synchro bidirectionnelle, les conflits, les suppressions de dossiers, le garde-fou, les modes miroir et sauvegarde, les exclusions, les transferts SFTP (serveur en mémoire, latence simulée), le changement de vitesse en cours de copie et les niveaux du journal.

## Ligne de commande

L'exécutable accepte aussi des commandes (utile pour le Planificateur de tâches Windows) :

```powershell
Synkro.exe list                       # liste des tâches
Synkro.exe analyze "Nom de la tâche"  # ce qui serait fait
Synkro.exe sync "Nom de la tâche"     # synchronise maintenant
Synkro.exe serve --port 47836         # serveur seul, interface dans le navigateur
```

Option `--data DOSSIER` pour choisir le dossier de données. L'exécutable étant compilé en application fenêtrée, ses messages ne s'affichent pas dans la console.

## Architecture

```
engine/
  cmd/synkrod/        point d'entrée : window | serve | list | analyze | sync ;
                      fenêtre WebView2, verrou d'instance, raccourcis Windows
  internal/vfs/       systèmes de fichiers (local, SFTP)
  internal/drives/    lecteurs et volumes (clés USB par numéro de série)
  internal/syncer/    algorithme : lecture → plan → exécution → nouvel état ;
                      pause, vitesse, réessais, corbeille
  internal/app/       stockage chiffré, exécution des tâches, planifications,
                      API HTTP, journal, interface (ui/ : index.html, app.js,
                      i18n.js, style.css)
installer/
  synkro.nsi          installateur NSIS (installé / portable / mise à jour)
  LISEZMOI.txt        résumé affiché à la fin de l'installation
```

L'API écoute uniquement sur `127.0.0.1`. Chaque appel exige un jeton aléatoire généré au démarrage (en-tête `X-Synkro-Token`), et l'en-tête Host est vérifié (protection contre le DNS rebinding).

## Données

- Mode installé : `%APPDATA%\Synkro`.
- Mode portable : `synkro-data` à côté de `Synkro.exe` (activé par un fichier vide `portable`).
- Journaux : sous-dossier `logs` des données. Un fichier `debug` à côté de l'exécutable force le niveau de journal détaillé.
