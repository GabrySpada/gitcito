---
title: Le graphe des commits
category: Dépôt et historique
order: 10
summary: Lire l'histoire : couloirs, références, colonnes, filtres et sélection multiple.
keywords: graphe graph historique history commits couloirs lanes branches fusions merges colonnes columns filtre filter linéaire linear first-parent amender amend annuler undo réinitialisation reset github
---

# Le graphe des commits

Branches, fusions et fusions pieuvre dessinées correctement, en clair comme en
sombre. Le rendu est fenêtré : un dépôt de cent mille commits défile comme un
dépôt d'une centaine.

| | |
|---|---|
| ![Graphe des commits, thème clair](../../screenshots/graph-light.webp) | ![Graphe des commits, thème sombre](../../screenshots/graph-dark.webp) |

## Se déplacer

- <kbd>↑</kbd> <kbd>↓</kbd> (ou <kbd>j</kbd> <kbd>k</kbd>) déplacent la
  sélection.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>-clic fait basculer un commit dans une **sélection
  multiple**, en partant du commit déjà sélectionné ; <kbd>⇧</kbd>-clic prend une
  plage. Avec plusieurs commits sélectionnés, un clic droit sur l'un d'eux permet
  de les cherry-picker sur la branche courante, d'exporter un patch combiné
  unique, ou de copier leurs SHA.
- **Écraser (squash)** figure toujours dans ce menu, mais ne s'exécute que si la
  sélection est la pointe de la branche courante et les commits juste en
  dessous, sans trou — c'est un reset soft vers le parent du plus ancien. Les
  lignes de stash intercalées ne comptent pas. Sinon l'entrée est grisée ;
  survolez-la pour en voir la raison. Le cas habituel : des commits d'une
  branche qui n'est pas extraite — basculez d'abord dessus. Pour des commits
  plus bas, passez par le [rebase interactif](rebase.md). Les hooks de commit ne
  s'exécutent pas, comme lors d'un rebase : les commits existent déjà, et un
  hook en échec ne peut plus laisser la branche à moitié réinitialisée.
- Les commits arrivés lors de votre **dernier fetch ou pull** sont signalés comme
  nouveaux. Ceux qui ne font pas encore partie de la branche active restent
  légèrement translucides jusqu'à ce qu'un pull les intègre.
- Clic droit sur un commit pour **Amender**, **Annuler**, **Réinitialiser au
  commit…** et **Voir sur GitHub**, en plus du checkout, du cherry-pick, du
  revert, de la branche, de l'étiquette et de la copie. Les actions risquées
  restent visibles et se désactivent.

## Lui faire montrer ce que vous voulez

- La **focalisation du graphe** décide de la quantité d'historique dessinée —
  Réglages → Thèmes → **Graphe**, ou le menu engrenage de l'en-tête du graphe.
  *Tout* dessine l'ensemble ; *Historique linéaire* (premier parent) ne laisse
  que le tronc ; *Masquer les branches fusionnées* garde le tronc plus les
  branches encore non fusionnées ; *Mode solo* garde votre branche, vos branches
  favorites et la branche par défaut.

  Elle ne filtre que ce que le journal a déjà chargé. *Masquer les branches
  fusionnées* s'appuie sur la réponse de git à « déjà contenue dans la branche
  courante » : changer de branche change donc ce qui est masqué — et ce mode
  garde tout commit encore pointé par une étiquette ou une référence qu'il ne
  reconnaît pas, c'est-à-dire précisément ce que laisse une branche supprimée.
  *Historique linéaire* et *Mode solo* sont plus brutaux : une étiquette ou une
  remise posée sur un commit qu'ils masquent disparaît avec lui.

- **Filtrer par chemin** : clic droit sur un fichier ou un dossier → *Filtrer le
  graphe par ce chemin*, et seuls les commits qui l'ont touché restent allumés.

![Graphe réduit à un seul chemin par un filtre](../../screenshots/graph-path-filter.webp)

- **Colonnes** : afficher, masquer, redimensionner et réordonner les colonnes
  branche, message, auteur, date, SHA, signature et déploiement.
- **Style** : Réglages → Thèmes → **Graphe** — palette de couloirs (8 intégrées,
  personnalisée, ou générée par l'IA), style des angles, densité des lignes et
  épaisseur des traits, avec un aperçu en direct sous forme de mini-graphe.

![Les réglages de style du graphe avec aperçu en direct](../../screenshots/settings-graph.webp)

## Séparateurs de date

En parcourant un long historique, la question n'est presque jamais l'horodatage
exact d'un commit — la colonne de date y répond déjà. C'est « où en suis-je, à peu
près ». Les séparateurs de date y répondent : un filet à travers le graphe avec
une étiquette relative à droite, marquant la fin d'une tranche d'historique et le
début d'une plus ancienne.

Les tranches s'élargissent avec la distance — aujourd'hui, hier, quelques jours,
une semaine, des semaines, des mois, des années. C'est tout l'intérêt : un trait à
chaque changement de jour apparaîtrait sous presque chaque commit d'un dépôt actif
et nulle part dans un dépôt calme.

![Séparateurs de date marquant les tranches d'historique dans le graphe](../../screenshots/graph-date-dividers.webp)

Un séparateur se place **en bas** de la dernière ligne de sa tranche et porte le
nom de la tranche qu'il referme : il décrit donc les lignes au-dessus de lui. La
dernière ligne affichée n'en porte pas — sa tranche peut se prolonger dans des
commits pas encore chargés.

Les limites. Les commits sont listés en `--date-order`, qui place une fusion
au-dessus des commits qu'elle fusionne : une ligne plus récente que la tranche où
elle tombe rejoint cette tranche au lieu d'en ouvrir une nouvelle, si bien que les
séparateurs vont toujours du récent vers l'ancien. La ligne des modifications non
validées et les remises sont ignorées, car leur date n'est pas leur place dans
l'historique. Et il n'y a pas de réglage : les séparateurs sont toujours actifs.

## Détails d'un commit

Sélectionner un commit affiche ses fichiers modifiés (en arbre ou à plat),
l'auteur, le SHA, les co-auteurs et sa signature. Les références `#123` et les
`@mentions` sont automatiquement liées à votre hébergeur.

Au-dessus de la liste des fichiers, un **résumé des changements** décompose le
commit par type au lieu de donner un seul total — *5 modifiés*, *1 ajouté*,
*1 supprimé*, plus *renommés* et *en conflit* quand le commit en contient. Chacun
reprend la couleur du glyphe d'état des lignes en dessous et des compteurs sur les
dossiers repliés : la même couleur veut donc dire la même chose partout dans le
panneau. Les types sans fichier disparaissent complètement, si bien qu'une
modification ordinaire affiche une entrée et non cinq. Survolez le résumé pour
obtenir le total « *n* fichiers modifiés ».

![Résumé des changements au-dessus de la liste de fichiers d'un commit : 6 modifiés, 2 ajoutés, 1 supprimé, 1 renommé](../../screenshots/change-summary.webp)

Deux choses qu'il ne vous dit délibérément pas. Il compte **les fichiers, pas les
lignes** : une correction d'un caractère et une réécriture valent toutes deux
*1 modifié* ; c'est le diff qui montre l'ampleur du changement. Et il compte tous
les fichiers du commit, pas seulement ceux que retient un filtre ou une recherche
active.

Le même résumé coiffe le panneau de préparation et la liste de fichiers d'un
stash. Dans le panneau de préparation, les fichiers non suivis comptent comme des
ajouts : *ajouté* signifie donc « absent du dernier commit », et non « déjà
préparé ».

La liste de fichiers se sélectionne en groupe avec les gestes habituels (clic
<kbd>⌘</kbd>/<kbd>Ctrl</kbd>, clic <kbd>⇧</kbd>,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Clic droit sur la sélection →
*Restaurer {n} fichiers dans l'arbre de travail* reprend ces fichiers
exactement tels que ce commit les avait : après une seule confirmation, les
copies de travail sont écrasées, sans toucher ni HEAD ni l'index.

![Parcours des détails d'un commit](../../screenshots/clip-commit-details.webp)

**Voir aussi :** [Blame et historique de fichier](blame.md) · [Recherche](search.md) · [Machine à remonter le temps](time-machine.md)
