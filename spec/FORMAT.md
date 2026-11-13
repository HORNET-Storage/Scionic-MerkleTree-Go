# The Scionic tree format

This is the format that the Rust, Go and Swift libraries write and check. It says what every hash covers, what a receiver checks, and the bytes of every request and answer. The code in `src/` is held to it by the tests, and the three libraries are held to each other by the vectors in `spec/vectors/` and `spec/fastcdc-vectors/`.

## In short

- A tree holds a folder of files, or one file, under one root.
- **A piece** is a run of a file's bytes. Its hash is the SHA-256 of those bytes and nothing else.
- **A file** is the list of its pieces, in the file's order. A piece's number is its place in that list.
- **A folder** is the list of its entries, sorted by name. An entry holds a name, and the file or folder under it.
- **The record** says what is on top of a tree and how much the tree holds. **The root** is the hash of the record.
- A thing's hash covers what the thing is. Its parent's hash covers where it sits. So a file has no name of its own and a piece has no number of its own, and identical pieces, files and folders have identical hashes wherever they are.

## Conventions

- Every hash is SHA-256.
- Numbers are big-endian, and as wide as the number in brackets says: `size (8)` is eight bytes.
- `+` joins bytes.
- Reading is strict. Bytes that could be written in two ways are read in one way only, and bytes left over at the end are an error.

## A piece

- A piece holds 1 byte to 8 MiB (8,388,608 bytes).
- **Its hash:** `SHA-256(bytes)`.

## A file

- **An item of a file's list:** `piece hash (32) + length (4)`, 36 bytes. The length is that of the piece, 1 to 8,388,608.
- **A file's list** is its items one after the other. A file with no bytes has no items, and its list is no bytes.
- **A file's size** is the sum of its pieces' lengths.
- **A file's hash:** `SHA-256(0x10 + count (8) + size (8) + list hash (32))`. The count is how many items the list has. The list hash is the top of the list's tree ("The tree of a list").
- A piece may be listed more than once, in one file or in several. It always has the same length.

## A folder

- **An entry,** an item of a folder's list: `kind (1) + size (8) + hash (32) + name length (2) + name + extra data`.
  - The kind is 1 for a file and 2 for a folder.
  - The size and the hash are those of the file or folder under the name.
- **A name** is 1 to 255 bytes of UTF-8. It holds no `/` and no zero byte, and it is not `.` or `..`.
- **Extra data:** `how many (2)`, then for each value `name length (2) + name + value length (2) + value`.
  - There are 32 values at most.
  - A value's name is 1 to 64 bytes of UTF-8. A value is 0 to 1,024 bytes.
  - The values come in the order of their names' bytes, and no name comes twice.
- **A folder's list** is its entries one after the other, in the order of their names' bytes, with no name twice. A folder with no entries has a list of no bytes.
- **A folder's size** is the sum of its entries' sizes. A sum that does not fit in 64 bits is an error.
- **A folder's hash:** `SHA-256(0x11 + count (8) + size (8) + list hash (32))`.

A file's extra data, such as its type, sits beside its name in the folder. So the same bytes under two names are one file with one hash.

## The tree of a list

A list's hash is the top of a tree over its items. Every node of the tree has a hash and a size.

- **A leaf of a file's tree:** `SHA-256(0x00 + item)`. Its size is the item's length.
- **A leaf of a folder's tree:** `SHA-256(0x02 + entry)`. Its size is the entry's size.
- **A fork of a file's tree:** `SHA-256(0x01 + left hash (32) + left size (8) + left items (8) + right hash (32) + right size (8) + right items (8))`. The items are how many items of the list are under that side. The fork's size is the sum of the two sizes.
- **A fork of a folder's tree:** the same with `0x03` in place of `0x01`, and at the end the name of the first entry under the right side, as its bytes with no length before them.
- **The shape.** The node over `m` items that start at item `o`, written `node(o, m)`:
  - if `m` is 1, it is the leaf of item `o`;
  - otherwise `k` is the largest power of two that is less than `m`, and it is the fork of `node(o, k)` and `node(o + k, m - k)`.
- **The list hash** of a list of `n` items is the hash of `node(0, n)`. A list of one item has its leaf's hash. An empty list has 32 zero bytes, and a size of 0.
- A list has 2^40 items at most.

The first bytes differ for every kind of node, so a leaf is never taken for a fork, nor a file's node for a folder's. A fork covers sizes and counts, so a list of another length never has the same hash. A folder's fork covers a name, so a name has one place in a folder: the one a search for it ends at ("Showing a part").

## The record and the root

- **The record:** `0x20 + kind (1) + size (8) + hash (32) + pieces (8) + piece bytes (8) + list bytes (8) + extra data`.
  - The kind, the size and the hash are those of the file or folder on top.
  - **Pieces** is how many different pieces the tree has, each counted once however often it is listed.
  - **Piece bytes** is how many bytes those pieces hold.
  - **List bytes** is how many bytes the lists of the tree's different files and folders take: 36 for each item of each different file's list, and each entry's length for each different folder's list.
- **The record's extra data must hold `chunking`,** which says how the tree's files were cut ("Cutting"). A builder may add `timestamp`: when the tree was built, in UTC to the second, as `2026-11-12T10:00:00Z`. Any other value is the tree author's own.
- **The totals must be possible:** no more pieces than piece bytes; no more piece bytes than the size on top, nor than 8 MiB a piece; at least 36 list bytes a piece; and with a file on top, list bytes that are a multiple of 36. A record that fails this is refused before anything else of the tree is taken.
- A record is 35,012 bytes at most.
- **The root:** `SHA-256(record)`.
- **The root as text** is a CID: the letter `b`, then the base32 (RFC 4648, lower case, no padding) of `0x01 0x55 0x12 0x20 + root`. It is 59 letters and starts with `bafkrei`. No other spelling of it is read.

## Cutting

A tree's record says how its files were cut, and every file in the tree is held to it.

- **`fastcdc-v1`** cuts by content. It is the way files are cut.
  - The constants: `MIN` is 524,288, `TARGET` is 2,097,152 and `MAX` is 8,388,608. The strict mask is `0xFFFFFE0000000000` and the loose mask is `0xFFFFE00000000000`.
  - The gear table has 256 numbers: `G[i]` is the first 8 bytes, as a big-endian number, of `SHA-256("scionic-fastcdc-v1:gear:" + byte i)`.
  - To cut the next piece from the `n` bytes that are left:
    1. If `n` is `MIN` or less, the piece is all of them.
    2. Otherwise `limit` is the lesser of `n` and `MAX`, and `h` starts at 0.
    3. For each `i` from `MIN` up to `limit - 1`: `h` becomes `(h << 1) + G[byte i]`, in 64 bits and with the overflow dropped. If `h` and the mask have no bit in common, the piece ends after byte `i`. The mask is the strict one while `i` is less than `TARGET`, and the loose one from there on.
    4. If no byte ended the piece, it is `limit` bytes long.
  - So every piece but a file's last holds more than 524,288 bytes and no more than 8,388,608, and the last holds 1 to 8,388,608. A file of `S` bytes has between `ceil(S / 8388608)` and `(S - 1) / 524289 + 1` pieces, rounded down.
- **`fixed-<n>`** cuts every `n` bytes, and is for tests. `n` is written in decimal with no leading zero, from 1 to 8388608. Every piece but a file's last holds exactly `n` bytes, and the last holds 1 to `n`.
- A file with no bytes has no pieces, under either.
- The lengths do not show that a file was cut where `fastcdc-v1` would cut it. A checker that holds a file's bytes can cut them again, and refuse a file whose list is not the cutter's. With that on, the same bytes always have the same file hash.
- The steps above are the definition, and a cutter may be faster as long as it cuts at the same bytes. One way: `h` holds no more than the last 64 bytes it took, because every step shifts it one bit and a byte has left it 64 steps later. So several stretches that follow one another can be rolled over side by side, each by its own `h` that starts at 0 and takes the 64 bytes before its stretch first. The Rust library does this with four, and its tests hold the result to the steps above.
- Where a piece ends decides where the next one begins, so a file is cut in order. Hashing the pieces needs no order, and can use every core.

`spec/fastcdc-vectors/manifest.json` gives, for inputs of several kinds, where each piece ends and what each piece and file hashes to.

## Showing a part

A sender can show a run of a list without the rest. It sends the items of the run, and for the rest the nodes that stand for them: the **carried nodes**.

**To check an opening** of items `a` up to `b` of a list of `n` items, work out `node(0, n)` by the shape above, with one more rule that comes first:

- if none of the items of `node(o, m)` is in the run, it is the next carried node.

Every carried node must be used, in that order, and none may be left over. The top that comes out must be the list hash that the file's or folder's hash covers, with the same count and the same size.

**In a folder,** a carried node comes with the name of the first entry under it. At each fork, every entry of the run on the left must sort before the name of the first entry on the right, and every entry of the run on the right at or after it. The entries of a run must themselves be in order, with no name twice.

**One carried node has no name:** the one that begins with the list's first entry. A run has it when `start` is more than 0, and it is then the first carried node. A fork's hash covers the first name of its right half, and this node is in no right half, so no hash would cover a name for it. It is sent with a name length of 0, and a run that gives it a name is refused. Every other carried node has a name of 1 to 255 bytes, and a run that gives one of them no name is refused. So every byte of a run is covered by the hash the run is checked against: no two runs show the same thing.

**To answer for one name,** the sender shows the one entry that a search for the name ends at: left of each fork when the name sorts before the fork's name, and right otherwise. The receiver follows the same search as it checks the opening. If the entry shown has the name, that is the entry. If it has another name, the folder has no entry of that name. Either answer is proven, and a folder that was put together out of order cannot show a name at two places.

- **A run of a file's list:** `count (8) + start (8) + shown (4) + the items shown + carried (2)`, then for each carried node `hash (32) + size (8)`. It shows one item at least.
- **A run of a folder's list:** `count (8) + start (8) + shown (4) + the entries shown + carried (2)`, then for each carried node `hash (32) + size (8) + name length (2) + name`. The name length is 0 for the carried node that begins the list, and 1 to 255 for every other. The run of an empty folder has a count of 0, shows nothing and carries nothing.
- A run carries 80 nodes at most, and lies inside its list: `start + shown` is no more than `count`.
- The size of everything before a run is the sum of the sizes of the carried nodes on its left. For a file, that is the byte at which the run starts.

## What a receiver checks

A receiver starts from a root it trusts. It takes a tree from a peer, from a bundle or out of its own store by the same checks:

1. **The record** must hash to the root, be well formed, and have totals that are possible.
2. **A list** is taken only when a list that is already checked names its hash, or the record does. A folder's list must have the hash and the size its parent gives it, with its entries in order. A file's list must have the hash and the size its parent gives it, and its pieces' lengths and their number must fit the way the tree was cut.
3. **A piece** is taken only when a file's list that is already checked names its hash, and only with the length that list gives it.
4. **A piece listed twice** must have the same length each time. **A file or folder named twice** must have the same size each time.
5. **The totals are a budget.** As lists come in, the different pieces, their bytes and the lists' bytes are counted, and the receiver stops as soon as a count goes over what the record says.
6. **At the end** nothing that is named may be missing, the counts must be exactly those of the record, and no path from the top may pass through more than 128 folders, the top one counted.

- **Parents come first.** A frame never says which hash it belongs under: the receiver works the hash out from the frame's bytes. So nothing is kept before it is tied to the root.
- **A list that is named and then fails a check is not kept,** and shows that the tree itself is wrong. The receiver takes nothing more for that tree.
- **What a receiver holds already, it does not ask for:** a piece, a file or a whole folder, by its hash, from whatever tree it came.
- **A file is read whole or not at all.** If a piece of a stretch is missing, reading it is an error, and never a shorter file.
- **Bytes that a store holds under a hash they do not have are damaged.** A checker names them by that hash. A receiver may remove them and ask for them again, and so mend a tree that was harmed where it is kept.
- **Bytes that do have the hash they are kept under are never removed for what a list says of them.** A list that gives a held piece another length than it has is wrong, and is refused. Before a receiver removes a piece whose length does not fit, it hashes the piece's bytes to tell the two apart.

## Requests

A request starts with one byte that says what is asked.

| First byte | Then | Asks for |
| --- | --- | --- |
| `0x30` | `root (32)` | a tree's record |
| `0x31` | `folder hash (32)` | a folder's whole list |
| `0x32` | `folder hash (32) + start (8) + count (4)` | a run of a folder's entries |
| `0x33` | `folder hash (32) + name length (2) + name` | one name of a folder |
| `0x34` | `file hash (32)` | a file's whole list |
| `0x35` | `file hash (32) + start (8) + count (4)` | a run of a file's list |
| `0x36` | `file hash (32) + offset (8) + length (8)` | the run of a file's list that holds those bytes |
| `0x37` | `file hash (32) + start (8) + count (4)` | pieces of a file, by their numbers |
| `0x38` | `piece hash (32)` | one piece |
| `0x39` | `file hash (32)` | which pieces of a file the peer holds |

A request is 290 bytes at most.

## Answers

An answer is one frame. For pieces it is one frame a piece, in the file's order, and as many as the peer gives: it may give fewer than were asked for. A frame starts with one byte that says what it is.

| First byte | Then | Is |
| --- | --- | --- |
| `0x20` | the rest of the record | a record. The frame is the record itself. |
| `0x21` | the items | a file's whole list |
| `0x22` | the entries | a folder's whole list |
| `0x23` | a run | a run of a file's list |
| `0x24` | a run | a run of a folder's list |
| `0x25` | the bytes | a piece |
| `0x26` | `count (8) + bits` | which pieces a peer holds |
| `0x27` | `why (1)` | a refusal |

- **Which pieces a peer holds** is one bit a piece, the first piece in the highest bit of the first byte. Spare bits are zero. It is what the peer says about itself, and nothing proves it.
- **A refusal** says 1 when the peer does not hold what was asked for, and 2 when what was asked for lies outside the list or the file. Nothing more comes after it in that answer.
- **Every request has a longest answer,** worked out from what the asker already trusts: a file's list is no longer than the file's size allows, no list is longer than the record leaves room for, a piece is no longer than the tree's way of cutting allows, and a run is no longer than the items asked for. A longer frame is refused by its length, before its bytes are read.

## On a connection

- A request is sent as `length (4) + request`.
- An answer is sent as each of its frames as `length (4) + frame`, and then a length of zero.
- Requests are answered one at a time, in the order they come. An asker may send requests ahead of their answers. It sends no more than 2,048 bytes of them ahead, so that two ends never wait for each other.
- A request that is malformed, empty, or longer than 290 bytes ends the connection.

## A bundle

A bundle is a tree as one stream: the eight bytes `SCIONIC` and `0x01`, then the record, and then every list and every piece once, each after what names it. Each comes as `length (4) + frame`. A reader holds it to the checks above. A frame that nothing in the tree names is an error, and so is a stream that ends before the tree is whole.

## Limits

| What | Limit |
| --- | --- |
| A piece | 8,388,608 bytes |
| A list | 2^40 items |
| A name | 255 bytes |
| Extra data | 32 values, a name of 64 bytes, a value of 1,024 bytes: 34,946 bytes in all |
| An entry | 35,244 bytes |
| A record | 35,012 bytes |
| A request | 290 bytes |
| The nodes a run carries | 80 |
| Folders inside folders | 128, the top one counted |
| Sizes and counts | 64 bits. A sum that does not fit is an error. |

A tree keeps a folder once, however many places the folder has in it. So a tree of a few small lists can stand for more folders than a disk holds: a folder with two names for one folder, a hundred deep. Checking a tree, taking it in and sending it go through each folder once. Whatever goes through a tree place by place needs a limit of its own:

- writing a tree out counts what it would make before it makes anything, and makes a million files and folders at most unless told otherwise;
- a list of what changed between two trees holds a million changes at most unless told otherwise.

## Writing a tree out

A tree can hold a name that one system cannot. Writing out is stricter than holding:

- The place written to must not exist. Every file and folder is created as new, and a folder is written into only when this writing created it. A tree holds no links, so nothing is written outside that place.
- Before anything is written, every list and every piece must be there, and every name must be one the system can hold as it is. On Windows that leaves out names with `< > : " | ? * \` or a control character, names that end in a dot or a space, and the device names (`CON`, `NUL`, `COM1` and the like, with or without an ending).
- Where two names of one folder land on the same place, as `File` and `file` do on Windows and macOS, the second is refused. So is a name that the system keeps differently from how it was given.
- A name is never changed to make it fit. The error says which name it was.
- A folder that this writing created is held open while it is filled, and what goes into it is made from the folder itself. A link that takes its place meanwhile is not written through.

## Building a tree

- A file is read a part at a time and never held whole, and its hash does not depend on how it was read.
- The record's totals are counted by the checks that a receiver runs, so a tree that is built has passed them.
- A link is never followed. It is refused, or left out when the builder is told to. Anything that is neither a file nor a folder is refused.
- That holds too for a link that takes a file's or a folder's place while the folder is read. A builder holds each folder open while it reads it, and opens what is in it from the folder itself, so that no path is looked up twice. Where a system has no way to do that, the builder says so.
- An entry holds the bytes of the file that the folder lists under the entry's name, and of no other. Where a system changes a name when it looks a path up, as Windows drops a dot or a space from the end of one, a builder opens the file in a way that takes the name as it is.
- A name that is not UTF-8 is refused.
