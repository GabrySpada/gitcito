---
title: De commitgrafiek
category: Repository & geschiedenis
order: 10
summary: Geschiedenis lezen: banen, refs, kolommen, filters en meervoudige selectie.
keywords: grafiek graph geschiedenis history commits banen lanes branches merges kolommen filter lineair first-parent amend aanpassen undo ongedaan maken reset github
---

# De commitgrafiek

Branches, merges en octopus-merges netjes getekend, licht of donker. Het renderen
gebeurt in een venster, dus een repository met honderdduizend commits scrollt als
een met honderd.

| | |
|---|---|
| ![Commitgrafiek, licht](../../screenshots/graph-light.webp) | ![Commitgrafiek, donker](../../screenshots/graph-dark.webp) |

## Rondbewegen

- <kbd>↑</kbd> <kbd>↓</kbd> (of <kbd>j</kbd> <kbd>k</kbd>) verplaatsen de selectie.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>-klik zet een commit aan of uit in een
  **meervoudige selectie**, te beginnen bij de al geselecteerde commit;
  <kbd>⇧</kbd>-klik pakt een reeks. Met meerdere geselecteerd rechtsklik je op
  een ervan om ze op de huidige branch te cherry-picken, één gecombineerde patch
  te exporteren of hun SHA's te kopiëren.
- **Squash** staat altijd in dat menu, maar werkt alleen als de selectie de top
  van de uitgecheckte branch is plus de commits direct eronder, zonder gaten —
  het is een soft reset naar de parent van de oudste. Stash-rijen ertussen
  tellen niet mee. Anders is het grijs; beweeg erover voor de reden. Meestal
  gaat het om commits van een branch die niet uitgecheckt is: check die eerst
  uit. Voor commits verder omlaag gebruik je [interactieve rebase](rebase.md).
  Commit-hooks draaien niet, net als bij een rebase: de commits bestaan al, en
  een falende hook kan de branch niet meer half gereset achterlaten.
- Commits die bij je **laatste fetch of pull** binnenkwamen zijn gemarkeerd als
  nieuw. Die nog niet in de uitgecheckte branch zitten blijven licht
  doorschijnend tot een pull ze binnenhaalt.
- Rechtsklik een commit voor **Aanpassen**, **Ongedaan maken**, **Resetten naar
  commit…** en **Bekijken op GitHub**, plus checkout, cherry-pick, revert,
  branch, tag en kopiëren. Onveilige acties blijven zichtbaar en worden
  uitgeschakeld.

## Het laten tonen wat jij wilt

- De **graaffocus** bepaalt hoeveel geschiedenis wordt getekend — Instellingen →
  Thema's → **Graaf**, of het tandwielmenu in de kop van de graaf. *Alles* tekent
  alles; *Lineaire geschiedenis* (first-parent) laat alleen de stam over;
  *Samengevoegde branches verbergen* houdt de stam plus de branches die nog niet
  zijn samengevoegd; *Solomodus* houdt jouw branch, je favoriete branches en de
  standaardbranch.

  Het filtert alleen wat het log al heeft geladen. *Samengevoegde branches
  verbergen* volgt gits eigen antwoord op "zit al in de huidige branch", dus van
  branch wisselen verandert wat verdwijnt — en het houdt elke commit waar nog een
  tag of een onbekende ref naar wijst, precies wat een verwijderde branch
  achterlaat. *Lineaire geschiedenis* en *Solomodus* zijn botter: een tag of een
  stash op een commit die zij verbergen, verdwijnt mee.

- **Filteren op pad**: rechtsklik een bestand of map → *Grafiek filteren op dit
  pad*, en alleen de commits die het aanraakten blijven verlicht.

![Grafiek teruggefilterd tot één pad](../../screenshots/graph-path-filter.webp)

- **Kolommen**: toon, verberg, herschaal en herschik de kolommen voor branch,
  boodschap, auteur, datum, SHA, handtekening en deployment.
- **Stijl**: Instellingen → Thema's → **Grafiek** — baanpalet (8 ingebouwde,
  eigen, of door AI gegenereerd), hoekstijl, rijdichtheid en lijndikte, met een
  live minigrafiek als voorbeeld.

![Grafiekstijlinstellingen met live voorbeeld](../../screenshots/settings-graph.webp)

## Datumscheidingen

Bij het scrollen door een lange geschiedenis is de vraag zelden het exacte
tijdstip van een commit — daar gaat de datumkolom over. De vraag is: „waar zit ik
ongeveer?“ Datumscheidingen beantwoorden die: een haarlijn dwars door de graaf met
een relatief label rechts, die markeert waar het ene stuk geschiedenis eindigt en
een ouder stuk begint.

De stukken worden grover naarmate je teruggaat — vandaag, gisteren, een paar
dagen, een week, weken, maanden, jaren. Dat is precies de bedoeling: een lijn bij
elke dagwissel zou in een druk repository onder bijna elke commit staan en in een
rustig repository nergens.

![Datumscheidingen die stukken geschiedenis in de graaf markeren](../../screenshots/graph-date-dividers.webp)

Een scheiding staat **onderaan** de laatste rij van haar stuk en draagt de naam
van het stuk dat ze afsluit — ze beschrijft dus de rijen erboven. De onderste rij
op het scherm krijgt er geen: haar stuk kan doorlopen in commits die nog niet
geladen zijn.

De grenzen. Commits staan in `--date-order`, waardoor een merge boven de commits
staat die hij samenvoegt: een rij die nieuwer is dan het stuk waarin hij belandt
sluit zich bij dat stuk aan in plaats van een nieuw te openen, zodat scheidingen
altijd van nieuw naar oud lopen. De rij met niet-vastgelegde wijzigingen en
stashes worden overgeslagen, omdat hun datum niet hun plaats in de geschiedenis
is. En er is geen instelling: scheidingen staan altijd aan.

## Commitdetails

Een commit selecteren toont zijn gewijzigde bestanden (boom of plat), auteur,
SHA, co-auteurs en zijn handtekening. `#123`-verwijzingen en `@vermeldingen`
worden automatisch gelinkt naar je host.

Boven de bestandslijst splitst een **wijzigingsoverzicht** de commit uit naar
soort in plaats van één totaal te geven — *5 gewijzigd*, *1 toegevoegd*,
*1 verwijderd*, plus *hernoemd* en *conflicterend* als de commit die bevat. Elk
krijgt de kleur van het statusteken op de regels eronder en van de tellers op
ingeklapte mappen, zodat dezelfde kleur overal in het paneel hetzelfde betekent.
Soorten zonder bestanden vallen helemaal weg, dus een gewone bewerking toont één
item en niet vijf. Beweeg de muis over het overzicht voor het kale totaal
"*n* gewijzigde bestanden".

![Wijzigingsoverzicht boven de bestandslijst van een commit: 6 gewijzigd, 2 toegevoegd, 1 verwijderd, 1 hernoemd](../../screenshots/change-summary.webp)

Twee dingen vertelt het bewust niet. Het telt **bestanden, geen regels**: een
correctie van één teken en een volledige herschrijving lezen allebei als
*1 gewijzigd*; de diff laat zien hoeveel er echt veranderd is. En het telt elk
bestand in de commit, niet alleen wat aan een actief filter of een zoekopdracht
voldoet.

Hetzelfde overzicht staat boven het staging-paneel en de bestandslijst van een
stash. In het staging-paneel tellen niet-gevolgde bestanden als toevoegingen:
*toegevoegd* betekent daar "zat niet in de laatste commit", niet "al gestaged".

De bestandenlijst is meervoudig te selecteren met de gebruikelijke gebaren
(<kbd>⌘</kbd>/<kbd>Ctrl</kbd>-klik, <kbd>⇧</kbd>-klik,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Rechtsklik op de selectie → *{n}
bestanden terugzetten naar de werkboom* neemt die bestanden precies zoals deze
commit ze had: na één bevestiging worden de werkkopieën overschreven, zonder
HEAD of de index aan te raken.

![Door de commitdetails lopen](../../screenshots/clip-commit-details.webp)

**Zie ook:** [Blame & bestandsgeschiedenis](blame.md) · [Zoeken](search.md) · [Tijdmachine](time-machine.md)
