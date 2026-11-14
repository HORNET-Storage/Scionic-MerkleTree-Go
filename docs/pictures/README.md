# The Pictures of the README

The README has three pictures. One program draws all of them, so they share one theme and one example tree:

| Picture | File |
| --- | --- |
| Comparison of Many Merklized Forms | [`merklized-forms.png`](merklized-forms.png) |
| Scionic Merkle Tree | [`scionic-merkle-tree.png`](scionic-merkle-tree.png) |
| Scionic Merkle Branch | [`scionic-merkle-branch.png`](scionic-merkle-branch.png) |

The Rust library's README shows the same three pictures, from copies of these files in its own repository, under `docs/pictures`. When a picture is drawn again, the three `.png` files are copied there.

## Drawing Them Again

```sh
node docs/pictures/pictures.mjs <folder> "C:\Program Files\Google\Chrome\Application\chrome.exe"
python docs/pictures/shrink.py <folder>/merklized-forms.png docs/pictures/merklized-forms.png
python docs/pictures/shrink.py <folder>/scionic-merkle-tree.png docs/pictures/scionic-merkle-tree.png
python docs/pictures/shrink.py <folder>/scionic-merkle-branch.png docs/pictures/scionic-merkle-branch.png
```

`<folder>` is any folder outside the repository. The first command writes each picture into it three times: as an `.svg`, as an `.html` that checks its own layout, and as a `.png` at two pixels to a unit.

- **[`pictures.mjs`](pictures.mjs)** needs Node 20 or newer, and Chrome or Edge for the `.png` files. Without the path of a browser it writes the `.svg` and `.html` files only.
- **The browser checks a picture before it takes it.** Every text is inside its box. No text lies on another text, nor on a box that is not its own. No link crosses a box or a text, and no two links cross or run within 8 units of each other. Every box is on the canvas. If anything is wrong, the program says what, takes no picture, and ends with an error.
- **The pictures are drawn with Segoe UI and Consolas,** so they are drawn on Windows. On a machine without those fonts the check says so and takes no picture, because the same drawing in another font is a different layout.
- **[`shrink.py`](shrink.py)** needs Python 3 and Pillow. It gives a picture a palette of 256 colours, which makes the file about a third of the size. Every flat colour stays exactly as it was, and the program prints how far the furthest shade along an edge moved. For these pictures that is 8 of 255 at most, and it refuses a picture in which a colour would move further.

## Changing Them

Everything a picture says is at the top of `pictures.mjs` or in the function that draws it:

- **`SYNC`** is the name of the protocol that asks for a file's chunks by their numbers: LeafSync.
- **`TOP`, `LEFT` and `RIGHT`** are the names in the example tree: the folder on top holds two folders and two files, and each of those two folders holds three files. The names must be in the order of their bytes, as a folder's entries are, and the program refuses them otherwise. The first two names of `TOP` are the two folders.
- **The two folders stand side by side, with the files to the right of them.** The pictures draw a folder's entries in the order of their names. So the files of the folder on top have names that sort after both folders, as `test.csv` and `train.csv` sort after `model` and `samples`.
- **`CHUNKS`** is how many chunks each file has. A file holds what a folder holds: the Classic Merkle tree over its list, with its chunks' numbers under the tree's leaves, and its chunks under those.
- **A folder and a file are told apart by their outlines.** A folder's box has a tab over its top left corner, as a folder has. A file's box has its top right corner folded down, as a sheet has. The box is itself the picture of a folder or of a file, so its word is small and has no picture beside it. In the tree picture the tab and the folded corner are tinted. In the branch picture they are outlines only, because there a fill says what is shown or sent.
- **What a hash covers stands in a box of its own,** close under the name of every folder and file: the count and the size of its list, and its Classic Merkle root. Those three are what the hash of a folder or of a file covers, as [`spec/FORMAT.md`](../../spec/FORMAT.md) has it under "A file" and "A folder". A link ends at that box and the tree hangs from it, so a line always ends at what its hash is the hash of. The Classic Merkle root alone is not what the entry above holds.
  - In the tree picture the box looks as a chunk does, because both are what a link by hash ends at.
  - In the branch picture it is a ring on the way to the chunk, because the receiver works the three out, and it is faded everywhere else.
- **No list in these pictures has fewer than three items.** The tree over two items is a single fork, which does not look like a tree. So the program refuses a folder with fewer than three entries, and a file with fewer than three chunks. The format itself has smaller ones: a file of 512 KiB or less is a single chunk, and its tree is that one leaf.
- **Every folder and file box has one height,** that of the deepest tree, so that the boxes of a row line up at the top and at the bottom. A folder's tab stands over that height. A tree of fewer levels is drawn as tall as the deepest: every Classic Merkle root stands at one height in its box, and so does every leaf.
- **Each row stands in the middle of the picture.** Two boxes that belong together are 12 units apart, and two sets of them are 26 apart: the folders and the files of the second row, and the two folders' files in the bottom row.
- **The bottom row stands as close under the second as its links allow:** 123 units, in `apart`. The links from the second folder pass under the chunks that hang from the two files beside it, so each link falls straight for a while and then goes sideways. At 123 units a link has 14 units to the chunks over it and 13 to the next link. Closer rows bunch the links, and the check refuses two that come within 8 units of each other.
- **The two tree pictures are 1,424 units wide,** wider than the table, so that the bottom row holds six files with their chunks at a size that can be read, and with room between one chunk and the next. If the files are given more chunks than fit, the program refuses them, and says how much room they take and how much there is.
- **`PROVEN`** is the chunk that the branch picture proves, in the first file of the first folder. What is shown, what is sent beside it and what the receiver works out follow from it by the rule in [`spec/FORMAT.md`](../../spec/FORMAT.md), under "Showing a part".
- **The key of a tree picture stands on both sides of the root,** and fills the room beside the record and the folder on top. An entry is a title with two lines under it, and a small picture at their left. `keys` says where the key stands and how far its entries are apart. A line that is too long for its side is refused by the check, so it is put in fewer words.
- **The rows of the table and its three forms** are in `table()`.
- **The name of a form is the largest thing in its column.** The word that tells it from the other two is written large, over the rest of its name: `IPFS` over `Merkle DAGs`. Each form has a small picture of itself in the colours of the two tree pictures: boxes that link to boxes for a Merkle DAG, and a Classic Merkle tree. The Scionic Merkle Tree's is both in one: the boxes of the first, joined through the nodes of the second.
- **The table fills its picture.** Its title stands in the corner that a table leaves empty, beside the names of the forms. A mark is a badge that is no larger than what it says, and the picture is as wide as its columns and no wider, so that its text is large at the width of a page.

## The Colours

| Colour | Value | What it is for |
| --- | --- | --- |
| Canvas, box, raised | `#121430`, `#1D203E`, `#25284B` | the surfaces, which are those of the HORNET Storage relay panel |
| Sky | `#3ea1c3` | folders, files, chunks, what a hash covers, and the links between them |
| Amber | `#ba8c02` | the Classic Merkle tree inside a folder or a file |
| Green | `#06a763` | yes in the table, and the branch |
| Red | `#f7502a` | no in the table |

Sky and amber are the two colours of the HORNET Storage mark, each at the step of lightness that suits a dark surface.

- **No colour says anything alone.** A yes has a tick and the word, a no has a cross and the word, and in the branch picture what is sent is filled, what is worked out is a ring, and the rest is faded.
- **Colours that are seen together can be told apart by readers who are colour-blind.** Each pair was measured as protanopia and deuteranopia show it: sky and amber are 20 apart, and green and red are 8.6, where 8 is the least that is safe. The green of the branch stands beside faded colours only, and is more than 22 from each.
- **Every mark has a contrast of 3 to 1 or more against what it is drawn on,** and every text 6 to 1 or more. The faded part of the branch picture is the one exception, and is faded on purpose.
