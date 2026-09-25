---
title: Stagen
category: Werken met wijzigingen
order: 30
summary: Stage hele bestanden, losse hunks of afzonderlijke regels.
keywords: stagen staging stage unstage verwerpen discard hunk regels lines index gedeeltelijk
---

# Stagen

Het commitpaneel heeft drie lijsten: **Conflicterend**, **Niet gestaged** en
**Gestaged**. Elke lijst klapt in, en elke onthoudt of je hem open liet staan.

![Een niet-gestagede diff, met de knoppen voor hunk en bestand ernaast](../../screenshots/line-staging.webp)

## Drie niveaus van precisie

| Niveau | Hoe |
|---|---|
| **Bestand** | Klik de ✚ op de rij, of selecteer meerdere rijen en stage de hele boel |
| **Hunk** | Open de diff en gebruik de knop in de hunkkop |
| **Regel** | Beweeg over een gewijzigde regel in de diff en klik op zijn **+**, of selecteer meerdere regels en stage die |

Regels stagen is wat het praktisch maakt om een `console.log` voor debugwerk
buiten een commit te houden zonder hem eerst te verwijderen.

## Losse regels stagen

Open de diff van een niet-gestagede file, in de samengevoegde of de gesplitste
weergave. Beweeg over een toegevoegde of verwijderde regel en aan het begin
verschijnt een klein groen **+**: één klik staget die regel en verder niets. De
rest van de hunk blijft ongestaged, precies alsof je de hunk met de hand had
bewerkt in `git add -p`.

Voor meer regels tegelijk klik je op de regels zelf om ze te selecteren —
<kbd>⇧</kbd>-klik neemt elke gewijzigde regel vanaf de laatst aangeklikte — en
druk je op **N regel(s) stagen** in de balk boven de diff.

Het werkt ook andersom. Open de **gestagede** versie van een bestand en de
knoppen worden een rood **−**, **Hunk unstagen** en **N regel(s) unstagen**: ze
halen regels weer uit de index en laten de werkmap met rust.

Haal je de laatste wijziging weg van de kant die je bekijkt, dan volgt de diff
het bestand naar de andere kant in plaats van leeg te blijven.

Elke regel of hunk die je zo staget of unstaget, draai je terug met **Ongedaan
maken** in de werkbalk, één klik per keer.

| Je kiest | Stagen | Unstagen |
|---|---|---|
| Een toegevoegde regel | De index krijgt die regel erbij | De index raakt die regel kwijt |
| Een verwijderde regel | De index raakt die regel kwijt | De regel komt terug in de index |
| Geen van beide, in dezelfde hunk | Blijft zoals hij is, alleen in de werkmap | Blijft gestaged |

### Beperkingen

- **Witruimte verborgen, geen staging.** Zolang *Witruimte* wordt genegeerd,
  laat de diff wijzigingen weg en kan hij niet zeggen welke regels te stagen;
  de knoppen verdwijnen tot je het uitzet.
- **Niet-gevolgde bestanden** gaan in hun geheel. Stage eerst het bestand en
  unstage dan de regels die je niet wilt.
- **De laatste regel van een bestand zonder afsluitende regeleinde** kan worden
  geweigerd als je zijn wijziging scheidt van regels die erna zijn toegevoegd —
  git kan die halve toestand niet als patch beschrijven. Stage ze samen.
- **Ongedaan maken vraagt de index zoals je hem achterliet.** Heb je sindsdien
  meer van dezelfde regels gestaged, dan weigert git het terugdraaien in plaats
  van te gokken, en zegt dat.

## Verwerpen

Verwerpen werkt op dezelfde niveaus, en vraagt altijd. Untracked bestanden worden
verwijderd; getrackte gaan terug naar hun gestagede (of gecommitte) toestand.

## Toetsenbord

<kbd>↑</kbd> <kbd>↓</kbd> (of <kbd>j</kbd> <kbd>k</kbd>) lopen door de
bestandslijsten, met <kbd>⇧</kbd> voor een reeks en <kbd>⌘</kbd>/<kbd>Ctrl</kbd>
om losse bestanden aan of uit te zetten.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> breidt de selectie uit vanaf de laatst
aangeklikte rij. Rechtsklik op de selectie om alles erin in één keer te stagen,
unstagen, stashen of te verwerpen.

## Paden kopiëren

Rechtsklik op een niet-gecommit bestand voor **Bestandspad kopiëren**
(absoluut, met de scheidingstekens van het platform) en **Relatief bestandspad
kopiëren** (`src/index.ts`, zonder `./` vooraan). Meerdere geselecteerde
bestanden kopiëren één pad per regel, in lijstvolgorde. Verwijderde bestanden
blijven beschikbaar — die acties kopiëren alleen tekst. Mappen kopiëren nog
steeds het mappad.

## Voor je commit

Gitcito controleert een paar dingen en vraagt één keer, nooit in stilte:

- een bestand dat op een **geheim** lijkt (`.env`, `*.pem`, `id_rsa`…),
- een **erg grote** blob (drempel in Instellingen → Beveiliging),
- **rechtstreeks naar een beschermde branch** committen (`main`/`master`
  standaard).

Elk daarvan biedt een *Negeren & untracken* in één klik. Zie
[Beveiliging & geheimen](security.md).

**Zie ook:** [Committen](committing.md) · [Diffs](diffs.md) · [Absorb](absorb.md)
