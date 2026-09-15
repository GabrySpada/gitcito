---
title: Aan de slag
category: Begin hier
order: 1
summary: Open een repository, lees de grafiek, maak je eerste commit.
keywords: intro eerste stappen openen klonen clone tabbladen tabs grafiek graph commit
---

# Aan de slag

Gitcito opent een map en toont je zijn geschiedenis. Er wordt niets naar je
repository geschreven tot je erom vraagt.

![Een net geopende repository, nog zonder commits](../../screenshots/empty-repo.webp)

## Een repository openen

- **Sleep een map** op het venster, of gebruik **Repository openen** op het
  welkomstscherm.
- **Kloon** er een vanaf een URL of rechtstreeks bij je host — zie
  [klonen](cloning.md) voor de opties die een enorme repository snel kloonbaar
  maken.
- Vanuit een terminal opent `gitcito .` de huidige map in de draaiende app — zie
  [de commandoregel](cli.md).
- Een map die nog geen Git-repository is opent evengoed, en biedt aan hem te
  initialiseren.

## De drie panelen

| Paneel | Wat erin zit |
|---|---|
| Links | Branches, remotes, tags, stashes, worktrees — en het tabblad **Bestanden** voor de werkboom |
| Midden | De commitgrafiek, en wat je daaruit selecteert |
| Rechts | De commitopsteller, of de details van de geselecteerde commit |

## De rest vinden

Twee routes, en ze leiden naar dezelfde plekken:

- **`⌘K`** (`Ctrl+K`) — het commandopalet. Typ wat je wilt; het springt ook naar
  branches, commits en bestanden.
- **Tools** in de werkbalk — dezelfde repositorygebonden verzameling als menu,
  met de lange staart opgevouwen in groepen zodat het leesbaar blijft.

![Het Tools-menu: de veelgebruikte tools eerst, de rest gegroepeerd](../../screenshots/tools-menu.webp)

De actiebalk houdt zijn knoppen in het midden van het **venster**, niet in de ruimte tussen de repositorynaam en het zoekvak — ze blijven dus op dezelfde plek terwijl je wisselt tussen repositories met namen van heel verschillende lengte. Repository- en branchnamen staan er volledig; een naam die zo lang wordt dat hij de rest van de balk bedreigt, eindigt in puntjes, met de hele naam in de tooltip van de knop.

De ruimte wordt vanaf dat midden naar buiten gemeten, en dat is wat het kost om het vast te houden: wordt het venster smal, of de repositorynaam heel lang, dan geeft de balk ruimte af in plaats van te verschuiven. Het zoekveld geeft als eerste toe en ruilt zijn vak voor een loep — klik erop om te zoeken; hij blijft open zolang er een filter actief is. Daarna vouwen de knoppen die niet meer passen samen in een menu **Meer** aan het eind van de balk, in de volgorde van de balk en met hun submenu’s. Maak het venster breder en ze komen terug.

![De actiebalk in een smal venster: de zoekfunctie ingeklapt tot een loep, het eind van de balk in ‘Meer’, en de knoppen nog steeds gecentreerd](../../screenshots/toolbar-narrow.webp)

Alles wat via de een bereikbaar is, is via de ander bereikbaar, dus er is niets
dat alleen ingewijden kunnen vinden.

## Je eerste commit

1. Bewerk een bestand. Het verschijnt onder **Niet gestaged**.
2. Stage het — het hele bestand, een hunk, of [losse regels](staging.md).
3. Schrijf een boodschap en druk op **Committen**.

Al het andere in Gitcito is optioneel.

