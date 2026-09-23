---
title: Committen
category: Werken met wijzigingen
order: 31
summary: Boodschapstijlen, sjablonen, co-auteurs en de linter.
keywords: commit boodschap message composer conventional gitmoji ticket amend sjabloon template co-author linter undo ongedaan maken reset
---

# Committen

## Boodschapstijlen

Kies er een in de instellingen; de opsteller past zich eraan aan.

| Stijl | Ziet eruit als |
|---|---|
| **Conventional** | `feat(api)!: add rate limiting` — met een typekeuzelijst |
| **Gitmoji** | `✨ add rate limiting` — met een emoji-kiezer |
| **Ticket** | `ABC-123: add rate limiting` — voorgevuld vanuit de branchnaam |
| **Plain** · **Auto** | Wat je maar typt; bij Auto bepaalt de AI de vorm |
| **Caveman** · **Haiku** | Precies wat je erbij voorstelt |

![Opsteller voorgevuld vanuit een commitsjabloon](../../screenshots/commit-template.webp)

## Wat de opsteller voor je doet

- <kbd>↑</kbd> <kbd>↓</kbd> haalt je **recente boodschappen** terug.
- Een **co-auteurkiezer** voegt `Co-authored-by:`-trailers toe uit de eigen
  bijdragers van de repository.
- `commit.template` / `.gitmessage` **vult de boodschap voor**, met de
  commentaarregels eruit gestript.
- Tijdens een merge, cherry-pick of revert is de boodschap **voorgevuld** zoals
  git dat zou doen.
- Concepten **blijven bewaard** per repository, dus van tabblad wisselen kost je
  nooit een boodschap.

## De linter

Een live, niet-blokkerende controle: lengte van de onderwerpsregel (met een
tekenteller), een punt aan het eind, een niet-gebiedende of met kleine letter
beginnende onderwerpsregel, te brede regels in de body. Hints, nooit een hek —
het houdt je niet tegen bij het committen.

## Amend

Amend herschrijft de laatste commit met wat er gestaged staat. Gitcito laat je
eerst de bestaande boodschap zien, zodat je bewerkt in plaats van overtypt.

**Commit aanpassen…** op een grafiekrij doet hetzelfde voor HEAD: het laadt de
volledige boodschap, zet de opsteller in de amend-stand en geeft hem focus. Een
HEAD die al gepusht is kan nog steeds worden aangepast, maar Gitcito waarschuwt
dat het bijwerken van de remote een force push zal kosten.

### De commit van iemand anders aanpassen

Een amend behoudt **de auteur en de auteursdatum** van de oorspronkelijke commit — een regel van git, niet van Gitcito. Dat klopt als je een typfout in de commit van een collega herstelt, en is fout als je er eigen nieuw werk in vouwt: het resultaat wordt aan hen toegeschreven, en de graaf toont hun naam bij code die ze nooit schreven.

Daarom zegt de composer, als het auteurs-e-mailadres van HEAD afwijkt van je `user.email`, van wie de commit is die je aanpast, en biedt **Maak mij de auteur** aan. Aangevinkt past hij aan met `--reset-author`: jij wordt de auteur, gedateerd op nu. Laat het uit om hun naam te houden.

![Amend-modus op een commit van iemand anders](../../screenshots/amend-author.webp)

Er worden e-mailadressen vergeleken, dus iemand die commit als `Elisa` en als `elisa` telt als dezelfde auteur. Alleen als een e-mailadres ontbreekt valt het terug op de naam, en het zwijgt als de repository helemaal geen identiteit heeft ingesteld. Alleen de aangepaste commit wordt gecontroleerd: een latere cherry-pick of rebase die hem elders neerzet, houdt de auteur die hij dan heeft.

⌘Z na een amend zet de aangepaste commit terug, met je nieuwe wijzigingen nog gestaged zoals vóór de amend — het gaat niet terug naar de ouder van die commit.

**Commit ongedaan maken…** is de tegenhanger voor een niet-gepushte HEAD: een
mixed reset naar de ouder, de wijzigingen in de werkboom blijven behouden, en de
boodschap komt terug in de opsteller. De eerste commit heeft een eigen pad dat
een ongeboren branch achterlaat in plaats van de bestanden te vernietigen.

**Zie ook:** [Stagen](staging.md) · [Absorb](absorb.md) · [Changeloggenerator](changelog.md)
