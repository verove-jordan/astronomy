# AstroStack

[English](README.md) · **Français**

> Pointez-le vers un dossier de captures d'astrophotographie : il trie, note, calibre, empile et
> finalise automatiquement vos images pour en tirer la meilleure image finale possible — et planifie
> votre prochaine session par la même occasion.

AstroStack inspecte un répertoire de captures, détermine **ce qu'il contient** (images brutes par
filtre L/R/G/B/Ha, plus darks, bias/offsets, flats, dark-flats), **écarte les sous-images de mauvaise
qualité** (étoiles allongées, traînées, nuages), choisit et applique les **bons masters de
calibration**, puis aligne et empile chaque canal avant de les combiner (LRGB + Ha) en une image
finalisée. Il pilote **Siril** pour le gros du travail et **GIMP** pour la finition, avec des
améliorations IA optionnelles (**GraXpert**, **StarNet++**) et un **superviseur local à modèle de
vision** activable qui ajuste automatiquement la finition. Un **planificateur de session** intégré
(cibles du soir, alignement GoTo, calendrier d'événements, calques météo et pollution lumineuse)
complète le flux de travail. Conçu pour une configuration mono Takahashi FC-100 DF + ZWO ASI 1600MM
Pro, mais le matériel est configurable.

Siril et GIMP sont des applications macOS installées sur l'hôte ; pour le développement quotidien sous
macOS le moteur Go **s'exécute donc sur l'hôte** et les pilote directement, tandis que Docker Compose
fournit Postgres. C'est une exception délibérée à la règle habituelle du « tout-en-conteneur ». Un mode
entièrement **conteneurisé** est aussi fourni (`just stack`) pour des déploiements portables / serveur
Linux — il embarque les builds Linux de Siril/GIMP/GraXpert dans l'image du moteur ; sur un hôte
Linux+GPU, même le modèle IA tourne dans un conteneur. Voir [docs/architecture.md](docs/architecture.md).

---

## Démarrage rapide

```bash
git clone <repo-url> && cd astronomy
cp .env.example .env          # ajustez les chemins des outils hôte / secrets si nécessaire
just setup                    # deps Go, outils de dev, binaire MCP Siril, deps frontend (idempotent)
just up                       # démarre Postgres (Docker)
just migrate                  # crée le schéma

# A) en one-shot depuis la CLI :  just process <mode> <format> <chemin>
just process deepsky  image ~/Astro/M31          # mono LRGB+Ha → .xcf en calques + tif/png
just process planetary video ~/Astro/moon.mp4    # lucky imaging

# B) ou l'interface web (deux terminaux) :
just dev                      # API sur http://localhost:8080  (hôte ; pilote Siril/GIMP)
just web                      # UI  sur http://localhost:5173
```

Ouvrez <http://localhost:5173>, allez dans **Processing → Import**, pointez vers un dossier de captures
et lancez un traitement.

### Tout dans Docker (portable / serveur)

Exécutez toute l'application — moteur + frontend + Postgres — en conteneurs, sans toolchain hôte.
L'image du moteur embarque les Siril/GIMP/GraXpert Linux :

```bash
cp .env.example .env
just stack                    # db + moteur + frontend, tout en Docker (sans modèle IA)
# → UI web http://localhost:8082 · API moteur http://localhost:8080
```

Le modèle de vision du superviseur reste **activable et découplé** — `just stack` ne le télécharge
jamais. Ajoutez-le quand vous le souhaitez :

- **Linux + GPU NVIDIA :** `just stack-ai` (ou `just ai-up` plus tard) + `just ai-pull` — le modèle
  tourne dans un conteneur. Mettez `ASTRO_LLM_URL=http://ai:11434/v1` et `ASTRO_LLM_MODEL=qwen2.5vl:32b`
  dans `.env`.
- **macOS :** Docker n'a pas de GPU, gardez donc le modèle **natif** — `just run-ia-model` (Metal) — et
  laissez `ASTRO_LLM_URL` pointer vers l'hôte.

> **Architecture :** l'image du moteur est construite pour **votre hôte** — arm64 sur Apple Silicon,
> amd64 sur Linux — donc Siril/GIMP tournent nativement (sans émulation). Siril n'a pas de build arm64 :
> sur arm64 il provient de la distribution (Siril ~1.2.x) au lieu de l'AppImage x86_64 1.4.3 utilisée sur
> amd64 ; pour une parité multi-sessions exacte, préférez un hôte amd64 natif (ou le mode hôte sous macOS).

Matrice complète par environnement, tables services/ports et variables d'env du stack :
**[Docker & déploiement](#docker--déploiement)**.

---

## Prérequis

Pour le **développement quotidien sous macOS**, le moteur invoque Siril/GIMP/ffmpeg installés sur l'hôte
(et les outils IA optionnels), et seul Postgres tourne dans Docker — les prérequis ci-dessous couvrent ce
chemin. Pour exécuter **tout en conteneurs** (portable / serveur Linux), ignorez la plupart d'entre eux —
il ne faut que Docker + `just` — et allez à [Docker & déploiement](#docker--déploiement).

### Requis

| Outil | Installation | Pourquoi |
|------|---------|-----|
| macOS (Apple Silicon recommandé) | — | le moteur hôte pilote des bundles d'applications macOS ; le serveur de modèle IA nécessite Apple MLX |
| Docker + Compose | Docker Desktop | exécute Postgres (le seul service conteneurisé) |
| [`just`](https://github.com/casey/just) | `brew install just` | lanceur de tâches — chaque commande ci-dessous est une recette |
| Go 1.23+ | `brew install go` | le moteur, la CLI et le MCP Siril (toolchain figée à 1.23.3) |
| Node 20+ / pnpm | `brew install node pnpm` | l'interface web Vue |
| Siril | `brew install --cask siril` | calibration · alignement · empilement · étirement (**le moteur central**) |
| ffmpeg | `brew install ffmpeg` | extraction des images vidéo + rendu MP4 Ken-Burns |

### Recommandé

| Outil | Installation | Pourquoi |
|------|---------|-----|
| GIMP | `brew install --cask gimp` | le composite de finition LRGB+Ha ; absent → repli sur `rgbcomp` de Siril |
| Python 3.12 | `brew install python@3.12` | le scripting embarqué de Siril pour la **résolution astrométrique + la calibration colorimétrique SPCC** (sans lui, le ciel peut virer au brun) ; alimente aussi le venv du modèle IA |

### Optionnel (améliorations IA + le superviseur local)

| Outil | Installation | Pourquoi |
|------|---------|-----|
| GraXpert | [graxpert.com](https://www.graxpert.com) | extraction IA du gradient de fond / débruitage ; absent → `subsky` de Siril |
| StarNet++ v2 | [starnetastro.com](https://www.starnetastro.com) | retrait des étoiles pour une finition à étoiles réduites ; absent → étoiles complètes |
| Modèle de vision local | `just run-ia-model` (télécharge ~26 Go) | le **superviseur de finition** activable — un modèle de vision MLX hôte qui critique et réajuste la finition pour **tous les modes d'empilement** (ciel profond, comète, voie lactée, planétaire ; voir plus bas) |
| OpenCV | `brew install opencv` | uniquement pour le détecteur de traînées GoCV optionnel ; absent → le détecteur de Hough en Go pur |

GraXpert et StarNet++ sont **à tolérance de panne** : si le binaire est absent, le traitement consigne
un avertissement et bascule en repli. Désactivez les deux explicitement avec `process … --no-ai`. Aucun
des outils optionnels n'est embarqué — ils sont *invoqués* comme Siril/GIMP, donc leurs licences
restent avec votre propre installation.

---

## Utilisation

| Recette | Ce qu'elle fait |
|--------|--------------|
| `just setup` | Configuration initiale (deps Go, outils de dev, binaire MCP, deps frontend). Idempotent. |
| `just up` / `just down` | Démarre / arrête Postgres (Docker). |
| `just migrate` / `just migrate-down` | Applique / annule les migrations de la base. |
| `just inspect DIR` | Affiche l'inventaire classifié d'un dossier de captures (sans traitement). |
| `just process MODE FORMAT PATH` | Pipeline auto complet. MODE : `deepsky`·`nebula`·`milkyway`·`planetary` ; FORMAT : `image`·`video`·`both`. Le type d'entrée est détecté automatiquement. |
| `just video FILE` | Raccourci pour `process planetary video` (lucky imaging). |
| `just refine RUNDIR` | Relance **uniquement** la finition (via le superviseur IA) sur un traitement existant — sans réempilement. |
| `just dev` | Lance le serveur API sur l'hôte avec rechargement à chaud. |
| `just web` | Lance le serveur de dev de l'interface web Vue. |
| `just run-ia-model` | Sert le modèle de vision local pour le superviseur de finition (premier lancement : ~26 Go à télécharger). |
| `just stop-ia-model` / `just ia-model-status` | Arrête / vérifie l'état du serveur de modèle. |
| `just mcp-siril` | Lance le serveur MCP Siril au premier plan (tests manuels). |
| `just tools` | Démarre Adminer (UI de la base) sur `http://localhost:8081`. |
| `just stack` / `just stack-down` | Lance / arrête toute l'app en Docker (moteur + frontend + db), **sans modèle IA**. |
| `just stack-ai` | Idem, plus le VLM conteneurisé (Linux + GPU NVIDIA). |
| `just ai-up` / `just ai-pull` / `just ai-down` | Démarre / télécharge les poids / arrête le conteneur du modèle, découplé du stack. |
| `just check` | Lint + tests (la barrière de pre-push). |
| `just clean` | Supprime conteneurs, volumes et artefacts de build (destructif ; demande confirmation). |

`just process` accepte des drapeaux passe-plat après le chemin, p. ex.
`just process deepsky image ~/Astro/M31 -v --supervise`.

### Modes

Chaque mode réajuste la notation, l'extraction de fond, l'étirement, le mélange Ha, la saturation et
les courbes :

| Mode | Entrée | Pipeline |
|------|-------|----------|
| `deepsky` | FITS mono (L/R/G/B/Ha) | calibration → notation → empilement par canal → co-alignement des canaux → composite GIMP LRGB+Ha + courbes douces |
| `nebula` | FITS mono | comme deepsky mais notation indulgente + extraction IA du fond + mélange privilégiant le Ha + réduction d'étoiles StarNet++ |
| `milkyway` | couleur one-shot (iPhone ProRAW/HEIC, jpg/png/tif) | dématriçage → alignement → notation → empilement → courbes GIMP ; *rendu* réglable (natural/iphone/deepsky) + *luminosité* |
| `planetary` | vidéo (SER/AVI/MP4/MOV) | lucky imaging : tri des images par netteté → empilement du meilleur % → accentuation |

---

## L'interface web

L'interface comporte deux grands espaces : un **planificateur de session** (quoi photographier, comment
s'aligner, quand surviennent les événements) et le hub **Processing** (transformer les captures en image
finale).

### Planificateur

| Page | Route | À quoi ça sert |
|------|-------|---------------|
| **Tonight** | `/tonight` | Classe les cibles du ciel profond du soir selon votre lieu, votre matériel et les conditions de Lune/obscurité. Filtres par type/score/cadrage, mode caméra **ou** oculaire (visuel), courbes d'altitude, carte du ciel. Onglets intégrés **Polar** (réticule de viseur polaire + aide à l'alignement) et **Dark sky** (recherche de site sombre à faible pollution lumineuse). |
| **GoTo** | `/goto` | Calcule un jeu d'**étoiles d'alignement de monture** ordonné et bien réparti pour votre routine GoTo (six profils de monture). Parcourez la séquence de façon interactive — centrer/passer — et le serveur replanifie en fonction de vos choix. |
| **Calendar** | `/calendar` | Un almanach d'événements astronomiques : éclipses, phases de Lune, pluies d'étoiles filantes, conjonctions, oppositions, équinoxes… sous forme de calendrier mensuel sur une fenêtre de dates, ou les N prochains d'un seul type — chacun noté pour votre site et votre matériel. |

Le site d'observation (carte / adresse / géolocalisation) se choisit une fois et est partagé par les
trois pages ; les calques météo et pollution lumineuse proviennent par défaut d'API publiques sans clé.

### Hub de traitement (`/processing`)

Une page, cinq onglets :

| Onglet | Route | À quoi ça sert |
|-----|-------|---------------|
| **Import** | `/processing/import` | La page principale : parcourir et **multi-sélectionner des dossiers de captures**, les inspecter/fusionner en un seul inventaire, vérifier la correspondance des canaux + les tables brutes/calibration/fichiers, puis configurer et **lancer un traitement** (mode, format de sortie, options de calibration, réutilisation inter-sessions et le superviseur IA local optionnel). |
| **Live** | `/processing/live` | Démarre une session d'**empilement en direct incrémental** qui surveille un dossier local ou un préfixe S3 et réempile à mesure que de nouvelles poses arrivent ; la finition lourde ne s'exécute qu'au **Stop & finalize**. |
| **Tasks** | `/processing/tasks` | La file des tâches — chaque traitement avec statut + progression ; cliquez pour accéder à la vue détaillée en direct. |
| **Runs** | `/processing/runs` | Une galerie des traitements passés, sur disque (indépendante de la base) ; ouvrez-en un pour réafficher l'ensemble de ses panneaux de résultats. |
| **Library** | `/processing/library` | La **bibliothèque de masters de calibration** réutilisables (darks/flats/bias) que le pipeline construit et fait correspondre entre les sessions. |

La vue **détail d'une tâche** (sous *Tasks*) diffuse la progression en direct via SSE : barre de
progression, étape en cours, usage CPU/RAM en direct, journal défilant et image de prévisualisation en
direct. À la fin, elle montre l'image/vidéo finale, les stats d'empilement par canal, les graphiques de
notation par image, les masters utilisés, les liens de téléchargement et — pour les traitements
supervisés — le **panneau du superviseur IA** (une carte par itération avec ses scores, son
raisonnement, et un badge sur la meilleure retenue).

---

## Comment fonctionne le traitement

Un *schéma fonctionnel* de ce qui se passe quand vous traitez un dossier — de la page Import au moteur
jusqu'aux fichiers finaux.

### Flux de données (UI ↔ moteur ↔ outils)

```mermaid
flowchart TB
    subgraph UI["Navigateur — UI Vue 3 (:5173)"]
        IMP["Onglet Import<br/>choix dossier(s) · mode · format<br/>· options · réutilisation · ☐ superviseur IA"]
        JOB["Vue tâche<br/>progression · prévisu · journaux"]
    end

    subgraph ENG["moteur astrostack — hôte (:8080)"]
        INS["POST /api/inspect<br/>classifie + fusionne les dossiers"]
        REU["POST /api/reuse/preview<br/>découvre les sessions antérieures"]
        CRE["POST /api/jobs<br/>→ pool de workers in-process"]
        PIPE["pipeline.Process"]
        SSE["GET /api/jobs/:id/events (SSE)"]
    end

    subgraph TOOLS["Outils hôte externes"]
        SIRIL["Siril — calibre/aligne/empile/étire"]
        GIMP["GIMP — composite LRGB+Ha"]
        GRAX["GraXpert (opt)"]
        STAR["StarNet++ (opt)"]
        LLM["modèle de vision MLX (opt, :1234)"]
        FF["ffmpeg — vidéo"]
    end

    PG[("Postgres<br/>tâches · catalogue d'images · masters")]

    IMP -->|sélection| INS
    IMP --> REU
    IMP -->|Lancer| CRE
    CRE --> PIPE
    PIPE --> SIRIL & GIMP & GRAX & STAR & LLM & FF
    PIPE <--> PG
    PIPE -. progression+prévisu .-> SSE
    SSE -. flux .-> JOB
    CRE -->|id tâche| JOB
```

### Le pipeline (ciel profond)

```mermaid
flowchart LR
    A["1· Inspecter<br/>classer L/R/G/B/Ha<br/>+ darks/flats/bias"] --> B["2· Masters de calibration<br/>construire/réutiliser les masters<br/>(bibliothèque · inter-sessions)"]
    B --> C["3· Par canal<br/>calibrer → aligner<br/>→ noter/rejeter → empiler"]
    C --> D["4· Co-aligner les canaux<br/>L·R·G·B·Ha alignés"]
    D --> E["5· Finition<br/>fond GraXpert → SPCC → étirement<br/>→ GIMP LRGB+Ha → StarNet"]
    E --> F["6· (opt) superviseur IA<br/>réafficher la finition plusieurs fois,<br/>noter, garder la meilleure"]
    F --> G["Sorties<br/>.xcf · tif · png · mp4<br/>+ rapport JSON/Markdown"]
```

1. **Inspecter** — parcourt le(s) dossier(s), lit chaque en-tête FITS, classe chaque fichier
   (light/dark/flat/bias/dark-flat/vidéo) et regroupe en *sets* par objet, filtre, exposition, gain,
   offset, température et binning. Les captures héritées à nom de fichier nu sont étiquetées depuis un
   fichier annexe `info.txt`. Plusieurs dossiers sont fusionnés en un seul inventaire, de sorte que la
   calibration de l'un peut servir les brutes d'un autre.
2. **Masters de calibration** — empile un master par set de calibration (sigma winsorisé). Les masters
   sont enregistrés dans une **bibliothèque réutilisable** et appariés à chaque set de brutes (darks par
   exposition+température+gain+offset, flats par filtre, bias par gain+offset) ; une session uniquement
   brutes tire les bons masters de la bibliothèque.
3. **Par canal** — calibre + aligne les brutes, **note** chaque pose à partir de ses métriques
   d'alignement (FWHM, rondeur, nombre d'étoiles, fond de ciel) plus un **détecteur de traînées** de
   Hough, rejette les mauvaises et n'empile que les survivantes.
4. **Co-aligner les canaux** — aligne les masters par canal sur une même référence afin que L/R/G/B/Ha
   se superposent avant le compositing.
5. **Finition** — extraction du fond (GraXpert si présent, sinon `subsky` de Siril), calibration
   colorimétrique (SPCC), étirement, puis pilotage du serveur Script-Fu de GIMP résident pour construire
   une image en calques (RGB + luminance L + Ha en mode écran), application de courbes douces et export
   d'un `.xcf` éditable plus un TIFF/PNG aplati. StarNet++ optionnel produit une variante à étoiles
   réduites. Si GIMP est indisponible, repli sur `rgbcomp` de Siril.
6. **Superviseur IA** *(activable)* — quand il est activé, une boucle bornée réaffiche uniquement le
   composite GIMP rapide quelques fois (en variant saturation, écran/point noir Ha, flou de chroma,
   recadrage), note chaque rendu avec des métriques déterministes **et** un modèle de vision local, et
   garde le meilleur. Désactivé par défaut → une finition mono-passe identique au bit près.

Chaque traitement écrit ses sorties + un rapport JSON/Markdown ; avec l'API, le rapport complet
(notations par image, masters utilisés, détail de l'intégration) est stocké sur la tâche et affiché dans
la vue **Runs**/tâche.

Pour le chemin planétaire, la réutilisation inter-sessions et les réglages par mode, voir
[docs/pipeline.md](docs/pipeline.md).

### Le superviseur de finition IA (activable)

Une petite fonctionnalité entièrement locale qui traite la finition comme une boucle d'optimisation.
Démarrez le modèle une fois avec `just run-ia-model` (sert un modèle de vision MLX — Qwen2.5-VL par
défaut — sur `:1234`), puis activez-le par traitement : cochez **« Run with local AI agent »** sur la
page Import (deepsky/nebula uniquement), passez `process … --supervise`, ou `just refine <run-dir>` pour
réajuster un empilement existant sans réempiler. Il est **désactivé par défaut et à tolérance de
panne** : case décochée ou serveur éteint, la finition est identique au pipeline standard. Voir
[`.env.example`](.env.example) (`ASTRO_LLM_*`) pour les réglages.

---

## Comment le planificateur obtient ses données

Un *schéma fonctionnel* de la façon dont les trois pages du planificateur obtiennent leurs données —
étoiles, événements, météo, pollution lumineuse — quelle API sert chacune, d'où vient chaque valeur, et
ce qui est **mis en cache versus stocké**.

### Flux de données (UI ↔ moteur ↔ sources de données)

```mermaid
flowchart TB
    subgraph UI["Navigateur — pages du planificateur (:5173)"]
        PAGES["Tonight · GoTo · Calendar<br/>+ un site d'observation partagé<br/>(lat/lon/alt/tz)"]
    end

    subgraph ENG["moteur astrostack — GET /api/sky/* (:8080)"]
        SKY["targets · events · series · polar · align<br/>geocode · lightpollution (+tiles)<br/>darksites · weather (+grid)"]
    end

    subgraph SRC["D'où vient chaque valeur — aucune base de données"]
        EMB["① Catalogues embarqués / sur disque<br/>étoiles brillantes + pluies d'étoiles (go:embed)<br/>objets DSO Siril nom→coords (disque + en mémoire)"]
        CMP["② Calcul local, par requête<br/>planètes · Lune · éclipses · saisons<br/>visibilité/optique · passages SGP4 · polaire/horizon"]
        EXT["③ Flux externes — sans clé, tolérants aux pannes<br/>Open-Meteo · 7Timer! · NOAA SWPC · NASA GIBS VIIRS<br/>CelesTrak TLE · Minor Planet Center · OSM Nominatim"]
    end

    DISK[("Cache disque<br/>work/cache/&lt;domaine&gt;<br/>TTL + repli sur cache périmé hors-ligne")]
    PG[("Postgres<br/>tâches · images · masters<br/>— imagerie uniquement —")]

    PAGES -->|lat/lon · heure| SKY
    SKY --> EMB & CMP & EXT
    EXT <-->|cache| DISK
    SKY -. données célestes jamais persistées .-> PG
```

### L'API de données célestes (schéma fonctionnel)

Chaque valeur du planificateur est servie en lecture seule sous `GET /api/sky/*` (déclarée dans
`internal/api/api.go`) ; le navigateur transmet le site d'observation partagé (`lat`/`lon`/`time`) et le
moteur se ramifie vers la source de la dernière colonne. Les endpoints d'imagerie (`/api/inspect`,
`/api/jobs`, `/api/reuse/*`) relèvent du [flux de traitement ci-dessus](#comment-fonctionne-le-traitement),
pas d'ici.

| Endpoint (tous en `GET`) | Alimente | Source des données | Stockage local |
|----------------------|-------|-------------|-------------|
| `/api/sky/targets` | Tonight — cibles classées | catalogue DSO Siril (disque) + calcul visibilité/optique | catalogue en mémoire |
| `/api/sky/events` | Calendar — almanach sur une fenêtre | éphémérides + pluies embarquées + TLE CelesTrak + comètes MPC | cache disque |
| `/api/sky/series` | Calendar — les N prochains d'un type | identique à `events` | cache disque |
| `/api/sky/align` | GoTo — séquence d'étoiles d'alignement | `brightstars.csv` embarqué + calcul de dispersion maximale | embarqué |
| `/api/sky/polar` | Tonight → réticule polaire | géométrie du pôle céleste (calcul) | — |
| `/api/sky/lightpollution` | SQM/Bortle par site + scores de visibilité | API à clé → atlas → NASA GIBS VIIRS → défaut | mémoire + disque (~720 h) |
| `/api/sky/lightpollution/tiles/{z}/{x}/{y}` | Tuiles de calque cartographique | NASA GIBS VIIRS Black Marble (relayé par le serveur) | tuiles sur disque |
| `/api/sky/darksites` | Tonight → recherche de site sombre | pollution lumineuse × altitude Open-Meteo (calcul) | hérite des caches pollution lum. + altitude |
| `/api/sky/weather` | Tonight — panneau prévisions/seeing | Open-Meteo + 7Timer! ASTRO + NOAA SWPC (Kp) | mémoire + disque (~30 min) |
| `/api/sky/weather/grid` | Tonight — calque météo cartographique | grille multi-points Open-Meteo | cache disque |
| `/api/sky/geocode` | Sélecteur de site (adresse → coords) | OpenStreetMap Nominatim | — (par requête) |

Chaque flux est **sans clé par défaut et tolérant aux pannes** : un amont mort se rabat sur le cache
disque (même périmé), puis sur l'atlas hors-ligne / le calcul local / une valeur par défaut configurable
— ainsi aucun appel `/api/sky/*` ne renvoie d'erreur dure. La météo se dégrade par source (un flux mort
≠ panneau mort). Un fournisseur de pollution lumineuse calibré optionnel accepte une clé, lue
**côté serveur uniquement** (jamais l'UI, jamais journalisée).

### Ce qui est stocké en local — et ce qui ne l'est pas

**Dans Postgres** — le référentiel de l'imagerie (`pgx/v5` brut, migrations `*.up.sql` embarquées ; pas
d'ORM) :

- `sessions`, `frames`, `frame_metrics` — sessions de capture, catalogue par image, et notations par image.
- `master_frames` — l'*index* des masters de calibration (les blobs FITS eux-mêmes vivent sur disque
  dans `library/`).
- `jobs`, `outputs`, `finish_iterations` — traitements, leurs artefacts, et les étapes de finition
  supervisée.
- `targets` — nom canonique + coords d'un objet du ciel profond, utilisé **uniquement** pour regrouper
  les brutes en vue de la réutilisation inter-sessions. Alimenté par le chemin d'imagerie (noms d'objet
  des images), pas par le planificateur.

**Jamais dans Postgres** — chaque donnée du planificateur/céleste vit dans l'un des trois niveaux
hors-base :

- *Embarqué dans le binaire* (`go:embed`) — le catalogue d'étoiles brillantes d'alignement et la table
  des pluies d'étoiles filantes.
- *Calculé par requête* — visibilité des cibles, planètes/Lune/éclipses/saisons, passages de satellites
  (SGP4), géométrie polaire & horizon, et la recherche de site sombre par grille.
- *Récupéré depuis des flux sans clé, mis en cache sur disque* sous `work/cache/<domaine>` (TTL + repli
  sur cache périmé hors-ligne) — météo, pollution lumineuse (y compris les tuiles cartographiques
  VIIRS), altitude/horizon, éléments de comètes et TLE de satellites. Le géocodage se fait par requête
  (sans cache). Les catalogues DSO embarqués de Siril sont lus sur disque et mémoïsés en mémoire.

> En bref : **Postgres est le référentiel de l'imagerie ; le planificateur est sans état au-dessus de
> données publiques mises en cache.** Supprimez `work/cache/` et chaque valeur céleste est simplement
> re-récupérée ou recalculée — rien n'est perdu.

Tous les flux sont surchargeables dans `.env` (`ASTRO_WEATHER_*`, `ASTRO_LIGHTPOLLUTION_*`,
`ASTRO_ELEVATION_*`, plus les réglages de site `ASTRO_LAT`/`ASTRO_LON`/matériel) — voir
[Configuration](#configuration) plus bas.

---

## Configuration

Toute la configuration passe par `.env` (copiez depuis [`.env.example`](.env.example) ; le vrai `.env`
est ignoré par git — **ne committez jamais de secrets**). Les variables les plus importantes :

| Variable | Défaut | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://astro:astro@localhost:5432/astrostack?sslmode=disable` | DSN Postgres utilisé par le moteur hôte. |
| `API_ADDR` | `:8080` | Adresse d'écoute du serveur API. |
| `ASTRO_DATA_DIR` | `./data` | Racine que l'UI web peut parcourir pour les dossiers de captures. |
| `ASTRO_WORK_DIR` | `./work` | Espace de travail pour les FITS/séquences intermédiaires. |
| `ASTRO_OUTPUT_DIR` | `./output` | Où sont écrits les empilements finaux et les rapports. |
| `ASTRO_LIBRARY_DIR` | `./library` | Bibliothèque persistante de masters de calibration. |
| `SIRIL_BIN` | `/Applications/Siril.app/Contents/MacOS/siril-cli` | Siril sans interface. |
| `GIMP_BIN` | `/Applications/GIMP.app/Contents/MacOS/gimp-console-2.10` | GIMP sans interface (finition). |
| `FFMPEG_BIN` | `ffmpeg` | Binaire ffmpeg. |
| `GRAXPERT_BIN` / `STARNET_BIN` | `graxpert` / `starnet++` | Outils IA optionnels (résolus via `PATH`) ; absents → repli. |
| `ASTRO_LLM_URL` / `ASTRO_LLM_MODEL` | `http://127.0.0.1:1234/v1` / — | Serveur de modèle du superviseur de finition activable + id du modèle. |
| `ASTRO_MAX_CPUS` / `ASTRO_SIRIL_MEM_RATIO` | `10` / `0.5` | Plafonne les threads / la RAM de Siril pour qu'un gros empilement ne gèle pas l'hôte. |
| `ASTRO_SPCC_SENSOR` | `ZWO ASI1600MM` | Nom du capteur SPCC — **doit** correspondre exactement à la base de Siril. |
| `ASTRO_REUSE_ENABLED` | `true` | Réutilisation inter-sessions (accroître l'intégration + approfondir la calibration). |
| `VITE_API_BASE` | `http://localhost:8080` | URL de base de l'API utilisée par le frontend. |
| `ASTRO_LAT` / `ASTRO_LON` / `ASTRO_TIMEZONE` | Paris | Site d'observation par défaut du planificateur (surchargé en direct dans l'UI). |

`.env.example` documente l'ensemble complet, groupé : **outils hôte**, **outils IA + superviseur**,
**limites de ressources**, **résolution astrométrique + SPCC**, **site du planificateur + matériel +
oculaires**, **réutilisation inter-sessions**, **empilement en direct + S3**, et les services de données
sans clé **pollution lumineuse / site sombre / météo**. Les secrets (`HF_TOKEN`, clés S3, clés d'API
météo/pollution lumineuse) sont lus uniquement depuis l'environnement — jamais l'UI, jamais journalisés.

---

## Architecture

Le moteur s'exécute sur l'hôte (il sert d'intermédiaire avec Siril/GIMP installés sur l'hôte — la même
raison pour laquelle le MCP GIMP tourne sur l'hôte) ; Postgres tourne dans Compose. Pas de Redis : les
tâches s'exécutent dans un pool de workers in-process avec l'état dans Postgres et la progression en
direct via SSE.

- **`cmd/astrostack`** — CLI (`inspect`/`process`/`video`/`refine`) + serveur d'API HTTP (`serve`).
- **`cmd/siril-mcp`** — serveur MCP Siril (stdio) pour Claude ; partage le moteur `internal/`.
- **`internal/`** — `fits`, `inspect`, `siril`, `grade`, `calib`, `pipeline`, `planetary`,
  `postprocess`, `graxpert`, `starnet`, `llm` (le superviseur), `store`, `job`, `api`, `report` ;
  `astro`/`skycat`/`skyplan` (éphémérides · catalogue · visibilité) plus `skyevents`, `align`,
  `lightpollution`, `weather`, `elevation`, `darksky` alimentent les endpoints `/api/sky/*` du
  planificateur (voir [Comment le planificateur obtient ses données](#comment-le-planificateur-obtient-ses-données)).
- **`mcp-servers/gimp/`** — MCP GIMP vendorisé (Python ; l'outillage propre à GIMP — le seul Python du dépôt).
- **`frontend/`** — Vue 3 + Vite + Pinia + Tailwind + vue-i18n + ECharts + Leaflet.

La persistance est en **`pgx/v5` brut** avec des migrations SQL versionnées et embarquées appliquées par
`just migrate`. Voir [docs/architecture.md](docs/architecture.md) et [docs/pipeline.md](docs/pipeline.md)
pour le détail.

---

## Développement

- `just check` lance `go vet` + `golangci-lint` + `vue-tsc` et les suites de tests (reflète la barrière
  de pre-push).
- Les conventions de code maison vivent dans [`./conventions/`](conventions/) ; les règles propres au
  projet sont dans [`CLAUDE.md`](CLAUDE.md).
- **Les tests Go tournent sur l'hôte** (ils sollicitent `siril-cli` de l'hôte) ; démarrez Postgres
  d'abord avec `just up`.
- Les serveurs MCP `siril` (Go) et `gimp` (Python) sont déclarés dans `.mcp.json` pour Claude Code.
  `.mcp.json` exécute `./bin/siril-mcp`, donc construisez-le d'abord avec `just build-mcp` (inclus dans
  `just setup`).

---

## Docker & déploiement

AstroStack fonctionne en **deux modes depuis un seul `compose.yaml`**, sélectionnés par les *profils*
Compose. Le développement quotidien sous macOS garde le moteur sur l'hôte (l'exception hôte-moteur — les
Siril/GIMP natifs sont les plus rapides) ; un stack entièrement **conteneurisé** embarque tout dans
Docker pour des déploiements portables / serveur Linux, piloté par les recettes `just stack*`. Aucun
changement de code ne bascule entre les deux — chaque chemin d'outil est une variable d'env.

### Ce qui est conteneurisé

| Service | Profil | Image | Contient | Port (hôte) |
|---|---|---|---|---|
| `db` | *(toujours)* | `postgres:16-alpine` | Postgres 16 (tâches · catalogue d'images · index des masters) | `5432` |
| `engine` | `stack` | buildée · `docker/engine.Dockerfile` · **arch hôte** | Moteur Go (`serve`) + **Siril · GIMP 2.10 · GraXpert · ffmpeg · Python 3.12** | `8080` |
| `frontend` | `web`, `stack` | buildée · `docker/frontend.Dockerfile` | Build Vue sur nginx ; relaie `/api` → moteur | `8082` |
| `ai` | `ai` | `ollama/ollama` | Ollama servant un modèle de vision Qwen2.5-VL (GPU) | `11434` |
| `adminer` | `tools` | `adminer:4` | UI web Postgres | `8081` |

L'image `engine` est construite pour l'**architecture de l'hôte** (arm64 sur Apple Silicon, amd64 sur
Linux) afin que les outils tournent nativement. Siril n'a pas de build arm64 : sur arm64 c'est le paquet
de la distribution (Siril ~1.2.x) et sur amd64 l'**AppImage 1.4.3** épinglée — la version ne diffère que
pour la parité multi-sessions (voir la table par environnement). **StarNet++ n'est pas embarqué** (licence
non redistribuable) — montez votre installation et définissez `STARNET_BIN` pour l'activer ; sinon le
pipeline garde les étoiles complètes. Le **VLM du superviseur n'est jamais embarqué** — c'est un service
séparé et activable (ci-dessous).

### Quel mode selon l'environnement

| Environnement | Commande | Moteur + Siril/GIMP | Modèle IA (VLM) | À utiliser pour |
|---|---|---|---|---|
| **macOS — dev quotidien** | `just up` + `just dev` + `just web` | **natif sur l'hôte** (rapide) | mlx natif : `just run-ia-model` | tout — le flux normal |
| **macOS — tout conteneur** | `just stack` | conteneur (**arm64 natif**) — Siril/GIMP tournent nativement (Siril ~1.2.x de la distrib) | mlx natif sur l'hôte | un stack local complet ; amd64 ou mode hôte pour la parité 1.4.3 exacte |
| **Linux + GPU NVIDIA** | `just stack-ai` + `just ai-pull` | conteneur (**amd64 natif**) — traitement complet | conteneur (Ollama, GPU) | **100 % dockerisé**, VLM inclus |
| **Linux — sans GPU** | `just stack` | conteneur (amd64 natif) — traitement complet | ignoré, ou pointez vers un serveur compatible OpenAI | traitement headless sans GPU |

> **La seule limite macOS intrinsèque est le modèle IA :** Docker sous macOS est une VM Linux sans accès
> GPU/Metal, donc le modèle de vision ne peut pas tourner en conteneur — gardez-le natif
> (`just run-ia-model`). Le moteur et ses outils tournent nativement (arm64) ; la seule différence avec un
> déploiement Linux amd64 est la version de Siril (~1.2.x de la distrib vs l'AppImage 1.4.3), qui ne
> compte que pour la parité multi-sessions.

### Le modèle IA est découplé et activable

`just stack` ne télécharge ni ne démarre **jamais** le modèle de vision (~28 Go). Le moteur a seulement
besoin d'un serveur **compatible OpenAI** à `ASTRO_LLM_URL` (il n'embarque rien) ; choisissez un backend :

| Où | Backend | Mise en place |
|---|---|---|
| **macOS** | **mlx-vlm** natif (Metal) | `just run-ia-model` · `.env` : `ASTRO_LLM_URL=http://host.docker.internal:1234/v1`, `ASTRO_LLM_MODEL=mlx-community/Qwen2.5-VL-32B-Instruct-6bit` |
| **Linux + GPU** | conteneur **Ollama** | `just stack-ai` (ou `just ai-up`) puis `just ai-pull` · `.env` : `ASTRO_LLM_URL=http://ai:11434/v1`, `ASTRO_LLM_MODEL=qwen2.5vl:32b` · nécessite `nvidia-container-toolkit` |
| **partout** | tout serveur compatible OpenAI (LM Studio, vLLM…) | définissez `ASTRO_LLM_URL` + `ASTRO_LLM_MODEL` |

Le superviseur est activable par traitement et tolérant aux pannes : sans modèle, le stack effectue quand
même une finition normale.

### Recettes du stack

| Recette | Effet |
|---|---|
| `just stack-build` | Builde les images `engine` + `frontend` (sans modèle). |
| `just stack` | Lance db + moteur + frontend en Docker, **sans modèle** — UI `:8082`, API `:8080`. |
| `just stack-ai` | Idem **plus** le conteneur du modèle Ollama (Linux + GPU). |
| `just stack-down` | Arrête moteur + frontend + ai (laisse Postgres actif). |
| `just stack-logs` / `just engine-sh` | Suit les logs du moteur / ouvre un shell dans le conteneur moteur. |
| `just ai-up` / `just ai-pull` / `just ai-down` | Démarre / télécharge les poids / arrête le conteneur du modèle, indépendamment du stack. |

### Ports

| Port | Service | Mode |
|---|---|---|
| `5432` | Postgres | tous |
| `8080` | API moteur | hôte (`just dev`) **ou** conteneur (`just stack`) — un à la fois |
| `8082` | frontend (nginx) | `stack` / `web` |
| `11434` | VLM Ollama | `ai` (Linux + GPU) |
| `1234` | VLM mlx natif | hôte macOS (`just run-ia-model`) |
| `8081` | Adminer | `tools` |
| `5173` | serveur de dev Vite | hôte (`just web`) |

### Configuration du stack (`.env`)

Au-delà des variables du mode hôte, le stack conteneurisé lit (les chemins d'outils hôte comme
`SIRIL_BIN` sont **embarqués dans l'image du moteur** et ne s'appliquent pas ici) :

| Variable | Défaut | Description |
|---|---|---|
| `API_UPSTREAM` | `host.docker.internal:8080` | Cible `/api` de nginx ; `just stack` la met à `engine:8080`. |
| `ENGINE_PORT` | `8080` | Port hôte où l'API du moteur conteneurisé est publiée. |
| `UID` / `GID` | *(non défini → 10001)* | Linux : exécuter le moteur avec l'UID/GID propriétaire des dossiers de données (`id -u`/`id -g`). |

> **Vos données et traitements existants continuent de fonctionner.** Le moteur stocke des chemins
> **absolus** dans Postgres + `run.json`, donc le stack monte vos `./input`, `./library`, `./output`,
> `./work` à leurs **mêmes chemins hôte absolus** et s'exécute avec la racine du dépôt comme CWD. Les
> **masters de la Bibliothèque, les Runs, les Tâches et la réutilisation inter-sessions** se résolvent
> sans changement, et vous pouvez basculer librement entre le mode hôte et le stack. Gardez les captures
> sous `./input` (ou liez-les par symlink) et lancez `just stack` depuis la racine du dépôt ; `input` est
> monté en lecture seule.
| `ASTRO_LLM_URL` / `ASTRO_LLM_MODEL` | mlx hôte / — | Endpoint + id du modèle VLM (voir la table IA ci-dessus). |
| `OLLAMA_TAG` / `OLLAMA_PORT` | `0.6.8` / `11434` | Tag de l'image Ollama (vérifiez-en un pour votre pilote) + port. |

Les données de capture vivent dans les montages `input/`/`library/`/`output/` ; le scratch est un volume
`work` et le cache du modèle le volume `ollama`. Voir [docs/architecture.md](docs/architecture.md) →
*Fully containerized mode* pour la conception et les compromis.

---

## Licence

MIT
