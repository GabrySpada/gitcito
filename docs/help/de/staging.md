---
title: Staging
category: Mit Änderungen arbeiten
order: 30
summary: Ganze Dateien, einzelne Hunks oder einzelne Zeilen stagen.
keywords: staging stagen stage unstage entfernen verwerfen discard hunk zeilen lines index teilweise partial
---

# Staging

Das Commit-Panel hat drei Listen: **Konflikte**, **Nicht gestagt** und
**Gestagt**. Jede lässt sich einklappen, und jede merkt sich, ob du sie offen
gelassen hast.

![Ein nicht gestagtes Diff, daneben die Bedienelemente für Hunk und Datei](../../screenshots/line-staging.webp)

## Drei Genauigkeitsstufen

| Stufe | Wie |
|---|---|
| **Datei** | Klick auf das ✚ in der Zeile — oder wähle mehrere Zeilen aus und stage sie alle |
| **Hunk** | Öffne das Diff und nutze den Knopf in der Hunk-Kopfzeile |
| **Zeile** | Fahre im Diff über eine geänderte Zeile und klicke ihr **+**, oder markiere mehrere Zeilen und stage diese |

Zeilenweises Staging ist das, was es praktikabel macht, ein
Debug-`console.log` aus einem Commit herauszuhalten, ohne es vorher zu löschen.

## Einzelne Zeilen stagen

Öffne den Diff einer nicht gestagten Datei, in der vereinheitlichten oder der
geteilten Ansicht. Fahre über eine hinzugefügte oder entfernte Zeile, und an
ihrem Anfang erscheint ein kleines grünes **+**: Ein Klick stagt diese Zeile
und sonst nichts. Der Rest des Hunks bleibt ungestagt, genau so, als hättest du
den Hunk in `git add -p` von Hand bearbeitet.

Für mehrere Zeilen auf einmal klickst du die Zeilen selbst an, um sie
auszuwählen — <kbd>⇧</kbd>-Klick nimmt jede geänderte Zeile ab der zuletzt
geklickten mit — und drückst **N Zeile(n) stagen** in der Leiste über dem Diff.

Es geht auch andersherum. Öffne die **gestagte** Fassung einer Datei, und die
Bedienelemente werden zu einem roten **−**, **Hunk entstagen** und **N Zeile(n)
entstagen**: Sie nehmen Zeilen wieder aus dem Index und lassen den
Arbeitsbaum in Ruhe.

Nimmst du die letzte Änderung von der Seite, die du gerade ansiehst, folgt der
Diff der Datei auf die andere Seite, statt leer zu bleiben.

Jede Zeile und jeder Hunk, die du so stagst oder entstagst, lässt sich mit
**Rückgängig** in der Werkzeugleiste zurücknehmen, ein Klick nach dem anderen.

| Du wählst | Stagen | Entstagen |
|---|---|---|
| Eine hinzugefügte Zeile | Der Index bekommt diese Zeile | Der Index verliert diese Zeile |
| Eine entfernte Zeile | Der Index verliert diese Zeile | Die Zeile kommt zurück in den Index |
| Keine von beiden, im selben Hunk | Bleibt nur im Arbeitsbaum | Bleibt gestagt |

### Grenzen

- **Leerraum ausgeblendet, kein Staging.** Solange *Leerraum* ignoriert wird,
  lässt der Diff Änderungen weg und kann nicht sagen, welche Zeilen zu stagen
  sind; die Bedienelemente verschwinden, bis du es ausschaltest.
- **Nicht versionierte Dateien** werden als Ganzes gestagt. Stage die Datei
  zuerst und entstage dann die Zeilen, die du nicht willst.
- **Die letzte Zeile einer Datei ohne abschließenden Zeilenumbruch** kann
  abgelehnt werden, wenn du ihre Änderung von danach hinzugefügten Zeilen
  trennst — git kann diesen halben Zustand nicht als Patch beschreiben. Stage
  beide zusammen.
- **Rückgängig braucht den Index so, wie du ihn verlassen hast.** Hast du
  inzwischen mehr von denselben Zeilen gestagt, lehnt git das Rückgängigmachen
  ab, statt zu raten, und sagt es.

## Verwerfen

Verwerfen arbeitet auf denselben Stufen und fragt immer nach. Ungetrackte
Dateien werden gelöscht; getrackte kehren in ihren gestagten (oder committeten)
Zustand zurück.

## Tastatur

<kbd>↑</kbd> <kbd>↓</kbd> (oder <kbd>j</kbd> <kbd>k</kbd>) laufen durch die
Dateilisten, mit <kbd>⇧</kbd> für einen Bereich und <kbd>⌘</kbd>/<kbd>Ctrl</kbd>
zum Umschalten einzelner Dateien.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> erweitert die Auswahl von der zuletzt
geklickten Zeile aus. Ein Rechtsklick auf die Auswahl staged, unstaged, stasht
oder verwirft alles darin auf einmal.

## Pfade kopieren

Ein Rechtsklick auf eine uncommittete Datei bietet **Dateipfad kopieren**
(absolut, mit den Trennzeichen der Plattform) und **Relativen Dateipfad
kopieren** (`src/index.ts`, ohne führendes `./`). Mehrere ausgewählte Dateien
landen eine pro Zeile, in Listenreihenfolge. Gelöschte Dateien bleiben
aktiviert — kopiert wird nur der Pfadtext. Ordner kopieren weiterhin den
Ordnerpfad.

## Bevor du committest

Gitcito prüft ein paar Dinge und fragt einmal nach — nie stillschweigend:

- eine Datei, die nach einem **Geheimnis** aussieht (`.env`, `*.pem`,
  `id_rsa`…),
- ein **sehr großer** Blob (Schwellwert unter Einstellungen → Sicherheit),
- ein Commit **direkt auf einen geschützten Branch** (voreingestellt
  `main`/`master`).

Jede dieser Warnungen bietet ein *Ignorieren & nicht mehr tracken* mit einem
Klick an. Siehe [Sicherheit & Geheimnisse](security.md).

**Siehe auch:** [Committen](committing.md) · [Diffs](diffs.md) · [Einarbeiten](absorb.md)
