# The Pictures of the README

The README has three pictures. One program draws all of them, so they share one theme and one example tree:

| Picture | File |
| --- | --- |
| Comparison of Many Merklized Forms | [`merklized-forms.png`](merklized-forms.png) |
| Scionic Merkle Tree | [`scionic-merkle-tree.png`](scionic-merkle-tree.png) |
| Scionic Merkle Branch | [`scionic-merkle-branch.png`](scionic-merkle-branch.png) |

## Drawing Them Again

```sh
node docs/pictures/pictures.mjs <folder> "C:\Program Files\Google\Chrome\Application\chrome.exe"
python docs/pictures/shrink.py <folder>/merklized-forms.png docs/pictures/merklized-forms.png
python docs/pictures/shrink.py <folder>/scionic-merkle-tree.png docs/pictures/scionic-merkle-tree.png
python docs/pictures/shrink.py <folder>/scionic-merkle-branch.png docs/pictures/scionic-merkle-branch.png
```

`<folder>` is any folder outside the repository. The first command writes each picture into it three times: as an `.svg`, as an `.html` that checks its own layout, and as a `.png` at two pixels to a unit.

- **[`pictures.mjs`](pictures.mjs)** needs Node 20 or newer, and Chrome or Edge for the `.png` files. Without the path of a browser it writes the `.svg` and `.html` files only.
- **The browser checks a picture before it takes it.** Every text is inside its box. No text lies on another text, nor on a box that is not its own. No link crosses a box or a text. Every box is on the canvas. If anything is wrong, the program says what, takes no picture, and ends with an error.
- **The pictures are drawn with Segoe UI and Consolas,** so they are drawn on Windows. On a machine without those fonts the check says so and takes no picture, because the same drawing in another font is a different layout.
- **[`shrink.py`](shrink.py)** needs Python 3 and Pillow. It gives a picture a palette of 256 colours, which makes the file about a third of the size. Every flat colour stays exactly as it was, and the program prints how far the furthest shade along an edge moved. For these pictures that is 8 of 255 at most, and it refuses a picture in which a colour would move further.

## Changing Them

Everything a picture says is at the top of `pictures.mjs` or in the function that draws it:

- **`SYNC`** is the name of the protocol that asks for a file's chunks by their numbers.
- **`TOP`, `LEFT` and `RIGHT`** are the names in the example tree. They must be in the order of their bytes, as a folder's entries are, and the program refuses them otherwise.
- **`PROVEN`** is the chunk that the branch picture proves. What is shown, what is sent beside it and what the receiver works out follow from it by the rule in [`spec/FORMAT.md`](../../spec/FORMAT.md), under "Showing a part".
- **The rows of the table** are in `table()`.

## The Colours

| Colour | Value | What it is for |
| --- | --- | --- |
| Canvas, box, raised | `#121430`, `#1D203E`, `#25284B` | the surfaces, which are those of the HORNET Storage relay panel |
| Sky | `#3ea1c3` | folders, files, chunks and the links between them |
| Amber | `#ba8c02` | the Classic Merkle tree inside a folder or a file |
| Green | `#06a763` | yes in the table, and the branch |
| Red | `#f7502a` | no in the table |

Sky and amber are the two colours of the HORNET Storage mark, each at the step of lightness that suits a dark surface.

- **No colour says anything alone.** A yes has a tick and the word, a no has a cross and the word, and in the branch picture what is sent is filled, what is worked out is a ring, and the rest is faded.
- **Colours that are seen together can be told apart by readers who are colour-blind.** Each pair was measured as protanopia and deuteranopia show it: sky and amber are 20 apart, and green and red are 8.6, where 8 is the least that is safe. The green of the branch stands beside faded colours only, and is more than 22 from each.
- **Every mark has a contrast of 3 to 1 or more against what it is drawn on,** and every text 6 to 1 or more. The faded part of the branch picture is the one exception, and is faded on purpose.
