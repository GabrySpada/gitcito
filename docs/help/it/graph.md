---
title: Il grafo dei commit
category: Repository e cronologia
order: 10
summary: Leggere la storia: corsie, ref, colonne, filtri e selezione multipla.
keywords: grafo graph storia cronologia commit corsie lanes branch merge colonne filtro lineare first-parent correggi amend annulla undo reset github
---

# Il grafo dei commit

Branch, merge e merge a piovra disegnati come si deve, in chiaro o in scuro. Il
rendering è a finestra, quindi un repository con centomila commit scorre come uno
che ne ha cento.

| | |
|---|---|
| ![Grafo dei commit, tema chiaro](../../screenshots/graph-light.webp) | ![Grafo dei commit, tema scuro](../../screenshots/graph-dark.webp) |

## Muoversi

- <kbd>↑</kbd> <kbd>↓</kbd> (oppure <kbd>j</kbd> <kbd>k</kbd>) spostano la
  selezione.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>+clic aggiunge o toglie un commit da una
  **selezione multipla**, partendo dal commit già selezionato; <kbd>⇧</kbd>+clic
  prende un intervallo. Con più commit selezionati, il clic destro su uno di essi
  permette di fare cherry-pick sul branch corrente, esportare un'unica patch
  combinata o copiare i loro SHA.
- **Squash** è sempre nel menu, ma funziona solo quando la selezione è la punta
  del branch corrente più i commit subito sotto, senza salti — è un reset soft
  al genitore del più vecchio. Le righe di stash in mezzo non contano.
  Altrimenti è disattivato: passaci sopra per il motivo. Il caso tipico sono
  commit di un branch su cui non hai fatto checkout: fai prima checkout di quel
  branch. Per commit più in basso usa il [rebase interattivo](rebase.md). Gli
  hook di commit non vengono eseguiti, come in un rebase: i commit esistono già,
  e un hook che fallisce non può più lasciare il branch resettato a metà.
- I commit arrivati con il tuo **ultimo fetch o pull** sono contrassegnati come
  nuovi. Quelli non ancora entrati nel branch attivo restano leggermente
  traslucidi finché un pull non li integra.
- Clic destro su un commit per **Correggi**, **Annulla**, **Reset al commit…** e
  **Apri su GitHub**, oltre a checkout, cherry-pick, revert, branch, tag e
  copia. Le azioni non sicure restano visibili e si disabilitano.

## Fargli mostrare quello che vuoi

- Il **focus del grafo** decide quanta storia viene disegnata — Impostazioni →
  Temi → **Grafo**, o il menu dell'ingranaggio nell'intestazione del grafo.
  *Tutto* disegna ogni cosa; *Cronologia lineare* (first-parent) lascia solo il
  tronco; *Nascondi i rami già uniti* tiene il tronco più i rami ancora aperti;
  *Modalità solo* tiene il tuo ramo, i rami preferiti e il ramo predefinito.

  Filtra soltanto ciò che il log ha già caricato. *Nascondi i rami già uniti* si
  fida della risposta di git a «già contenuto nel ramo corrente», quindi cambiare
  ramo cambia ciò che sparisce — e tiene ogni commit ancora puntato da un tag o
  da un riferimento che non riconosce, cioè proprio quello che lascia un ramo
  cancellato. *Cronologia lineare* e *Modalità solo* sono più drastiche: un tag o
  uno stash su un commit che nascondono sparisce con lui.

- **Filtra per percorso**: clic destro su un file o una cartella → *Filtra il
  grafo per questo percorso*, e restano accesi solo i commit che l'hanno toccato.

![Il grafo filtrato su un solo percorso](../../screenshots/graph-path-filter.webp)

- **Colonne**: mostra, nascondi, ridimensiona e riordina le colonne branch,
  messaggio, autore, data, SHA, firma e deploy.
- **Stile**: Impostazioni → Temi → **Grafo** — palette delle corsie (9 integrate,
  personalizzata o generata dall'AI), stile degli angoli, densità delle righe e
  spessore delle linee, con un mini-grafo di anteprima dal vivo.

![Le impostazioni di stile del grafo con anteprima dal vivo](../../screenshots/settings-graph.webp)

## Divisori di data

Scorrendo una storia lunga la domanda non è quasi mai l'ora esatta di un commit —
a quella risponde la colonna della data. È «più o meno a che punto sono». I
divisori di data rispondono a questa: un filo sottile attraverso il grafo con
un'etichetta relativa a destra, che segna dove un tratto di storia finisce e ne
comincia uno più vecchio.

I tratti si allargano man mano che si va indietro — oggi, ieri, qualche giorno,
una settimana, settimane, mesi, anni. È proprio questo il punto: una riga a ogni
cambio di giorno comparirebbe sotto quasi ogni commit in un repository attivo e
da nessuna parte in uno tranquillo.

![Divisori di data che segnano i tratti di storia nel grafo](../../screenshots/graph-date-dividers.webp)

Un divisore sta in **fondo** all'ultima riga del suo tratto e porta il nome del
tratto che chiude, quindi descrive le righe sopra di sé. L'ultima riga a schermo
non ne ha: il suo tratto potrebbe continuare fra i commit non ancora caricati.

I limiti. I commit sono elencati in `--date-order`, che mette un merge sopra i
commit che unisce: una riga più recente del tratto in cui capita entra in quel
tratto invece di aprirne uno nuovo, così i divisori vanno sempre dal recente al
vecchio. La riga delle modifiche non salvate e gli stash vengono saltati, perché
la loro data non è la loro posizione nella storia. E non c'è un'impostazione: i
divisori ci sono sempre.

## Dettagli di un commit

Selezionando un commit vedi i suoi file modificati (ad albero o piatti),
l'autore, lo SHA, i coautori e la sua firma. I riferimenti `#123` e le
`@menzioni` diventano automaticamente link al tuo host.

Sopra l'elenco dei file, un **riepilogo delle modifiche** scompone il commit per
tipo invece di dare un totale unico — *5 modificati*, *1 aggiunto*, *1 eliminato*,
più *rinominati* e *in conflitto* quando il commit li contiene. Ogni voce ha il
colore del glifo di stato nelle righe sottostanti e dei contatori sulle cartelle
chiuse, così lo stesso colore significa la stessa cosa in tutto il pannello. I
tipi senza file spariscono del tutto: una modifica ordinaria mostra una voce, non
cinque. Passa il mouse sul riepilogo per il totale "*n* file cambiati".

![Riepilogo delle modifiche sopra l'elenco file di un commit: 6 modificati, 2 aggiunti, 1 eliminato, 1 rinominato](../../screenshots/change-summary.webp)

Due cose che di proposito non ti dice. Conta **i file, non le righe**: una
correzione di un carattere e una riscrittura sono entrambe *1 modificato*; è il
diff a mostrare quanto è cambiato. E conta tutti i file del commit, non il
sottoinsieme che corrisponde a un filtro o a una ricerca attiva.

Lo stesso riepilogo apre il pannello di staging e l'elenco file di uno stash. Nel
pannello di staging i file non tracciati contano come aggiunte, quindi *aggiunto*
significa "non era nell'ultimo commit", non "già in staging".

L'elenco dei file si seleziona in gruppo con i gesti consueti (clic
<kbd>⌘</kbd>/<kbd>Ctrl</kbd>, clic <kbd>⇧</kbd>,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Clic destro sulla selezione →
*Ripristina {n} file nell'albero di lavoro* riprende quei file esattamente come
li aveva questo commit: dopo un'unica conferma sovrascrive le copie di lavoro,
senza toccare né HEAD né l'indice.

![Una passeggiata fra i dettagli dei commit](../../screenshots/clip-commit-details.webp)

**Vedi anche:** [Blame e cronologia del file](blame.md) · [Ricerca](search.md) · [Macchina del tempo](time-machine.md)
