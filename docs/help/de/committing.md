---
title: Committen
category: Mit Änderungen arbeiten
order: 31
summary: Nachrichtenstile, Vorlagen, Co-Autoren und der Linter.
keywords: commit committen nachricht message composer conventional gitmoji ticket amend template vorlage co-author linter rückgängig undo reset
---

# Committen

## Nachrichtenstile

Wähle einen in den Einstellungen; der Editor passt sich daran an.

| Stil | Sieht so aus |
|---|---|
| **Conventional** | `feat(api)!: add rate limiting` — mit einer Typ-Auswahl |
| **Gitmoji** | `✨ add rate limiting` — mit einer Emoji-Auswahl |
| **Ticket** | `ABC-123: add rate limiting` — aus dem Branch-Namen vorbefüllt |
| **Plain** · **Auto** | Was immer du tippst; bei Auto entscheidet die KI über die Form |
| **Caveman** · **Haiku** | Genau das, wonach es klingt |

![Editor, vorbefüllt aus einer Commit-Vorlage](../../screenshots/commit-template.webp)

## Was der Editor für dich erledigt

- <kbd>↑</kbd> <kbd>↓</kbd> holt deine **letzten Nachrichten** zurück.
- Eine **Co-Autoren-Auswahl** fügt `Co-authored-by:`-Trailer aus den eigenen
  Mitwirkenden des Repositorys ein.
- `commit.template` / `.gitmessage` **befüllt** die Nachricht vor,
  Kommentarzeilen entfernt.
- Während eines Merge, Cherry-Pick oder Revert ist die Nachricht **vorbefüllt**,
  so wie git es täte.
- Entwürfe **bleiben** pro Repository erhalten, ein Tab-Wechsel verliert also
  nie eine Nachricht.

## Der Linter

Eine laufende, nicht blockierende Prüfung: Länge der Betreffzeile (mit
Zeichenzähler), ein Punkt am Ende, ein nicht-imperativer oder kleingeschriebener
Betreff, zu breite Zeilen im Textkörper. Hinweise, niemals eine Schranke — er
hält dich nicht vom Committen ab.

## Amend

Amend schreibt den letzten Commit mit dem um, was gerade gestaged ist. Gitcito
zeigt dir zuerst die vorhandene Nachricht, damit du sie bearbeitest, statt sie
neu zu tippen.

**Commit ändern…** auf einer Zeile des Graphen tut dasselbe für HEAD: Es lädt
die vollständige Nachricht, schaltet den Editor in den Amend-Modus und
fokussiert ihn. Ein bereits gepushter HEAD lässt sich weiterhin ändern, aber
Gitcito warnt, dass das Aktualisieren des Remotes einen Force-Push braucht.

### Den Commit einer anderen Person ändern

Ein Amend behält **Autor und Autorendatum** des ursprünglichen Commits — eine Regel von git, nicht von Gitcito. Das passt, wenn du einen Tippfehler im Commit einer Kollegin korrigierst, und ist falsch, wenn du eigene neue Arbeit hineinfaltest: Das Ergebnis wird ihr zugeschrieben, und der Graph zeigt ihren Namen an Code, den sie nie geschrieben hat.

Deshalb sagt der Composer, wenn die Autor-E-Mail von HEAD von deiner `user.email` abweicht, wessen Commit du änderst, und bietet **Mich als Autor eintragen** an. Aktiviert, ändert er mit `--reset-author`: Du wirst Autor, datiert auf jetzt. Lass es aus, um ihren Namen zu behalten.

![Amend-Modus bei einem Commit einer anderen Person](../../screenshots/amend-author.webp)

Verglichen werden E-Mails, also zählt jemand, der als `Elisa` und als `elisa` committet, als dieselbe Person. Auf den Namen fällt es nur zurück, wenn eine E-Mail fehlt, und es schweigt, wenn das Repository gar keine Identität konfiguriert hat. Geprüft wird nur der Commit, den du änderst: Ein späterer Cherry-Pick oder Rebase, der ihn woandershin trägt, behält den Autor, den er dann hat.

⌘Z nach einem Amend stellt den geänderten Commit wieder her, deine neuen Änderungen bleiben gestaged wie vor dem Amend — es springt nicht zum Eltern-Commit zurück.

**Commit rückgängig…** ist das Geschwister für einen ungepushten HEAD: Mixed
Reset auf den Eltern-Commit, die Änderungen im Arbeitsbaum bleiben erhalten,
die Nachricht kehrt in den Editor zurück. Der allererste Commit hat einen
eigenen Weg, der einen ungeborenen Branch hinterlässt, statt die Dateien zu
zerstören.

**Siehe auch:** [Staging](staging.md) · [Absorbieren](absorb.md) · [Changelog-Generator](changelog.md)
