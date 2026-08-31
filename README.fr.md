# AstroStack

[English](README.md) · **Français**

> Pointez-le vers un dossier de captures astrophoto : il trie, note, calibre, empile et finalise
> automatiquement la meilleure image possible — et planifie votre prochaine session au passage.

AstroStack inspecte un dossier de captures, comprend **ce qu'il contient** (lights par filtre,
darks, offsets, flats — et si le capteur est monochrome ou couleur), **écarte les mauvaises poses**
(étoiles allongées, traînées, nuages),
choisit et applique la **bonne calibration maître** (une bibliothèque inter-sessions avec cartes
de pixels défectueux), puis enregistre et empile par canal avec une réjection adaptative avant de
composer l'image finale. Il pilote **Siril** pour le gros œuvre et **GIMP** pour la finition, avec
des outils IA optionnels (**GraXpert**, **StarNet++**) et un **superviseur local à modèle de
vision** (opt-in) qui critique et réajuste la finition. Un **planificateur de session** intégré
(cibles du soir, alignement GoTo, calendrier d'événements, météo + pollution lumineuse) complète
le flux. Conçu pour une lunette Takahashi FC-100 DF + ZWO ASI 1600MM Pro, mais l'équipement est
configurable — et un reflex ou une caméra couleur fonctionne sans réglage supplémentaire.

Siril et GIMP sont des applications macOS installées sur l'hôte : en développement quotidien le
moteur Go **tourne sur l'hôte** et les pilote directement ; Docker Compose fournit Postgres. C'est
une exception assumée à la règle « tout en conteneur ». Un mode entièrement **conteneurisé**
existe aussi (`just stack`) pour un déploiement portable / serveur Linux. Voir
[docs/architecture.md](docs/architecture.md).

## Démarrage rapide

**Première fois ? Suivez [docs/getting-started.md](docs/getting-started.md)** (en anglais) — le même
chemin, avec les prérequis, la première exécution pas à pas et les pannes courantes.

```bash
git clone <repo-url> && cd astronomy
cp .env.example .env          # ajustez les chemins d'outils / secrets si besoin
just setup                    # dépendances Go, outils dev, binaire MCP Siril, frontend (idempotent)
just up                       # Postgres (Docker) ; le moteur applique le schéma au démarrage

mkdir -p input                # ASTRO_DATA_DIR — la racine explorable par l'UI (git-ignorée : un
cp -r /chemin/vers/captures input/M31   # clone n'a aucun de ces dossiers, ni jeu d'exemple)

# A) l'interface web (deux terminaux) :
just dev                      # API sur http://localhost:8080  (hôte ; pilote Siril/GIMP)
just web                      # UI  sur http://localhost:5173

# B) ou en une commande CLI :
just process deepsky image input/M31          # LRGB+Ha mono, ou couleur — détecté automatiquement
just process planetary video input/lune.mp4   # lucky imaging
```

Ouvrez <http://localhost:5173> → **Processing → Import**, choisissez un dossier de captures,
lancez un traitement. Chaque page a un bouton **aide** qui ouvre une visite guidée de la page.

**Tout en Docker** (portable / serveur — aucun outillage hôte ; l'image moteur embarque les
versions Linux de Siril/GIMP/GraXpert) :

```bash
cp .env.example .env
just stack                    # db + moteur + frontend → UI :8082, API :8080
```

Le modèle de vision (~28 Go) du superviseur reste **opt-in et découplé** — `just stack` ne le
télécharge jamais. Ajoutez-le avec `just run-ia-model` (macOS, Metal natif) ou `just stack-ai` +
`just ai-pull` (Linux + GPU NVIDIA). Matrice par environnement, ports et variables :
[docs/architecture.md → Fully containerized mode](docs/architecture.md#fully-containerized-mode-stack).

## Prérequis

Le développement quotidien sur macOS pilote des outils installés sur l'hôte (l'«
[exception moteur-hôte](docs/architecture.md#deliberate-deviation) » documentée) ; seul Postgres
est en Docker. Pour le tout-conteneur, il ne faut que Docker + `just`.

- **Requis** : macOS (Apple Silicon recommandé) · Docker · [`just`](https://github.com/casey/just)
  · Go 1.23+ · Node 20+/pnpm · **Siril** (`brew install --cask siril`) · ffmpeg
- **Recommandé** : GIMP (la composition LRGB+Ha ; absent → repli Siril `rgbcomp`) · LibRaw
  (`brew install libraw` — développe les raws reflex/téléphone) · Python 3.12 (résolution
  astrométrique + SPCC de Siril)
- **Optionnel** : [GraXpert](https://www.graxpert.com) (fond de ciel/débruitage IA) ·
  [StarNet++ v2](https://www.starnetastro.com) (réduction d'étoiles) · un modèle de vision local
  (`just run-ia-model`) pour le [superviseur de finition](docs/agent.md)

Les outils optionnels sont **à échec doux** (absent → avertissement + repli ; `--no-ai` pour tout
couper) et sont *invoqués, jamais embarqués*. Pour une **résolution astrométrique + SPCC hors
ligne**, téléchargez une fois les catalogues Gaia : `just download-catalogues`
(`just download-catalogues-spcc` ajoute les blocs photométriques).

## Utilisation

`just` seul liste les 58 recettes. Celles que vous utiliserez vraiment :

| Recette | Rôle |
|--------|------|
| `just` | Liste toutes les recettes. |
| `just setup` / `just up` / `just down` | Installation initiale · démarrer Postgres · arrêter la pile. |
| `just migrate` / `just migrate-down` | Appliquer / annuler les migrations (`dev` migre au démarrage : rarement utile). |
| `just inspect DIR` | Affiche l'inventaire classifié d'un dossier (sans traiter). |
| `just process MODE FORMAT PATH` | Pipeline automatique. MODE : `deepsky`·`nebula`·`milkyway`·`planetary`·`comet`·`mosaic`·`sun`·`eclipse`·`livestack` ; FORMAT : `image`·`video`·`both`. Options après le chemin (ex. `-v --supervise`). |
| `just video FILE` | Raccourci de `process planetary video` (lucky imaging). |
| `just refine RUNDIR` | Rejoue **uniquement** la finition (superviseur IA) sur un run existant — sans ré-empiler. |
| `just dev` / `just web` | API hôte avec rechargement à chaud · serveur de dev Vue. |
| `just stack` / `just stack-down` / `just stack-logs` | Toute l'app en Docker (sans modèle IA) · l'arrêter · suivre ses logs. |
| `just device` / `just device-x86` | Serveur caméra/monture/roue — simulateur, ou une vraie ZWO sous Rosetta. |
| `just device-status` / `just mount-doctor` | Santé du serveur d'appareils · diagnostic du lien USB de la monture. |
| `just run-ia-model` / `just ia-model-status` | Sert le modèle de vision local (~28 Go au premier lancement) · le vérifier. |
| `just download-catalogues` | Catalogues Gaia hors ligne pour l'astrométrie (~3 Go ; `-spcc` ajoute la calibration photométrique). |
| `just download-deepstars` | Le catalogue de 2,5 M d'étoiles derrière l'annotation et la carte 3D. |
| `just demo tour` | Enregistre une vidéo de démo de l'UI ([tools/demo](tools/demo/README.md)). |
| `just tour-shots` | Régénère les captures d'écran des visites guidées (à relancer quand l'UI change). |
| `just test` / `just lint` / `just fmt` | Tests · lint et vérification de types · formatage. |
| `just check` | Lint + tests — le portail pré-push. |
| `just clean` | **Destructif** : supprime conteneurs, volumes et artefacts de build. |

Les recettes `just gitnexus-*` pilotent un index de code côté auteur et dépendent d'un outil externe
au projet — ignorez-les.

### Modes

**La couleur est automatique.** Tous les modes acceptent des images monochromes issues d'une roue à
filtres *ou* de la couleur one-shot — un raw reflex/hybride (NEF/CR2/CR3/ARW/RAF/DNG), le FITS Bayer
d'une caméra couleur, ou de simples TIFF/PNG/JPEG RVB. C'est détecté à l'inspection du dossier et
empilé comme un unique canal RVB, la calibration étant appliquée en espace CFA avant dématriçage.
Rien à configurer.

| Mode | Entrée | Pipeline |
|------|--------|----------|
| `deepsky` | FITS mono (L/R/G/B/Ha/OIII/SII), ou couleur | calibration → notation → empilement par canal → co-registration → composition GIMP LRGB avec superpositions d'émission Ha/OIII/SII (palettes : natural/HaRGB/HOO/SHO/HOS/Foraxx/mono). La couleur saute la composition et finalise directement le maître RVB |
| `nebula` | FITS mono, ou couleur | deepsky réglé pour l'émission faible : notation indulgente, Ha en avant, réduction d'étoiles |
| `milkyway` | couleur one-shot (iPhone ProRAW/HEIC, raw reflex) | développement photométrique → empilement du ciel seul → composite avec premier plan + rendu gradé |
| `planetary` | vidéo (SER/AVI/MP4/MOV) ou photos | lucky imaging : tri par netteté → alignement multi-points → empilement pondéré → déconvolution |
| `comet` | FITS horodatés, mono ou couleur | double empilement étoiles/comète sur un alignement global + trajectoire auto-ajustée |
| `mosaic` | panneaux jointifs d'un grand objet | empilement deepsky par panneau → astrométrie de chacun → reprojection sur une toile unique + fondu |
| `sun` | vidéo/photos Hα ou lumière blanche | composite multi-exposition, lucky imaging recalé sur le limbe, PSF mesurée sur le limbe |
| `livestack` | un dossier/préfixe S3 en cours d'écriture | ré-empilement incrémental pendant la capture, pipeline complet au Stop |

L'empilement étape par étape : [docs/pipeline.md](docs/pipeline.md) · par mode :
[docs/modes/](docs/modes/README.md).

## L'interface web

- **Planificateur** — Tonight (cibles classées, météo astro, recherche de site sombre, alignement
  polaire) · GoTo (séquences d'étoiles d'alignement) · Calendar (almanach d'événements). Données
  publiques sans clé, en cache, à échec doux : [docs/planner.md](docs/planner.md).
- **Hub Processing** — six onglets : Import & inspection (inventaire multi-dossiers, **presets**,
  lancement), Live (live stacking), Tasks (jobs avec progression SSE, pause/reprise, re-run par
  étape, panneau superviseur), Runs (galerie disque), Library (maîtres de calibration), Storage
  (connexions S3, synchronisation, libération vérifiée, sauvegarde/restauration). Page par page :
  [docs/ui.md](docs/ui.md).
- **Capture** — vue en direct avec histogramme, viseur et zoom ; contrôle complet de la caméra
  (pose, gain, offset, refroidissement, bande passante USB et tout ce que la caméra expose) ; roue à
  filtres à emplacements nommés ; séquenceur multi-filtres automatique ; indicateur de mise au
  point ; GoTo avec centrage astrométrique ; enregistrement vidéo SER et assistant de calibration.
- **AstroAgent** — un chat sur modèle local avec outils à confirmation sur vos jobs, données et
  ciel : [docs/agent.md](docs/agent.md).

### Connecter du matériel réel

Les appareils tournent dans un processus séparé, lancé par `just device` (un simulateur complet, sans
matériel).

Pour une **caméra ou roue à filtres ZWO réelle sur un Mac Apple Silicon**, utilisez `just device-x86`.
ZWO ne publie aucune bibliothèque macOS arm64 — leur SDK et leur propre ASIStudio sont uniquement
x86_64 — donc ce composant est compilé en x86_64 et exécuté sous Rosetta, pendant que le moteur et
tout l'empilement restent en arm64 natif. Les bibliothèques sont récupérées automatiquement depuis
ASIStudio, ou via `ASI_SDK_LIB` / `EFW_SDK_LIB`. Détails dans
[docs/architecture.md](docs/architecture.md).

La monture parle le protocole Celestron NexStar par le port USB de la raquette (`just device` liste
les ports série candidats).

## Configuration

Tout passe par l'environnement. Copiez [`.env.example`](.env.example) (commenté, groupé) vers
`.env` — `just` et Compose le chargent ; **ne committez jamais de secrets**. Notez que les
répertoires de données sont tous git-ignorés : **un clone frais n'en contient aucun**. Créez
`ASTRO_DATA_DIR` (par défaut `./input`, le seul dossier explorable par l'UI) avant de chercher vos
captures dans l'explorateur de fichiers. Variables phares :
`SIRIL_BIN` / `GIMP_BIN` (outils hôte), `ASTRO_DATA_DIR`/`ASTRO_OUTPUT_DIR`/`ASTRO_LIBRARY_DIR`
(répertoires), `ASTRO_LLM_URL`/`ASTRO_LLM_MODEL` (modèle du superviseur), `ASTRO_SPCC_SENSOR`
(doit correspondre à la base Siril), `ASTRO_LAT`/`ASTRO_LON` (site d'observation), `ASTRO_S3_*`
(S3 en repli). Tables complètes : [docs/configuration.md](docs/configuration.md).

## Architecture & docs

Moteur Go sur l'hôte (CLI + API HTTP + pool de workers en processus ; pas de Redis) pilotant
Siril/GIMP/ffmpeg et les outils IA optionnels ; frontend Vue 3 ; Postgres en `pgx/v5` brut avec
migrations embarquées ; serveurs MCP pour Claude (`siril`, `gimp` vendorisé). Les docs sont
organisées par sujet (en anglais) :

| Doc | Sujet |
|---|---|
| [getting-started.md](docs/getting-started.md) | **commencez ici** — du clone à la première image, avec les pannes courantes |
| [architecture.md](docs/architecture.md) | forme du système, composants, mode conteneurisé `stack`, provenance & santé des outils |
| [pipeline.md](docs/pipeline.md) | comment l'empilement est fait, étape par étape |
| [stacking.md](docs/stacking.md) | méthodes de combinaison, algorithmes de réjection, normalisation et pondération |
| [calibration.md](docs/calibration.md) | bibliothèque de maîtres, pools inter-sessions, **cartes de pixels défectueux**, règles d'appariement |
| [modes/](docs/modes/README.md) | plongées par mode (deepsky · nebula · milkyway · planetary · comet · mosaic · sun · livestack) |
| [mount.md](docs/mount.md) | le lien avec la raquette Celestron : câblage, le piège du pilote macOS, récupération, test d'endurance |
| [storage-s3.md](docs/storage-s3.md) | miroir S3, connexions & secrets, libérations vérifiées, sauvegarde/restauration |
| [configuration.md](docs/configuration.md) | toutes les variables d'environnement |
| [api.md](docs/api.md) | la référence de l'API HTTP |
| [planner.md](docs/planner.md) | les pages du planificateur et leurs sources de données |
| [ui.md](docs/ui.md) | l'interface web, page par page |
| [agent.md](docs/agent.md) | l'IA locale : superviseur de finition, chat AstroAgent, campagnes |
| [third-party.md](docs/third-party.md) | chaque outil, catalogue, service et bibliothèque externe, avec sa licence |
| [verification.md](docs/verification.md) | recettes de vérification de bout en bout avec critères de réussite |

## Développement

- `just check` exécute `go vet` + `golangci-lint` + `vue-tsc` + les suites de tests (miroir du
  portail pré-push). **Les tests Go tournent sur l'hôte** (ils exercent le `siril-cli` hôte) ;
  démarrez Postgres d'abord.
- Les conventions maison vivent dans [`./conventions/`](conventions/) ; les règles projet dans
  [`CLAUDE.md`](CLAUDE.md). Les serveurs MCP sont déclarés dans `.mcp.json` (à construire une
  fois : `just build-mcp`, inclus dans `just setup`).
- Recettes de vérification avec critères objectifs : [docs/verification.md](docs/verification.md).

## Licence

MIT — pour le code de ce dépôt.

AstroStack orchestre énormément de travail réalisé par d'autres : **Siril** et **GIMP** font
l'empilement et la finition, et le ciel lui-même vient d'**Open-Meteo**, de **NASA/NOAA VIIRS**, des
catalogues **HYG**/**ATHYG** et **OpenNGC**, de **Gaia DR3**, du **Minor Planet Center**, de
**CelesTrak**, d'**OpenStreetMap** et d'autres encore. Chaque outil est invoqué plutôt qu'embarqué,
et chaque flux est récupéré à l'exécution sous ses propres conditions.

Deux de ces conditions engagent quiconque redistribue ce projet : **l'offre gratuite d'Open-Meteo est
non commerciale** et ses données sont en CC BY 4.0 (l'attribution est affichée dans l'interface), et
les catalogues **HYG, ATHYG et OpenNGC sont en CC BY-SA**. La liste complète, avec les licences et le
raisonnement derrière chaque choix, est dans [docs/third-party.md](docs/third-party.md).
