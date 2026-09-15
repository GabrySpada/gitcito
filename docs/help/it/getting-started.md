---
title: Primi passi
category: Per iniziare
order: 1
summary: Apri un repository, leggi il grafo, fai il tuo primo commit.
keywords: introduzione primi passi apri clone schede tabs grafo graph commit
---

# Primi passi

Gitcito apre una cartella e ti mostra la sua storia. Nel tuo repository non viene
scritto niente finché non lo chiedi tu.

![Un repository appena aperto, ancora senza commit](../../screenshots/empty-repo.webp)

## Aprire un repository

- **Trascina una cartella** sulla finestra, oppure usa **Apri repository** nella
  schermata di benvenuto.
- **Clonane** uno da un URL o direttamente dal tuo host — vedi
  [clonare](cloning.md) per le opzioni che rendono rapido il clone di un
  repository enorme.
- Da terminale, `gitcito .` apre la cartella corrente nell'app già in esecuzione
  — vedi [la riga di comando](cli.md).
- Una cartella che non è ancora un repository Git si apre lo stesso, e ti propone
  di inizializzarla.

## I tre pannelli

| Pannello | Cosa contiene |
|---|---|
| Sinistra | Branch, remote, tag, stash, worktree — e la scheda **File** per l'albero di lavoro |
| Centro | Il grafo dei commit, e qualunque cosa tu vi selezioni |
| Destra | Il compositore di commit, o i dettagli del commit selezionato |

## Trovare tutto il resto

Due strade, e portano negli stessi posti:

- **`⌘K`** (`Ctrl+K`) — la tavolozza dei comandi. Scrivi quello che vuoi; salta
  anche a branch, commit e file.
- **Strumenti** nella barra degli strumenti — lo stesso insieme legato al
  repository, ma come menu, con la coda lunga raccolta in gruppi perché resti
  leggibile.

![Il menu Strumenti: prima gli strumenti frequenti, il resto raggruppato](../../screenshots/tools-menu.webp)

La barra delle azioni tiene i pulsanti al centro della **finestra**, non nello spazio fra il nome del repository e la casella di ricerca: così restano dove sono anche passando fra repository con nomi di lunghezze molto diverse. I nomi di repository e branch si leggono per intero; quello abbastanza lungo da minacciare il resto della barra viene troncato, con il nome completo nel tooltip del pulsante.

Lo spazio si misura da quel centro verso l’esterno, ed è il prezzo di tenerlo: quando la finestra si restringe, o il nome del repository è molto lungo, la barra cede spazio invece di spostarsi. Il primo a cedere è il campo di ricerca, che scambia la sua casella per una lente — cliccala per cercare, e resta aperta finché c’è un filtro attivo. Poi i pulsanti che non entrano più si ripiegano in un menu **Altro** in fondo alla barra, nell’ordine della barra e con i loro sottomenu. Allarga la finestra e tornano fuori.

![La barra delle azioni in una finestra stretta: la ricerca ripiegata in una lente, la coda della barra dentro «Altro» e i pulsanti ancora centrati](../../screenshots/toolbar-narrow.webp)

Tutto ciò che è raggiungibile da una via lo è anche dall'altra, quindi non c'è
niente che solo gli utenti esperti riescono a trovare.

## Il tuo primo commit

1. Modifica un file. Compare sotto **Non in stage**.
2. Mettilo in stage — l'intero file, un hunk, o [singole righe](staging.md).
3. Scrivi un messaggio e premi **Commit**.

Tutto il resto in Gitcito è opzionale.

