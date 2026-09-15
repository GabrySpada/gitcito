---
title: Pierwsze kroki
category: Zacznij tutaj
order: 1
summary: Otwórz repozytorium, przeczytaj graf, zrób pierwszego commita.
keywords: wprowadzenie start pierwsze kroki otwórz sklonuj karty graf commit intro open clone tabs graph
---

# Pierwsze kroki

Gitcito otwiera katalog i pokazuje jego historię. Nic nie zostaje zapisane do
twojego repozytorium, dopóki o to nie poprosisz.

![Świeżo otwarte repozytorium, jeszcze bez commitów](../../screenshots/empty-repo.webp)

## Otwórz repozytorium

- **Przeciągnij katalog** na okno albo użyj **Otwórz repozytorium** na ekranie
  powitalnym.
- **Sklonuj** je z adresu URL lub prosto z hostingu — zobacz
  [klonowanie](cloning.md), żeby poznać opcje, dzięki którym ogromne
  repozytorium klonuje się szybko.
- Z terminala `gitcito .` otwiera bieżący katalog w działającej aplikacji —
  zobacz [wiersz poleceń](cli.md).
- Katalog, który nie jest jeszcze repozytorium Gita, też się otworzy — aplikacja
  zaproponuje jego zainicjowanie.

## Trzy panele

| Panel | Co zawiera |
|---|---|
| Lewy | Gałęzie, zdalne repozytoria, tagi, stashe, worktree — oraz zakładkę **Pliki** z drzewem roboczym |
| Środkowy | Graf commitów i to, co z niego wybierzesz |
| Prawy | Kompozytor commita albo szczegóły zaznaczonego commita |

## Jak znaleźć całą resztę

Dwie drogi, prowadzące w te same miejsca:

- **`⌘K`** (`Ctrl+K`) — paleta poleceń. Wpisz, czego szukasz; skacze też do
  gałęzi, commitów i plików.
- **Narzędzia** na pasku — ten sam zestaw działający na repozytorium, tyle że
  jako menu, z długim ogonem zwiniętym w grupy, żeby dało się to czytać.

![Menu Narzędzia: najpierw te używane najczęściej, reszta pogrupowana](../../screenshots/tools-menu.webp)

Pasek akcji trzyma przyciski na środku **okna**, a nie w przestrzeni między nazwą repozytorium a polem wyszukiwania — zostają więc w tym samym miejscu, gdy przechodzisz między repozytoriami o zupełnie różnej długości nazw. Nazwy repozytorium i brancha widać w całości; ta, która jest na tyle długa, że zagraża reszcie paska, kończy się wielokropkiem, a w podpowiedzi przycisku zostaje cała.

Miejsce mierzy się od tego środka na zewnątrz i to jest cena jego utrzymania: gdy okno się zwęża albo nazwa repozytorium jest bardzo długa, pasek oddaje miejsce, zamiast się przesuwać. Pierwsze ustępuje pole wyszukiwania, wymieniając swoje okienko na lupę — kliknij ją, by szukać; zostaje otwarte tak długo, jak długo działa filtr. Potem przyciski, które już się nie mieszczą, zwijają się do menu **Więcej** na końcu paska — w kolejności paska i z własnymi podmenu. Poszerz okno, a wrócą na swoje miejsce.

![Pasek akcji w wąskim oknie: wyszukiwanie zwinięte do lupy, koniec paska w menu „Więcej“, a przyciski nadal wyśrodkowane](../../screenshots/toolbar-narrow.webp)

Wszystko, co da się osiągnąć jedną drogą, da się osiągnąć i drugą — nie ma więc
niczego, co znajdą wyłącznie użytkownicy zaawansowani.

## Twój pierwszy commit

1. Zmień plik. Pojawi się w sekcji **Poza przechowalnią**.
2. Dodaj go do przechowalni — cały plik, pojedynczy hunk albo
   [pojedyncze linie](staging.md).
3. Napisz wiadomość i naciśnij **Commit**.

Cała reszta Gitcito jest opcjonalna.

