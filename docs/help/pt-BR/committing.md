---
title: Commitando
category: Trabalhando com mudanças
order: 31
summary: Estilos de mensagem, templates, coautores e o linter.
keywords: commit mensagem message compositor composer conventional gitmoji ticket amend template coautor co-author linter desfazer undo reset
---

# Commitando

## Estilos de mensagem

Escolha um nas Configurações; o compositor se adapta a ele.

| Estilo | Se parece com |
|---|---|
| **Conventional** | `feat(api)!: add rate limiting` — com um dropdown de tipo |
| **Gitmoji** | `✨ add rate limiting` — com um seletor de emoji |
| **Ticket** | `ABC-123: add rate limiting` — semeado a partir do nome da branch |
| **Simples** · **Auto** | O que você digitar; o Auto deixa a IA decidir o formato |
| **Homem das cavernas** · **Haicai** | Exatamente o que o nome sugere |

![Compositor pré-preenchido a partir de um template de commit](../../screenshots/commit-template.webp)

## Coisas que o compositor faz por você

- <kbd>↑</kbd> <kbd>↓</kbd> trazem de volta suas **mensagens recentes**.
- Um **seletor de coautores** adiciona trailers `Co-authored-by:` a partir dos
  próprios contribuidores do repositório.
- `commit.template` / `.gitmessage` **pré-preenchem** a mensagem, com as linhas de
  comentário removidas.
- Durante um merge, cherry-pick ou revert, a mensagem vem **pré-preenchida** do
  jeito que o git faria.
- Rascunhos **persistem** por repositório, então trocar de aba nunca perde uma
  mensagem.

## O linter

Uma verificação ao vivo e não bloqueante: comprimento do assunto (com contador de
caracteres), ponto final sobrando, assunto não imperativo ou em minúsculas, linhas
do corpo largas demais. São dicas, nunca um portão — ele não vai impedir você de
commitar.

## Amend

O amend reescreve o último commit com o que estiver preparado. O Gitcito mostra a
mensagem existente primeiro, então você está editando, não redigitando.

**Fazer amend no commit…** numa linha do grafo faz a mesma coisa para o HEAD:
carrega a mensagem completa, coloca o compositor em modo amend e o foca. Um HEAD
que já foi enviado ainda pode receber amend, mas o Gitcito avisa que atualizar o
remoto vai exigir um force push.

### Fazer amend no commit de outra pessoa

Um amend mantém **o autor e a data de autoria** do commit original — é uma regra do git, não do Gitcito. Isso está certo quando você corrige um erro de digitação no commit de um colega, e errado quando você junta trabalho novo seu a ele: o resultado é creditado ao colega, e o grafo mostra o nome dele em código que ele nunca escreveu.

Por isso, quando o email do autor do HEAD é diferente do seu `user.email`, o compositor diz de quem é o commit em que você está fazendo amend e oferece **Tornar-me o autor**. Marcado, o amend usa `--reset-author`: você passa a ser o autor, com a data de agora. Deixe desmarcado para manter o nome dele.

![Modo amend num commit escrito por outra pessoa](../../screenshots/amend-author.webp)

A comparação é por email, então quem faz commit como `Elisa` e como `elisa` conta como o mesmo autor. Só recorre ao nome quando falta um email, e não diz nada se o repositório não tiver nenhuma identidade configurada. Só verifica o commit em que você faz amend: um cherry-pick ou rebase que depois o leve para outro lugar mantém o autor que ele tiver então.

⌘Z depois de um amend traz de volta o commit alterado, com suas mudanças novas ainda no stage, como antes do amend — não volta para o pai desse commit.

**Desfazer commit…** é o irmão para um HEAD não enviado: reset mixed para o pai,
mudanças da árvore de trabalho mantidas, mensagem restaurada no compositor. O
commit inicial tem um caminho dedicado que deixa uma branch não nascida em vez
de destruir os arquivos.

**Veja também:** [Staging](staging.md) · [Absorb](absorb.md) · [Gerador de changelog](changelog.md)
