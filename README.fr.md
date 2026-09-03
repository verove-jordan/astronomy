# AstroStack

[English](README.md) · **Français**

> Un poste de travail astrophoto complet : préparez la nuit, pilotez le matériel, puis triez,
> calibrez, empilez et finalisez ce que vous avez photographié — ciel profond, planétaire, solaire,
> comètes, mosaïques et panoramas de Voie lactée.

C'était un empileur, c'est maintenant la nuit entière. **Préparez** ce qui vaut la peine d'être
photographié et quand, **pilotez** la caméra, la roue à filtres et la monture, **traitez** les
captures avec l'une des dix recettes, et **relisez** le résultat avec toute sa provenance. Le tout
est un moteur Go et une interface web Vue par-dessus des outils qui font déjà très bien le plus
dur — **Siril** pour le gros œuvre, **GIMP** pour la finition, avec **GraXpert** / **StarNet++** en
option et un modèle de vision local, opt-in, qui critique et réajuste la finition.

Conçu autour d'une Takahashi FC-100 DF + ZWO ASI 1600MM Pro et d'une RedCat 51 + ASI2600MC, mais
l'équipement n'est que de la configuration — un reflex, une caméra couleur ou un iPhone fonctionne
sans rien régler.

| | | |
|---|---|---|
| **Préparer** | Cibles classées du soir, météo astro, recherche de site sombre, étoiles d'alignement GoTo, almanach d'événements et système solaire en 3-D | [planner.md](docs/planner.md) |
| **Capturer** | Vue en direct, contrôle complet de la caméra, roue à filtres, séquenceur automatique, indicateur de mise au point, centrage astrométrique, alignement polaire, journal de sessions | [ui.md](docs/ui.md) · [mount.md](docs/mount.md) |
| **Traiter** | Dix modes, mono ou couleur, bibliothèque de calibration inter-sessions, notation des poses, miroir S3, live stacking | [pipeline.md](docs/pipeline.md) · [modes/](docs/modes/README.md) |
| **Relire** | Galerie des runs avec provenance complète, aperçus par étape et export pleine résolution, superviseur IA de finition, et un agent conversationnel sur vos propres données | [agent.md](docs/agent.md) |

Il y a deux façons de le lancer. **`just stack`** met tout dans Docker — l'image moteur embarque les
versions Linux de Siril, GIMP et GraXpert — et c'est le chemin en une commande pour une machine
neuve, un serveur, ou simplement « que ça marche ». Le **mode hôte** fait tourner le moteur Go sur
votre Mac avec vos propres Siril/GIMP, seul Postgres restant en Docker ; il est plus rapide à
itérer et c'est lui que les tests Go exercent. Ce second mode est une exception assumée à la règle
« tout en conteneur », parce que Siril et GIMP sont des applications de bureau qui ne peuvent pas
tourner dans un conteneur Linux sur macOS. Voir [docs/architecture.md](docs/architecture.md).

## Démarrage rapide

Tout en Docker — rien à installer sauf Docker et [`just`](https://github.com/casey/just) :

```bash
git clone <repo-url> && cd astronomy
just stack
```

C'est tout. `just stack` crée le `.env` et les dossiers de données, vérifie Docker et les ports,
construit les images, attend le moteur, indique quels outils sont présents et ce qui se dégrade
sans chacun d'eux, puis affiche l'URL. La commande est idempotente — relancez-la quand vous voulez.

**La première construction prend 15 à 40 minutes** et produit une image de plusieurs Go : elle
embarque Siril, GIMP, GraXpert, GDAL et ffmpeg en version Linux, pour que rien n'ait à être
installé sur votre machine. Les lancements suivants la réutilisent.

Ouvrez l'URL affichée (<http://localhost:8082> par défaut) → **Processing → Import**, choisissez un
dossier de captures sous `./input`, lancez un traitement. Chaque page a un bouton **aide** qui
ouvre une visite guidée de la page.

<details>
<summary><b>Mode hôte</b> — la boucle quotidienne plus rapide sur macOS (moteur sur l'hôte, Postgres en Docker)</summary>

Siril et GIMP sont des applications de bureau qui ne peuvent pas tourner dans un conteneur Linux
sur macOS : en développement quotidien le moteur Go tourne donc sur l'hôte et pilote ceux que vous
avez déjà — une [exception assumée](docs/architecture.md#deliberate-deviation) à la règle du
tout-conteneur.

```bash
git clone <repo-url> && cd astronomy
just setup                    # dépendances Go, outils dev, binaire MCP Siril, frontend (idempotent)
just doctor                   # ce qui est installé, et ce qui se dégrade sans
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

</details>

Le modèle de vision (~28 Go) du superviseur reste **opt-in et découplé** — `just stack` ne le
télécharge jamais. Ajoutez-le avec `just run-ia-model` (macOS, Metal natif) ou `just stack-ai` +
`just ai-pull` (Linux + GPU NVIDIA). Matrice par environnement, ports et variables :
[docs/architecture.md → Fully containerized mode](docs/architecture.md#fully-containerized-mode-stack).

## Prérequis

**Mode conteneur (`just stack`) — c'est tout ce qu'il faut :**

- [Docker](https://docs.docker.com/desktop/install/mac-install/) (Desktop sur macOS, démarré) ·
  [`just`](https://github.com/casey/just) (`brew install just`)

Tout ce que le pipeline pilote est embarqué dans l'image moteur. Rien d'autre n'est installé sur
votre machine.

**Mode hôte — en plus, sur l'hôte lui-même :**

- **Requis** : macOS (Apple Silicon recommandé) · Go 1.23+ · Node 22/pnpm ·
  **Siril 1.4+** (`brew install --cask siril`) · ffmpeg (`brew install ffmpeg`)
- **Recommandé** : GIMP (la composition LRGB+Ha ; absent → repli Siril `rgbcomp`) · LibRaw
  (`brew install libraw` — développe les raws reflex/téléphone) · Python 3.12 (résolution
  astrométrique + SPCC de Siril)
- **Optionnel** : [GraXpert](https://www.graxpert.com) (fond de ciel/débruitage IA) ·
  [StarNet++ v2](https://www.starnetastro.com) (réduction d'étoiles) · un modèle de vision local
  (`just run-ia-model`) pour le [superviseur de finition](docs/agent.md)

Lancez **`just doctor`** pour voir lesquels vous avez et ce que coûte chaque absence — le même
rapport que `just stack` affiche depuis l'intérieur du conteneur. Les commandes d'installation
copiables sont dans [docs/getting-started.md](docs/getting-started.md#2-install-the-prerequisites).

Les outils optionnels sont **à échec doux** (absent → avertissement + repli ; `--no-ai` pour tout
couper) et sont *invoqués, jamais embarqués* — StarNet++ n'est donc pas non plus dans l'image :
montez-le et définissez `STARNET_BIN`. Pour une **résolution astrométrique + SPCC hors
ligne**, téléchargez une fois les catalogues Gaia : `just download-catalogues`
(`just download-catalogues-spcc` ajoute les blocs photométriques).

## Utilisation

`just` seul liste toutes les recettes. Celles que vous utiliserez vraiment :

| Recette | Rôle |
|--------|------|
| `just` | Liste toutes les recettes. |
| `just stack` / `just stack-down` / `just stack-logs` | **Toute l'app dans Docker** — installe, construit, lance, diagnostique et affiche l'URL · arrêter · suivre les logs. |
| `just doctor` | Quels outils externes cette machine possède, et ce qui se dégrade sans chacun. |
| `just setup` / `just up` / `just down` | Installation initiale (mode hôte) · démarrer Postgres · arrêter la pile. |
| `just migrate` / `just migrate-down` | Appliquer / annuler les migrations (`dev` migre au démarrage : rarement utile). |
| `just inspect DIR` | Affiche l'inventaire classifié d'un dossier (sans traiter). |
| `just process MODE FORMAT PATH` | Pipeline automatique. MODE : `deepsky`·`nebula`·`milkyway`·`nightpano`·`planetary`·`comet`·`mosaic`·`sun`·`eclipse`·`livestack` ; FORMAT : `image`·`video`·`both`. Options après le chemin (ex. `-v --supervise`). |
| `just video FILE` | Raccourci de `process planetary video` (lucky imaging). |
| `just refine RUNDIR` | Rejoue **uniquement** la finition (superviseur IA) sur un run existant — sans ré-empiler. |
| `just dev` / `just web` | API hôte avec rechargement à chaud · serveur de dev Vue. |
| `just device` / `just device-x86` | Serveur caméra/monture/roue — simulateur, ou une vraie ZWO sous Rosetta. |
| `just device-status` / `just mount-doctor` | Santé du serveur d'appareils · diagnostic du lien USB de la monture. |
| `just mount-audit` / `just mount-reset` | Relit chaque réglage stocké dans la monture · remet ceux que l'app peut écrire. |
| `just run-ia-model` / `just ia-model-status` | Sert le modèle de vision local (~28 Go au premier lancement) · le vérifier. |
| `just download-catalogues` | Catalogues Gaia hors ligne pour l'astrométrie (~3 Go ; `-spcc` ajoute la calibration photométrique). |
| `just download-deepstars` | Le catalogue de 2,5 M d'étoiles derrière l'annotation et la carte 3D. |
| `just download-planet-textures` | Cartes de surface du système solaire 3-D (optionnel ; absentes → rendu procédural). |
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

| Mode | Entrée | Ce qu'il fait |
|------|--------|---------------|
| [`deepsky`](docs/modes/deepsky.md) | FITS mono (L/R/G/B/Ha/OIII/SII), ou couleur | calibration → notation → empilement par canal → co-registration → composition GIMP LRGB avec superpositions d'émission Ha/OIII/SII (palettes : natural/HaRGB/HOO/SHO/HOS/Foraxx/mono). La couleur finalise directement le maître RVB |
| [`nebula`](docs/modes/nebula.md) | FITS mono, ou couleur | deepsky réglé pour l'émission faible : notation indulgente, Ha en avant, réduction d'étoiles |
| [`milkyway`](docs/modes/milkyway.md) | couleur one-shot (iPhone ProRAW/HEIC, raw reflex) | développement photométrique → empilement du ciel seul → composition avec le premier plan et étalonnage ; récupération des météores en option |
| [`nightpano`](docs/modes/nightpano.md) | un arc de pointages balayé à la main | chaque panneau empilé par la recette milkyway, puis résolu astrométriquement à 70° de champ, ajusté à un objectif commun et reprojeté sur une toile sphérique |
| [`planetary`](docs/modes/planetary.md) | vidéo (SER/AVI/MP4/MOV) ou poses | lucky imaging : classement par netteté → alignement multi-points → empilement pondéré → déconvolution |
| [`comet`](docs/modes/comet.md) | FITS horodatés, mono ou couleur | double empilement étoiles/comète sur un alignement global + trajectoire ajustée automatiquement |
| [`mosaic`](docs/modes/mosaic.md) | panneaux jointifs d'un grand objet | empilements deepsky par panneau → résolution astrométrique de chacun → reprojection sur une toile unique + fondu |
| [`sun`](docs/modes/sun.md) | vidéo/poses Hα ou lumière blanche | composite par paliers d'exposition, lucky imaging recalé sur le limbe, PSF mesurée sur le limbe |
| [`eclipse`](docs/modes/eclipse.md) | un Soleil partiellement éclipsé | la recette solaire ajustée sur DEUX cercles, la Lune masquée de l'empilement et de chaque mesure sur le disque ; peut rendre tout l'événement en une planche de progression |
| [`livestack`](docs/modes/livestack.md) | un dossier/préfixe S3 en cours d'écriture | ré-empilement incrémental pendant la capture, pipeline complet à l'arrêt |

L'empilement étape par étape : [docs/pipeline.md](docs/pipeline.md) · par mode :
[docs/modes/](docs/modes/README.md).

## L'interface web

Page par page, avec le sens de chaque réglage : [docs/ui.md](docs/ui.md).

- **Tonight** — cibles classées pour votre site, votre matériel et la Lune, avec courbes de hauteur,
  carte du ciel, couches météo animées, un panneau de météo astro que l'on parcourt nuit par nuit,
  une **recherche de site sombre** (obscurité, horizon boisé, distance par la route) et une aide à
  la mise en station.
- **GoTo** — un jeu ordonné et bien réparti d'étoiles d'alignement pour six profils de raquette,
  parcouru pas à pas ; le serveur replanifie selon ce que vous centrez ou passez.
- **Calendar** — un almanach d'événements (éclipses, phases, essaims, conjonctions, oppositions,
  passages ISS, comètes), chacun noté pour votre site et votre matériel.
- **Solar system** — le système en 3-D, chaque planète là où elle est vraiment, sur son axe réel,
  avec une machine à remonter le temps de 1800 à 2050.
- **Capture** — vue en direct avec histogramme et zoom ; contrôle complet de la caméra (pose, gain,
  offset, refroidissement, et tout ce que la caméra expose) ; roue à filtres à emplacements nommés ;
  séquenceur multi-filtres automatique ; indicateur de mise au point ; GoTo avec centrage
  astrométrique ; enregistrement SER ; assistant de calibration ; et un audit qui relit tout ce qui
  est stocké dans la monture.
- **Logbook** — chaque session, passée et en cours : ce que vous avez photographié, quand, à travers
  quoi, et sous quel ciel, avec les conditions de la nuit résumées en une note.
- **Mosaic** — planifiez la grille de panneaux d'un grand objet, puis capturez-la et empilez-la.
- **Processing** — six onglets : Import & inspection (inventaire multi-dossiers, presets, lancement),
  Live, Tasks (progression SSE, pause/reprise, re-run par étape, panneau superviseur), Runs (galerie
  disque avec export pleine résolution par étape), Library (maîtres de calibration), Storage
  (connexions S3, synchronisation, libération vérifiée, sauvegarde/restauration).
- **AstroAgent** — un chat sur modèle local avec outils à confirmation sur vos jobs, données et
  ciel : [docs/agent.md](docs/agent.md).

Chaque page a un bouton **aide** qui ouvre une visite guidée de la page.

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
| [modes/](docs/modes/README.md) | plongées par mode — une page pour chacun des dix modes |
| [examples/](docs/examples/) | exemples détaillés : un run réel expliqué de bout en bout, chaque chiffre mesuré |
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
