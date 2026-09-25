---
title: Staging
category: Trabalhando com mudanças
order: 30
summary: Prepare arquivos inteiros, hunks isolados ou linhas individuais.
keywords: staging stage preparar unstage despreparar descartar discard hunk linhas lines índice index parcial partial
---

# Staging

O painel de commit tem três listas: **Em conflito**, **Não preparados** e
**Preparados**. Cada uma colapsa, e cada uma lembra se você a deixou aberta.

![Um diff não preparado, com os controles de hunk e de arquivo ao lado](../../screenshots/line-staging.webp)

## Três níveis de precisão

| Nível | Como |
|---|---|
| **Arquivo** | Clique no ✚ da linha, ou selecione várias linhas e prepare o lote |
| **Hunk** | Abra o diff e use o botão no cabeçalho do hunk |
| **Linha** | Passe o mouse sobre uma linha alterada no diff e clique no **+** dela, ou selecione várias linhas e mande essas para o stage |

O staging por linha é o que torna prático manter um `console.log` de depuração
fora de um commit sem precisar apagá-lo antes.

## Mandar linhas avulsas para o stage

Abra o diff de um arquivo fora do stage, na visão unificada ou na dividida.
Passe o mouse sobre uma linha adicionada ou removida e um pequeno **+** verde
aparece no começo dela: um clique manda essa linha para o stage e nada mais. O
resto do trecho continua fora do stage, exatamente como se você tivesse editado
o trecho à mão no `git add -p`.

Para várias linhas de uma vez, clique nas próprias linhas para selecioná-las —
<kbd>⇧</kbd>-clique pega todas as linhas alteradas desde a última clicada — e
aperte **Mandar N linha(s) para o stage** na barra acima do diff.

Também funciona ao contrário. Abra a versão **no stage** de um arquivo e os
controles viram um **−** vermelho, **Tirar o trecho do stage** e **Tirar N
linha(s) do stage**: eles tiram linhas do índice e deixam a working tree em paz.

Tire a última mudança do lado que você está vendo e o diff acompanha o arquivo
para o outro lado, em vez de ficar vazio.

Cada linha ou trecho que você manda para o stage ou tira dele assim se desfaz
com **Desfazer** na barra de ferramentas, um clique por vez.

| Você escolhe | Mandar para o stage | Tirar do stage |
|---|---|---|
| Uma linha adicionada | O índice ganha essa linha | O índice perde essa linha |
| Uma linha removida | O índice perde essa linha | A linha volta para o índice |
| Nenhuma, no mesmo trecho | Fica como está, só na working tree | Continua no stage |

### Limites

- **Espaços ocultos, sem stage.** Enquanto os *Espaços* são ignorados, o diff
  omite mudanças e não sabe dizer quais linhas mandar; os controles somem até
  você desligar isso.
- **Arquivos não rastreados** vão inteiros. Mande o arquivo primeiro e depois
  tire as linhas que você não quer.
- **A última linha de um arquivo sem quebra de linha no final** pode ser
  recusada se você separar a mudança dela de linhas adicionadas depois: o git
  não consegue descrever esse meio-termo como patch. Mande as duas juntas.
- **Desfazer precisa do índice como você deixou.** Se você mandou mais coisa
  das mesmas linhas desde então, o git recusa desfazer em vez de adivinhar, e
  avisa.

## Descartando

Descartar funciona nos mesmos níveis, e sempre pergunta. Arquivos não rastreados
são apagados; os rastreados voltam ao estado preparado (ou commitado).

## Teclado

<kbd>↑</kbd> <kbd>↓</kbd> (ou <kbd>j</kbd> <kbd>k</kbd>) percorrem as listas de
arquivos, com <kbd>⇧</kbd> para um intervalo e <kbd>⌘</kbd>/<kbd>Ctrl</kbd> para
alternar arquivos individuais.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> amplia a seleção a partir da última
linha clicada. Clique com o botão direito na seleção para dar stage, tirar do
stage, fazer stash ou descartar tudo de uma vez.

## Copiar caminhos

O clique com o botão direito num arquivo não commitado oferece **Copiar
caminho do arquivo** (absoluto, com os separadores da plataforma) e **Copiar
caminho relativo do arquivo** (`src/index.ts`, sem `./` no início). Vários
arquivos selecionados copiam um caminho por linha, na ordem da lista. Arquivos
apagados continuam disponíveis — essas ações só copiam texto. Pastas ainda
copiam o caminho da pasta.

## Antes de você commitar

O Gitcito verifica algumas coisas e pergunta uma vez, nunca em silêncio:

- um arquivo que parece um **segredo** (`.env`, `*.pem`, `id_rsa`…),
- um blob **muito grande** (limite em Configurações → Segurança),
- commitar **direto numa branch protegida** (`main`/`master` por padrão).

Cada um desses oferece um *Ignorar e parar de rastrear* em um clique. Veja
[Segurança e segredos](security.md).

**Veja também:** [Commitando](committing.md) · [Diffs](diffs.md) · [Absorb](absorb.md)
