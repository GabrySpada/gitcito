---
title: O grafo de commits
category: Repositório e histórico
order: 10
summary: Lendo o histórico: faixas, refs, colunas, filtros e seleção múltipla.
keywords: grafo graph histórico history commits faixas lanes branches merges colunas columns filtro filter linear first-parent amend desfazer undo reset github
---

# O grafo de commits

Branches, merges e merges polvo desenhados direito, no claro ou no escuro. A
renderização é por janela, então um repositório com cem mil commits rola como um
com cem.

| | |
|---|---|
| ![Grafo de commits, claro](../../screenshots/graph-light.webp) | ![Grafo de commits, escuro](../../screenshots/graph-dark.webp) |

## Se movimentando

- <kbd>↑</kbd> <kbd>↓</kbd> (ou <kbd>j</kbd> <kbd>k</kbd>) andam com a seleção.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>-clique alterna um commit numa **seleção múltipla**,
  começando pelo commit já selecionado; <kbd>⇧</kbd>-clique pega um intervalo.
  Com vários selecionados, clique com o botão direito em um deles para fazer
  cherry-pick na branch atual, exportar um patch combinado, ou copiar os SHAs.
- **Squash** está sempre nesse menu, mas só roda quando a seleção é a ponta da
  branch atual e os commits logo abaixo, sem pular nenhum — é um reset soft para
  o pai do mais antigo. Linhas de stash no meio não contam. Caso contrário ele
  fica esmaecido; passe o mouse por cima para ver o motivo. O caso comum são
  commits de uma branch que não está em checkout: faça checkout dela antes. Para
  commits mais abaixo, use o [rebase interativo](rebase.md). Hooks de commit não
  rodam, como num rebase: os commits já existem, e um hook que falha não
  consegue mais deixar a branch com reset pela metade.
- Commits que chegaram no seu **último fetch ou pull** são sinalizados como novos.
  Os que ainda não entraram no branch atual ficam levemente translúcidos até um
  pull trazê-los.
- Clique com o botão direito num commit para **Amend**, **Desfazer**,
  **Resetar para o commit…** e **Ver no GitHub**, além de checkout, cherry-pick,
  revert, branch, tag e cópia. Ações inseguras continuam visíveis e se
  desabilitam.

## Fazendo o grafo mostrar o que você quer

- O **foco do grafo** decide quanto histórico é desenhado — Configurações →
  Temas → **Grafo**, ou o menu da engrenagem no cabeçalho do grafo. *Tudo*
  desenha tudo; *Histórico linear* (first-parent) deixa só o tronco; *Ocultar
  ramos mesclados* mantém o tronco mais os ramos ainda não mesclados; *Modo solo*
  mantém o seu ramo, os ramos favoritos e o ramo padrão.

  Ele só filtra o que o log já carregou. *Ocultar ramos mesclados* confia na
  resposta do próprio git a "já contido no ramo atual", então trocar de ramo muda
  o que some — e mantém todo commit que ainda tenha uma tag ou uma ref que ele
  não reconheça apontando para ele, que é justamente o que um ramo apagado deixa
  para trás. *Histórico linear* e *Modo solo* são mais brutos: uma tag ou um
  stash num commit que eles escondem some junto.

- **Filtrar por caminho**: clique com o botão direito num arquivo ou pasta →
  *Filtrar grafo por este caminho*, e só os commits que o tocaram continuam acesos.

![Grafo filtrado até um único caminho](../../screenshots/graph-path-filter.webp)

- **Colunas**: mostre, esconda, redimensione e reordene as colunas de branch,
  mensagem, autor, data, SHA, assinatura e deploy.
- **Estilo**: Configurações → Temas → **Grafo** — paleta de faixas (8 nativas,
  personalizada ou gerada por IA), estilo de canto, densidade das linhas e
  espessura dos traços, com uma pré-visualização ao vivo em mini-grafo.

![Configurações de estilo do grafo com pré-visualização ao vivo](../../screenshots/settings-graph.webp)

## Divisores de data

Ao percorrer um histórico longo, a pergunta quase nunca é o horário exato de um
commit — disso a coluna de data já cuida. É “mais ou menos onde eu estou”. Os
divisores de data respondem a isso: um fio fino atravessando o grafo com um rótulo
relativo à direita, marcando onde um trecho do histórico termina e outro mais
antigo começa.

Os trechos ficam mais largos conforme se recua — hoje, ontem, alguns dias, uma
semana, semanas, meses, anos. É justamente esse o ponto: uma linha a cada virada
de dia apareceria sob quase todo commit num repositório ativo e em nenhum num
repositório parado.

![Divisores de data marcando trechos do histórico no grafo](../../screenshots/graph-date-dividers.webp)

Um divisor fica no **fim** da última linha do seu trecho e leva o nome do trecho
que fecha, então descreve as linhas acima dele. A última linha da tela não recebe
nenhum: o trecho dela pode continuar nos commits ainda não carregados.

Os limites. Os commits são listados em `--date-order`, que põe um merge acima dos
commits que ele une: uma linha mais recente que o trecho em que cai entra nesse
trecho em vez de abrir um novo, de modo que os divisores sempre vão do recente ao
antigo. A linha de alterações não commitadas e os stashes são ignorados, porque a
data deles não é o lugar deles no histórico. E não há ajuste: os divisores estão
sempre ligados.

## Detalhes do commit

Selecionar um commit mostra os arquivos alterados dele (em árvore ou plano), autor,
SHA, coautores e a assinatura. Referências `#123` e `@menções` viram links
automáticos para o seu host.

Acima da lista de arquivos, um **resumo das mudanças** decompõe o commit por tipo
em vez de dar um total único — *5 modificados*, *1 adicionado*, *1 excluído*, mais
*renomeados* e *em conflito* quando o commit os tem. Cada um usa a cor do glifo de
status das linhas abaixo e dos contadores nas pastas recolhidas, de modo que a
mesma cor significa a mesma coisa em todo o painel. Tipos sem arquivos somem por
completo: uma edição comum mostra uma entrada, não cinco. Passe o mouse sobre o
resumo para ver o total "*n* arquivos alterados".

![Resumo das mudanças acima da lista de arquivos de um commit: 6 modificados, 2 adicionados, 1 excluído, 1 renomeado](../../screenshots/change-summary.webp)

Duas coisas que ele deliberadamente não conta. Ele conta **arquivos, não linhas**:
uma correção de um caractere e uma reescrita aparecem ambas como *1 modificado*; é
o diff que mostra o tamanho da mudança. E conta todos os arquivos do commit, não o
subconjunto que corresponde a um filtro ou busca ativa.

O mesmo resumo encabeça o painel de preparação e a lista de arquivos de um stash.
No painel de preparação, arquivos não rastreados contam como adições, então
*adicionado* quer dizer "não estava no último commit", e não "já preparado".

A lista de arquivos se seleciona em grupo com os gestos de sempre (clique com
<kbd>⌘</kbd>/<kbd>Ctrl</kbd>, clique com <kbd>⇧</kbd>,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Clique com o botão direito na seleção
→ *Restaurar {n} arquivos na árvore de trabalho* pega esses arquivos exatamente
como este commit os tinha: depois de uma única confirmação sobrescreve as
cópias de trabalho, sem tocar em HEAD nem no índice.

![Percorrendo os detalhes de um commit](../../screenshots/clip-commit-details.webp)

**Veja também:** [Blame e histórico do arquivo](blame.md) · [Busca](search.md) · [Máquina do tempo](time-machine.md)
