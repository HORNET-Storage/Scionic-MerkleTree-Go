# The shared vectors

`manifest.json` is what the Rust, Go and Swift libraries are held to, byte for byte. The format itself is `../FORMAT.md`. The Rust library makes this file (`tests/vectors.rs`), and its tests fail when the file and the library disagree. A library in another language reads the file and has to arrive at the same bytes.

Every hash and every run of bytes is written as lower-case hex. A root is also written as text, as it is shown to a person.

## The bytes of a file

No file's bytes are in the manifest. A file is described as parts, and each part is made from a seed and a length:

- block `k` is `SHA-256(seed + k)`, where the seed is its UTF-8 bytes and `k` is 8 bytes, big-endian, counted from 0;
- the blocks are joined, and cut to the length.

A file is its parts, joined. A file with no parts has no bytes.

## What is in it

- **`trees`:** whole trees. Each has a name, the way it was cut (`cutting`), the record's other extra values (`extra`), and what is on top (`top`): a file as its parts, or a folder as its entries, each with a name, its own extra values if it has any, and a file or folder under it. An extra value is given as text.
  - A library builds the tree from that, and must arrive at the `root`, the `record`, the totals (`size`, `pieces`, `piece_bytes`, `list_bytes`), every list in `lists` with its hash, size and bytes, and every piece in `piece_list` with its hash and length.
  - `lists` and `piece_list` are in the order a bundle holds them: each after what names it. The bundle itself is given by its length and its SHA-256.
- **`runs`:** parts of the tree named `folder`, as they are shown. Each gives the file or folder it is a part of (`of`), the items shown (`start` up to `end`), and the `frame`. A library makes the same frame, and checks it against `of`. For a file, the run starts at byte `starts_at_byte`.
- **`names`:** one name of a folder, as it is answered. `found` says whether the folder has the name. Either way the frame proves it.
- **`answers`:** requests, and the frames that the holder of the tree named `folder` answers with.
- **`connection`:** three of those requests as they are sent on a connection, and everything that comes back.
- **`refused`:** bytes that must be refused. `as` says what they are offered as:
  - `record`, `file-list`, `folder-list`, `request`, `frame`: reading them is an error.
  - `file-list-cut`: the list reads, and does not fit the way of cutting in `cutting`.
  - `file-run`, `folder-run`: reading the run, or checking it against the file or folder in `against`, is an error.
  - `folder-name`: the run is offered as the answer for `name` in the folder in `against`, and is no answer for it.
  - `root`: `text` is no root.

## Where files are cut

`../fastcdc-vectors/manifest.json` holds files that are cut by content. Each case gives where every piece ends (`boundaries`), every piece's SHA-256 (`chunk_sha256`), and the hash of the file (`file_hash`).

- A case of the kind `corpus` is one part, made from its `seed` and `length` as above.
- `zeros` is that many zero bytes.
- `edit` takes the case named in `base`. Where `op` is `insert`, it puts `edit_length` bytes made from `payload_seed` in at `offset`. Where `op` is `delete`, it takes `edit_length` bytes out from `offset` on. A number that is left out is 0. `reused` says, for each of its pieces, whether the base has that piece too.

## Making them again

When the format changes on purpose:

```sh
SCIONIC_WRITE_VECTORS=1 cargo test --locked --offline --test vectors
```

Then read what changed in the two files. A forged vector is written only once the library has refused it.
