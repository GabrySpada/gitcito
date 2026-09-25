---
title: Preparación
category: Trabajar con cambios
order: 30
summary: Prepara archivos enteros, hunks sueltos o líneas concretas.
keywords: preparacion preparar stage staging unstage descartar discard hunk lineas indice index parcial
---

# Preparación

El panel de commit tiene tres listas: **En conflicto**, **Sin preparar** y
**Preparado**. Cada una se pliega, y cada una recuerda si la dejaste abierta.

![Un diff sin preparar, con los controles de hunk y de archivo al lado](../../screenshots/line-staging.webp)

## Tres niveles de precisión

| Nivel | Cómo |
|---|---|
| **Archivo** | Pulsa el ✚ de la fila, o selecciona varias filas y prepáralas de golpe |
| **Hunk** | Abre el diff y usa el botón de la cabecera del hunk |
| **Línea** | Pasa el ratón sobre una línea cambiada del diff y pulsa su **+**, o selecciona varias líneas y prepáralas |

Preparar por líneas es lo que hace práctico dejar fuera del commit un
`console.log` de depuración sin tener que borrarlo antes.

## Preparar líneas sueltas

Abre el diff de un archivo sin preparar, en la vista unificada o en la
dividida. Pasa el ratón sobre una línea añadida o eliminada y aparece un
pequeño **+** verde al principio: un clic prepara esa línea y nada más. El
resto del bloque queda sin preparar, exactamente como si hubieras editado el
bloque a mano en `git add -p`.

Para varias líneas a la vez, haz clic en las propias líneas para
seleccionarlas — <kbd>⇧</kbd>-clic toma todas las líneas cambiadas desde la
última que pulsaste — y pulsa **Preparar N línea(s)** en la barra sobre el diff.

También funciona al revés. Abre la versión **preparada** de un archivo y los
controles pasan a ser un **−** rojo, **Quitar el bloque del área de
preparación** y **Quitar N línea(s) del área de preparación**: sacan líneas del
índice y no tocan el árbol de trabajo.

Si quitas el último cambio del lado que estás viendo, el diff sigue al archivo
al otro lado en vez de quedarse vacío.

Cada línea o bloque que prepares o quites así se deshace con **Deshacer** en la
barra de herramientas, un clic cada vez.

| Eliges | Al prepararla | Al quitarla |
|---|---|---|
| Una línea añadida | El índice gana esa línea | El índice pierde esa línea |
| Una línea eliminada | El índice pierde esa línea | La línea vuelve al índice |
| Ninguna, en el mismo bloque | Queda igual, solo en el árbol de trabajo | Sigue preparada |

### Límites

- **Espacios ocultos, sin preparación.** Mientras se ignoran los *Espacios*, el
  diff omite cambios y no puede decir qué líneas preparar; los controles se
  ocultan hasta que lo desactives.
- **Los archivos sin seguimiento** se preparan enteros. Prepara primero el
  archivo y luego quita las líneas que no quieras.
- **La última línea de un archivo sin salto de línea final** puede rechazarse
  si separas su cambio de líneas añadidas después: git no puede describir ese
  estado a medias como un parche. Prepáralas juntas.
- **Deshacer necesita el índice tal como lo dejaste.** Si desde entonces
  preparaste más de esas mismas líneas, git rechaza deshacer en vez de adivinar,
  y lo dice.

## Descartar

Descartar funciona en los mismos niveles, y siempre pregunta. Los archivos sin
seguimiento se borran; los que sí lo tienen vuelven a su estado preparado (o al
del commit).

## Teclado

<kbd>↑</kbd> <kbd>↓</kbd> (o <kbd>j</kbd> <kbd>k</kbd>) recorren las listas de
archivos, con <kbd>⇧</kbd> para un rango y <kbd>⌘</kbd>/<kbd>Ctrl</kbd> para
marcar archivos sueltos.

<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd> amplía la selección desde la última fila
en la que hiciste clic. Haz clic derecho sobre la selección para preparar,
quitar de preparados, guardar en stash o descartar todo de una vez.

## Copiar rutas

El clic derecho sobre un archivo sin commitear ofrece **Copiar ruta del
archivo** (absoluta, con los separadores de la plataforma) y **Copiar ruta
relativa del archivo** (`src/index.ts`, sin `./` inicial). Varios archivos
seleccionados copian una ruta por línea, en el orden de la lista. Los archivos
eliminados siguen disponibles: esas acciones solo copian texto. Las carpetas
siguen copiando la ruta de la carpeta.

## Antes de hacer commit

Gitcito comprueba unas cuantas cosas y pregunta una vez, nunca en silencio:

- un archivo que parece un **secreto** (`.env`, `*.pem`, `id_rsa`…),
- un blob **muy grande** (el umbral está en Ajustes → Seguridad),
- hacer commit **directamente a una rama protegida** (`main`/`master` por
  defecto).

Cada uno de esos casos ofrece un *Ignorar y dejar de seguir* de un clic. Mira
[Seguridad y secretos](security.md).

**Ver también:** [Hacer commits](committing.md) · [Diffs](diffs.md) · [Absorb](absorb.md)
