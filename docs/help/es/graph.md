---
title: El grafo de commits
category: Repositorio e historial
order: 10
summary: Leer el historial: carriles, refs, columnas, filtros y selección múltiple.
keywords: grafo graph historial commits carriles lanes ramas branches fusiones merges columnas filtro lineal first-parent amend enmendar deshacer undo reset github
---

# El grafo de commits

Ramas, fusiones y fusiones pulpo dibujadas como es debido, en claro o en oscuro.
El renderizado va por ventanas, así que un repositorio con cien mil commits se
desplaza igual que uno con cien.

| | |
|---|---|
| ![Grafo de commits, claro](../../screenshots/graph-light.webp) | ![Grafo de commits, oscuro](../../screenshots/graph-dark.webp) |

## Moverse por él

- <kbd>↑</kbd> <kbd>↓</kbd> (o <kbd>j</kbd> <kbd>k</kbd>) recorren la selección.
- <kbd>⌘</kbd>/<kbd>Ctrl</kbd>+clic mete o saca un commit de una **selección
  múltiple**; <kbd>⇧</kbd>+clic coge un rango. Con varios seleccionados, haz
  clic derecho para hacerles cherry-pick sobre la rama actual, aplastar un tramo
  contiguo, exportar un único parche combinado, o copiar sus SHA.
- Los commits que llegaron en tu **último fetch o pull** se marcan como nuevos.
  Los que aún no están en la rama activa se ven algo translúcidos hasta que un
  pull los incorpora.
- Clic derecho en un commit para **Enmendar**, **Deshacer**, **Restablecer al
  commit…** y **Ver en GitHub**, además de checkout, cherry-pick, revert, rama,
  etiqueta y copiar. Las acciones inseguras siguen visibles y se deshabilitan.

## Que muestre lo que tú quieres

- El **enfoque del grafo** decide cuánto historial se dibuja — Ajustes → Temas →
  **Grafo**, o el menú del engranaje en la cabecera del grafo. *Todo* lo dibuja
  entero; *Historial lineal* (first-parent) deja sólo el tronco; *Ocultar ramas
  fusionadas* deja el tronco más las ramas aún sin fusionar; *Modo solo* deja tu
  rama, tus ramas favoritas y la rama por defecto.

  Sólo filtra lo que el log ya ha cargado. *Ocultar ramas fusionadas* se fía de
  la respuesta de git a «ya está contenida en la rama actual», así que cambiar de
  rama cambia lo que esconde — y conserva todo commit al que aún apunte una
  etiqueta o una ref que no reconozca, que es justo lo que deja atrás una rama
  borrada. *Historial lineal* y *Modo solo* son más bruscos: una etiqueta o un
  stash que vivan en un commit que ocultan se van con él.

- **Filtrar por ruta**: clic derecho en un archivo o carpeta → *Filtrar el grafo
  por esta ruta*, y sólo se quedan encendidos los commits que la tocaron.

![El grafo filtrado a una sola ruta](../../screenshots/graph-path-filter.webp)

- **Columnas**: muestra, esconde, redimensiona y reordena las columnas de rama,
  mensaje, autoría, fecha, SHA, firma y despliegue.
- **Estilo**: Ajustes → Temas → **Grafo** — paleta de carriles (8 integradas,
  personalizada o generada por IA), estilo de las esquinas, densidad de filas y
  grosor de línea, con una vista previa en miniatura en vivo.

![Ajustes de estilo del grafo con vista previa en vivo](../../screenshots/settings-graph.webp)

## Detalles del commit

Al seleccionar un commit se ven sus archivos modificados (en árbol o en plano),
la autoría, el SHA, los coautores y su firma. Las referencias `#123` y las
`@menciones` se enlazan automáticamente a tu hosting.

Sobre la lista de archivos, un **resumen de cambios** desglosa el commit por tipo
en lugar de dar un único total — *5 modificados*, *1 añadido*, *1 eliminado*, más
*renombrados* y *en conflicto* cuando el commit los tiene. Cada uno lleva el color
del glifo de estado de las filas de abajo y de los contadores de las carpetas
plegadas, así que el mismo color significa lo mismo en todo el panel. Los tipos
sin archivos se omiten por completo: una edición corriente muestra una entrada, no
cinco. Pasa el ratón por encima para ver el total "*n* archivos cambiados".

![Resumen de cambios sobre la lista de archivos de un commit: 6 modificados, 2 añadidos, 1 eliminado, 1 renombrado](../../screenshots/change-summary.webp)

Dos cosas que deliberadamente no te dice. Cuenta **archivos, no líneas**: una
corrección de un carácter y una reescritura son ambas *1 modificado*; es el diff
lo que revela cuánto ha cambiado. Y cuenta todos los archivos del commit, no el
subconjunto que coincide con un filtro o una búsqueda activa.

El mismo resumen encabeza el panel de preparación y la lista de archivos de un
stash. En el panel de preparación los archivos sin seguimiento cuentan como
adiciones, así que *añadido* significa "no estaba en el último commit", no "ya
preparado".

La lista de archivos se selecciona en grupo con los gestos habituales (clic con
<kbd>⌘</kbd>/<kbd>Ctrl</kbd>, clic con <kbd>⇧</kbd>,
<kbd>⇧</kbd>+<kbd>↑</kbd>/<kbd>↓</kbd>). Clic derecho sobre la selección →
*Restaurar {n} archivos al árbol de trabajo* toma esos archivos exactamente
como estaban en este commit: tras una única confirmación sobrescribe las copias
de trabajo, sin tocar HEAD ni el índice.

![Recorriendo los detalles de un commit](../../screenshots/clip-commit-details.webp)

**Ver también:** [Blame e historial de archivo](blame.md) · [Búsqueda](search.md) · [Máquina del tiempo](time-machine.md)
