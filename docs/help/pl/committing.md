---
title: Commitowanie
category: Praca ze zmianami
order: 31
summary: Style wiadomości, szablony, współautorzy i linter.
keywords: commit wiadomość kompozytor szablon współautor linter amend conventional gitmoji ticket template co-author cofnij reset undo
---

# Commitowanie

## Style wiadomości

Wybierz jeden w Ustawieniach; kompozytor się do niego dostosuje.

| Styl | Wygląda tak |
|---|---|
| **Conventional** | `feat(api)!: add rate limiting` — z listą typów |
| **Gitmoji** | `✨ add rate limiting` — z wyborem emoji |
| **Ticket** | `ABC-123: add rate limiting` — zasiane z nazwy gałęzi |
| **Zwykły** · **Auto** | Cokolwiek napiszesz; Auto pozwala AI zdecydować o kształcie |
| **Jaskiniowiec** · **Haiku** | Dokładnie to, na co wyglądają |

![Kompozytor wypełniony z szablonu commita](../../screenshots/commit-template.webp)

## Co kompozytor robi za ciebie

- <kbd>↑</kbd> <kbd>↓</kbd> przywołuje twoje **ostatnie wiadomości**.
- **Wybór współautora** dodaje stopki `Co-authored-by:` na podstawie osób, które
  faktycznie współtworzyły to repozytorium.
- `commit.template` / `.gitmessage` **wypełnia** wiadomość wstępnie, z pominięciem
  linii komentarza.
- W trakcie merge'a, cherry-picka czy reverta wiadomość jest **wypełniona
  wstępnie** tak, jak zrobiłby to git.
- Szkice **zostają zapamiętane** dla każdego repozytorium z osobna, więc
  przełączenie karty nigdy nie gubi wiadomości.

## Linter

Sprawdzenie na żywo, które niczego nie blokuje: długość tematu (z licznikiem
znaków), kropka na końcu, temat nie w trybie rozkazującym albo z małej litery,
zbyt szerokie linie w treści. Podpowiedzi, nigdy bramka — nie powstrzyma cię
przed zacommitowaniem.

## Amend

Amend przepisuje ostatni commit tym, co jest w przechowalni. Gitcito pokazuje ci
najpierw istniejącą wiadomość, żebyś ją edytował, a nie przepisywał od nowa.

**Popraw commit…** na wierszu grafu robi to samo dla HEAD: wczytuje pełną
wiadomość, przełącza kompozytor w tryb amend i ustawia na nim fokus. HEAD, który
został już wypchnięty, wciąż da się poprawić, ale Gitcito ostrzega, że
zaktualizowanie zdalnego będzie wymagało force pusha.

### Poprawianie cudzego commita

Amend zachowuje **autora i datę autorstwa** oryginalnego commita — to reguła gita, nie Gitcito. Jest to właściwe, gdy poprawiasz literówkę w commicie kolegi, i niewłaściwe, gdy dokładasz do niego własną nową pracę: wynik zostaje przypisany jemu, a graf pokazuje jego nazwisko przy kodzie, którego nigdy nie napisał.

Dlatego gdy e-mail autora HEAD różni się od twojego `user.email`, kompozytor mówi, czyj commit poprawiasz, i proponuje **Ustaw mnie jako autora**. Po zaznaczeniu amend używa `--reset-author`: stajesz się autorem, z bieżącą datą. Zostaw niezaznaczone, by zachować jego nazwisko.

![Tryb amend na commicie napisanym przez kogoś innego](../../screenshots/amend-author.webp)

Porównywane są adresy e-mail, więc ktoś commitujący jako `Elisa` i `elisa` liczy się jako ten sam autor. Na nazwę przechodzi tylko wtedy, gdy brakuje e-maila, i milczy, jeśli repozytorium nie ma skonfigurowanej żadnej tożsamości. Sprawdzany jest tylko poprawiany commit: późniejszy cherry-pick lub rebase, który go przeniesie, zachowa autora, jakiego commit wtedy ma.

⌘Z po amendzie przywraca poprawiony commit, a twoje nowe zmiany zostają w przechowalni jak przed amendem — nie cofa się do rodzica tego commita.

**Cofnij commit…** to bliźniacza akcja dla niewypchniętego HEAD: mixed reset do
rodzica, zmiany w drzewie roboczym zachowane, wiadomość przywrócona do
kompozytora. Pierwszy commit ma dedykowaną ścieżkę, która zostawia nienarodzoną
gałąź, zamiast niszczyć pliki.

**Zobacz też:** [Przechowalnia](staging.md) · [Absorb](absorb.md) · [Generator changelogu](changelog.md)
