---
title: Staging
category: Lavorare con le modifiche
order: 30
summary: Metti in stage interi file, singoli hunk o singole righe.
keywords: staging stage unstage scarta discard hunk righe indice parziale
---

# Staging

Il pannello dei commit ha tre elenchi: **In conflitto**, **Non in stage** e **In
stage**. Ciascuno si richiude, e ciascuno ricorda se l'hai lasciato aperto.

![Un diff non in stage, con accanto i controlli per hunk e per file](../../screenshots/line-staging.webp)

## Tre livelli di precisione

| Livello | Come |
|---|---|
| **File** | Clicca la ✚ sulla riga, oppure seleziona più righe e mettile in stage tutte |
| **Hunk** | Apri il diff e usa il pulsante nell'intestazione dell'hunk |
| **Riga** | Passa sopra una riga modificata nel diff e clicca il suo **+**, oppure seleziona più righe e mettile in stage |

Lo staging per righe è ciò che rende praticabile tenere un `console.log` di
debug fuori da un commit senza doverlo prima cancellare.

## Mettere in stage singole righe

Apri il diff di un file non in stage, nella vista unificata o in quella
affiancata. Passa sopra una riga aggiunta o rimossa e al suo inizio compare un
piccolo **+** verde: un clic mette in stage quella riga e nient'altro. Il resto
dell'hunk resta fuori dallo stage, esattamente come se avessi modificato l'hunk
a mano con `git add -p`.

Per più righe alla volta, clicca le righe stesse per selezionarle —
<kbd>⇧</kbd>-clic prende tutte le righe modificate dall'ultima cliccata — e
premi **Metti in staging le righe selezionate** nella barra sopra il diff.

Funziona anche al contrario. Apri la versione **in stage** di un file e i
controlli diventano un **−** rosso, **Togli l’hunk dallo staging** e **Togli
dallo staging le righe selezionate**: riportano le righe fuori dall'indice e
lasciano stare la working tree.

Togli l'ultima modifica dal lato che stai guardando e il diff segue il file
sull'altro lato, invece di restare vuoto.

Ogni riga o hunk messo in stage o tolto così si annulla con **Annulla** nella
barra degli strumenti, un clic alla volta.

| Scegli | Metterla in stage | Toglierla dallo stage |
|---|---|---|
| Una riga aggiunta | L'indice guadagna quella riga | L'indice perde quella riga |
| Una riga rimossa | L'indice perde quella riga | La riga torna nell'indice |
| Nessuna delle due, nello stesso hunk | Resta com'è, solo nella working tree | Resta in stage |

### Limiti

- **Spazi nascosti, niente staging.** Con *Spazi bianchi* ignorati il diff omette
  delle modifiche, quindi non sa dire quali righe mettere in stage; i controlli
  spariscono finché non lo spegni.
- **I file non tracciati** vanno in stage interi. Mettili prima in stage, poi
  togli le righe che non vuoi.
- **L'ultima riga di un file senza a capo finale** può essere rifiutata se
  separi la sua modifica dalle righe aggiunte dopo: git non sa descrivere quello
  stato a metà come patch. Mettile in stage insieme.
- **L'annullamento vuole l'indice come l'hai lasciato.** Se nel frattempo hai
  messo in stage altro sulle stesse righe, git rifiuta l'annullamento invece di
  tirare a indovinare, e lo dice.

## Scartare

Lo scarto funziona agli stessi livelli, e chiede sempre. I file non tracciati
vengono eliminati; quelli tracciati tornano al loro stato in stage (o
committato).

## Tastiera

<kbd>↑</kbd> <kbd>↓</kbd> (oppure <kbd>j</kbd> <kbd>k</kbd>) scorrono gli elenchi
di file, con <kbd>⇧</kbd> per un intervallo e <kbd>⌘</kbd>/<kbd>Ctrl</kbd> per
aggiungere o togliere singoli file.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> estende la selezione dall'ultima riga
cliccata. Un clic destro sulla selezione mette in stage, toglie dallo stage,
stasha o scarta tutto in una volta.

## Copiare i percorsi

Un clic destro su un file non committato offre **Copia il percorso del file**
(assoluto, con i separatori della piattaforma) e **Copia il percorso relativo
del file** (`src/index.ts`, senza `./` iniziale). Più file selezionati copiano
un percorso per riga, nell'ordine dell'elenco. I file eliminati restano
disponibili: queste azioni copiano solo testo. Le cartelle continuano a
copiare il percorso della cartella.

## Prima di committare

Gitcito controlla alcune cose e chiede una volta, mai in silenzio:

- un file che sembra un **segreto** (`.env`, `*.pem`, `id_rsa`…),
- un blob **molto grande** (soglia in Impostazioni → Sicurezza),
- il commit **diretto su un branch protetto** (`main`/`master` di default).

Ognuno di questi offre un *Ignora e togli dal tracciamento* in un clic. Vedi
[Sicurezza e segreti](security.md).

**Vedi anche:** [Fare commit](committing.md) · [Diff](diffs.md) · [Absorb](absorb.md)
