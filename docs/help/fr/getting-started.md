---
title: Premiers pas
category: Pour commencer
order: 1
summary: Ouvrir un dépôt, lire le graphe, faire son premier commit.
keywords: intro premiers pas débuter ouvrir cloner clone onglets tabs graphe graph commit valider
---

# Premiers pas

Gitcito ouvre un dossier et vous montre son historique. Rien n'est écrit dans
votre dépôt tant que vous ne le demandez pas.

![Un dépôt fraîchement ouvert, encore sans aucun commit](../../screenshots/empty-repo.webp)

## Ouvrir un dépôt

- **Glissez un dossier** sur la fenêtre, ou utilisez **Ouvrir un dépôt** depuis
  l'écran d'accueil.
- **Clonez-en un** depuis une URL ou directement depuis votre hébergeur — voir
  [le clonage](cloning.md) pour les options qui rendent rapide le clonage d'un
  dépôt énorme.
- Depuis un terminal, `gitcito .` ouvre le dossier courant dans l'application
  déjà lancée — voir [la ligne de commande](cli.md).
- Un dossier qui n'est pas encore un dépôt Git s'ouvre quand même, en vous
  proposant de l'initialiser.

## Les trois panneaux

| Panneau | Ce qu'il contient |
|---|---|
| Gauche | Branches, distants, étiquettes, remisages, arbres de travail — et l'onglet **Fichiers** pour la copie de travail |
| Milieu | Le graphe des commits, et ce que vous y sélectionnez |
| Droite | Le compositeur de commit, ou les détails du commit sélectionné |

## Trouver tout le reste

Deux chemins, et ils mènent aux mêmes endroits :

- **`⌘K`** (`Ctrl+K`) — la palette de commandes. Tapez ce que vous voulez ; elle
  saute aussi vers les branches, les commits et les fichiers.
- **Outils** dans la barre d'outils — le même ensemble, à l'échelle du dépôt,
  présenté comme un menu, avec la longue traîne repliée en groupes pour rester
  lisible.

![Le menu Outils : les outils fréquents d'abord, le reste groupé](../../screenshots/tools-menu.webp)

La barre d’actions garde ses boutons au milieu de la **fenêtre**, et non dans l’espace laissé entre le nom du dépôt et le champ de recherche : ils restent donc au même endroit quand vous passez d’un dépôt à l’autre, quelle que soit la longueur des noms. Les noms de dépôt et de branche s’affichent en entier ; celui qui devient long au point de menacer le reste de la barre est tronqué, le nom complet passant dans l’infobulle du bouton.

La place est mesurée depuis ce milieu vers les bords, et c’est le prix à payer pour le tenir : quand la fenêtre se resserre, ou que le nom du dépôt est très long, la barre cède du terrain plutôt que de glisser. Le champ de recherche cède le premier et échange sa boîte contre une loupe — cliquez dessus pour chercher, elle reste ouverte tant qu’un filtre est actif. Ensuite, les boutons qui ne tiennent plus se replient dans un menu **Plus** à l’extrémité de la barre, dans l’ordre de la barre et avec leurs sous-menus. Élargissez la fenêtre et ils ressortent.

![La barre d’actions dans une fenêtre étroite : la recherche repliée en une loupe, la fin de la barre dans « Plus » et les boutons toujours centrés](../../screenshots/toolbar-narrow.webp)

Tout ce qui est atteignable par l'un l'est par l'autre : il n'y a donc rien que
seuls les utilisateurs avancés puissent trouver.

## Votre premier commit

1. Modifiez un fichier. Il apparaît sous **Non indexé**.
2. Indexez-le — le fichier entier, une section, ou [ligne par ligne](staging.md).
3. Écrivez un message et appuyez sur **Commit**.

Tout le reste dans Gitcito est optionnel.

