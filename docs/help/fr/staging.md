---
title: Indexation
category: Travailler sur les changements
order: 30
summary: Indexer des fichiers entiers, des sections isolées ou des lignes individuelles.
keywords: indexation staging indexer stage désindexer unstage abandonner discard section hunk lignes lines index partiel partial
---

# Indexation

Le panneau de commit contient trois listes : **En conflit**, **Non indexé** et
**Indexé**. Chacune se replie, et chacune se souvient si vous l'aviez laissée
ouverte.

![Un diff non indexé, avec les contrôles de section et de fichier à côté](../../screenshots/line-staging.webp)

## Trois niveaux de précision

| Niveau | Comment |
|---|---|
| **Fichier** | Cliquez le ✚ sur la ligne, ou sélectionnez plusieurs lignes et indexez le tout |
| **Section** | Ouvrez le diff et utilisez le bouton sur l'en-tête de la section |
| **Ligne** | Survolez une ligne modifiée dans le diff et cliquez sur son **+**, ou sélectionnez plusieurs lignes et indexez-les |

L'indexation par ligne est ce qui rend praticable de tenir un `console.log` de
débogage hors d'un commit sans avoir à le supprimer d'abord.

## Indexer une seule ligne

Ouvrez le diff d'un fichier non indexé, en vue unifiée ou côte à côte. Survolez
une ligne ajoutée ou supprimée : un petit **+** vert apparaît à son début. Un
clic indexe cette ligne et rien d'autre. Le reste du bloc reste non indexé,
exactement comme si vous aviez édité le bloc à la main dans `git add -p`.

Pour plusieurs lignes à la fois, cliquez sur les lignes elles-mêmes pour les
sélectionner — <kbd>⇧</kbd>-clic prend toutes les lignes modifiées depuis la
dernière cliquée — puis appuyez sur **Indexer N ligne(s)** dans la barre
au-dessus du diff.

Cela marche aussi dans l'autre sens. Ouvrez la version **indexée** d'un fichier
et les contrôles deviennent un **−** rouge, **Désindexer le bloc** et
**Désindexer N ligne(s)** : ils retirent des lignes de l'index sans toucher à la
copie de travail.

Retirez le dernier changement du côté affiché et le diff suit le fichier de
l'autre côté, au lieu de rester vide.

Chaque ligne ou bloc indexé ou désindexé ainsi s'annule avec **Annuler** dans
la barre d'outils, un clic à la fois.

| Vous choisissez | L'indexer | La désindexer |
|---|---|---|
| Une ligne ajoutée | L'index gagne cette ligne | L'index perd cette ligne |
| Une ligne supprimée | L'index perd cette ligne | La ligne revient dans l'index |
| Aucune des deux, dans le même bloc | Reste telle quelle, dans la copie de travail seulement | Reste indexée |

### Limites

- **Espaces masqués, pas d'indexation.** Tant que les *Espaces* sont ignorés, le
  diff omet des changements et ne peut pas dire quelles lignes indexer ; les
  contrôles se cachent jusqu'à ce que vous le désactiviez.
- **Les fichiers non suivis** s'indexent en entier. Indexez d'abord le fichier,
  puis désindexez les lignes dont vous ne voulez pas.
- **La dernière ligne d'un fichier sans saut de ligne final** peut être refusée
  si vous séparez son changement des lignes ajoutées après elle : git ne sait
  pas décrire cet état intermédiaire sous forme de patch. Indexez-les ensemble.
- **Annuler suppose l'index tel que vous l'avez laissé.** Si vous avez indexé
  d'autres changements sur les mêmes lignes depuis, git refuse l'annulation
  plutôt que de deviner, et le dit.

## Abandonner

L'abandon fonctionne aux mêmes niveaux, et demande toujours confirmation. Les
fichiers non suivis sont supprimés ; les fichiers suivis reviennent à leur état
indexé (ou validé).

## Clavier

<kbd>↑</kbd> <kbd>↓</kbd> (ou <kbd>j</kbd> <kbd>k</kbd>) parcourent les listes de
fichiers, avec <kbd>⇧</kbd> pour une plage et <kbd>⌘</kbd>/<kbd>Ctrl</kbd> pour
basculer des fichiers individuels.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> étend la sélection depuis la dernière
ligne cliquée. Un clic droit sur la sélection permet d'indexer, de désindexer,
de remiser ou d'annuler tout d'un coup.

## Copier les chemins

Un clic droit sur un fichier non validé propose **Copier le chemin du fichier**
(absolu, avec les séparateurs de la plateforme) et **Copier le chemin relatif
du fichier** (`src/index.ts`, sans `./` initial). Plusieurs fichiers
sélectionnés copient un chemin par ligne, dans l'ordre de la liste. Les fichiers
supprimés restent disponibles : ces actions ne copient que du texte. Les
dossiers copient toujours le chemin du dossier.

## Avant de valider

Gitcito vérifie quelques points et demande une fois, jamais en silence :

- un fichier qui ressemble à un **secret** (`.env`, `*.pem`, `id_rsa`…),
- un blob **très volumineux** (seuil dans Réglages → Sécurité),
- un commit **directement sur une branche protégée** (`main`/`master` par
  défaut).

Chacun de ces cas propose un *Ignorer et ne plus suivre* en un clic. Voir
[Sécurité et secrets](security.md).

**Voir aussi :** [Faire des commits](committing.md) · [Diffs](diffs.md) · [Absorption](absorb.md)
