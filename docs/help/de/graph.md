---
title: Der Commit-Graph
category: Repository & Historie
order: 10
summary: Historie lesen: Spuren, Refs, Spalten, Filter und Mehrfachauswahl.
keywords: graph graf historie verlauf commits spuren lanes branches merges spalten columns filter linear first-parent amend rückgängig undo reset github
---

# Der Commit-Graph

Branches, Merges und Octopus-Merges sauber gezeichnet, hell oder dunkel. Das
Rendering ist gefenstert, deshalb scrollt ein Repository mit hunderttausend
Commits wie eines mit hundert.

| | |
|---|---|
| ![Commit-Graph, hell](../../screenshots/graph-light.webp) | ![Commit-Graph, dunkel](../../screenshots/graph-dark.webp) |

## Navigieren

- <kbd>↑</kbd> <kbd>↓</kbd> (oder <kbd>j</kbd> <kbd>k</kbd>) bewegen die Auswahl.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>-Klick nimmt einen Commit in eine
  **Mehrfachauswahl** auf oder wieder heraus, ausgehend vom bereits gewählten
  Commit; <kbd>⇧</kbd>-Klick nimmt einen Bereich. Mit mehreren ausgewählten
  Commits kannst du per Rechtsklick auf einen davon ein Cherry-Pick auf den
  aktuellen Branch machen, einen kombinierten Patch exportieren oder ihre SHAs
  kopieren.
- **Squash** steht immer in diesem Menü, läuft aber nur, wenn die Auswahl die
  Spitze des ausgecheckten Branches und die Commits direkt darunter umfasst,
  ohne Lücke — es ist ein Soft-Reset auf den Parent des ältesten. Stash-Zeilen
  dazwischen spielen keine Rolle. Sonst ist es ausgegraut; fahr darüber, um den
  Grund zu sehen. Meist sind es Commits eines Branches, der nicht ausgecheckt
  ist: checke ihn zuerst aus. Für Commits weiter unten nimm den [interaktiven
  Rebase](rebase.md). Commit-Hooks laufen nicht, wie bei einem Rebase: Die
  Commits gibt es schon, und ein fehlschlagender Hook kann den Branch nicht mehr
  halb zurückgesetzt zurücklassen.
- Commits, die mit deinem **letzten Fetch oder Pull** hereinkamen, werden als neu
  markiert. Die, die noch nicht im ausgecheckten Branch stecken, bleiben leicht
  durchscheinend, bis ein Pull sie holt.
- Rechtsklick auf einen Commit für **Ändern**, **Rückgängig**, **Auf Commit
  zurücksetzen…** und **Auf GitHub anzeigen**, dazu Checkout, Cherry-Pick,
  Revert, Branch, Tag und Kopieren. Unsichere Aktionen bleiben sichtbar und
  werden deaktiviert.

## Ihn zeigen lassen, was du willst

- Der **Graph-Fokus** entscheidet, wie viel Historie gezeichnet wird —
  Einstellungen → Themes → **Graph** oder das Zahnradmenü in der Kopfzeile des
  Graphen. *Alles* zeichnet alles; *Lineare Historie* (First-Parent) lässt nur
  den Hauptstrang übrig; *Zusammengeführte Branches ausblenden* behält den
  Hauptstrang plus die noch offenen Branches; *Solo-Modus* behält deinen Branch,
  deine markierten Branches und den Standard-Branch.

  Gefiltert wird nur, was das Log bereits geladen hat. *Zusammengeführte Branches
  ausblenden* verlässt sich auf gits eigene Antwort auf „bereits im aktuellen
  Branch enthalten“, ein Branch-Wechsel ändert also das Ergebnis — und der Modus
  behält jeden Commit, auf den noch ein Tag oder eine unbekannte Ref zeigt, genau
  das, was ein gelöschter Branch hinterlässt. *Lineare Historie* und *Solo-Modus*
  sind gröber: Ein Tag oder ein Stash auf einem ausgeblendeten Commit geht mit
  ihm.

- **Nach Pfad filtern**: Rechtsklick auf eine Datei oder einen Ordner → *Graph
  nach diesem Pfad filtern*, und nur die Commits, die ihn berührt haben, bleiben
  hell.

![Graph heruntergefiltert auf einen einzigen Pfad](../../screenshots/graph-path-filter.webp)

- **Spalten**: Branch-, Nachrichten-, Autor-, Datums-, SHA-, Signatur- und
  Deployment-Spalten anzeigen, ausblenden, in der Größe ändern und umsortieren.
- **Stil**: Einstellungen → Themes → **Graph** — Spurenpalette (8 eingebaute,
  eigene oder KI-generierte), Eckenstil, Zeilendichte und Linienstärke, mit einer
  Live-Vorschau als Mini-Graph.

![Graph-Stileinstellungen mit Live-Vorschau](../../screenshots/settings-graph.webp)

## Datumstrenner

Beim Blättern durch eine lange Historie ist die Frage fast nie der exakte
Zeitstempel eines Commits — den nennt die Datumsspalte. Sie lautet: „Wo ungefähr
bin ich?“ Datumstrenner beantworten sie: eine Haarlinie quer durch den Graphen mit
einer relativen Beschriftung rechts, die markiert, wo ein Abschnitt der Historie
endet und ein älterer beginnt.

Die Abschnitte werden mit dem Abstand gröber — heute, gestern, ein paar Tage, eine
Woche, Wochen, Monate, Jahre. Genau darum geht es: Eine Linie bei jedem
Tageswechsel stünde in einem aktiven Repository unter fast jedem Commit und in
einem ruhigen unter gar keinem.

![Datumstrenner markieren Abschnitte der Historie im Graphen](../../screenshots/graph-date-dividers.webp)

Ein Trenner sitzt **unten** an der letzten Zeile seines Abschnitts und trägt den
Namen des Abschnitts, den er abschließt — er beschreibt also die Zeilen darüber.
Die unterste sichtbare Zeile bekommt keinen: Ihr Abschnitt kann sich in noch nicht
geladenen Commits fortsetzen.

Die Grenzen. Commits werden in `--date-order` aufgelistet, wodurch ein Merge über
den Commits steht, die er zusammenführt: Eine Zeile, die neuer ist als der
Abschnitt, in den sie fällt, schließt sich diesem an, statt einen neuen zu öffnen —
Trenner laufen also immer von neu nach alt. Die Zeile der nicht committeten
Änderungen und Stashes werden übersprungen, denn ihr Datum ist nicht ihr Platz in
der Historie. Und es gibt keine Einstellung: Trenner sind immer an.

## Commit-Details

Wählst du einen Commit aus, siehst du seine geänderten Dateien (als Baum oder
flach), Autor, SHA, Co-Autoren und seine Signatur. `#123`-Referenzen und
`@mentions` werden automatisch mit deinem Host verlinkt.

Über der Dateiliste zerlegt eine **Änderungsübersicht** den Commit nach Art,
statt eine einzelne Gesamtzahl zu nennen — *5 geändert*, *1 hinzugefügt*,
*1 gelöscht*, dazu *umbenannt* und *in Konflikt*, sofern der Commit sie enthält.
Jeder Eintrag trägt die Farbe des Status-Glyphs in den Zeilen darunter und der
Zähler an eingeklappten Ordnern — dieselbe Farbe bedeutet also überall im Panel
dasselbe. Arten ohne Dateien entfallen ganz, eine gewöhnliche Änderung zeigt
darum einen Eintrag statt fünf. Beim Überfahren erscheint die schlichte Summe
„*n* geänderte Dateien".

![Änderungsübersicht über der Dateiliste eines Commits: 6 geändert, 2 hinzugefügt, 1 gelöscht, 1 umbenannt](../../screenshots/change-summary.webp)

Zweierlei sagt sie bewusst nicht. Sie zählt **Dateien, keine Zeilen**: eine
Korrektur von einem Zeichen und eine komplette Neufassung lesen sich beide als
*1 geändert*; wie viel sich wirklich bewegt hat, zeigt der Diff. Und sie zählt
alle Dateien des Commits, nicht nur die, auf die ein aktiver Filter oder eine
Suche passt.

Dieselbe Übersicht steht über dem Staging-Panel und der Dateiliste eines Stash.
Im Staging-Panel zählen unversionierte Dateien als Hinzufügungen — *hinzugefügt*
heißt dort also „nicht im letzten Commit", nicht „bereits gestaged".

Die Dateiliste lässt sich mit den üblichen Gesten mehrfach auswählen
(<kbd>⌘</kbd>/<kbd>Strg</kbd>-Klick, <kbd>⇧</kbd>-Klick,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Rechtsklick auf die Auswahl → *{n}
Dateien in den Arbeitsbaum zurückholen* übernimmt diese Dateien genau so, wie
dieser Commit sie hatte: nach einer einzigen Bestätigung werden die
Arbeitskopien überschrieben — HEAD und Index bleiben unangetastet.

![Durch die Commit-Details laufen](../../screenshots/clip-commit-details.webp)

**Siehe auch:** [Blame & Dateiverlauf](blame.md) · [Suche](search.md) · [Zeitmaschine](time-machine.md)
