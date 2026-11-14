// pictures.mjs draws the three pictures of the README: the comparison of
// Merklized forms, the Scionic Merkle Tree and the Scionic Merkle Branch.
// They share one theme and one tree, so a change here changes all of them.
//
//   node docs/pictures/pictures.mjs <folder> [<path of Chrome or Edge>]
//
// writes <name>.svg and <name>.html for each picture into the folder. The .html
// is the same drawing with a check of its layout, which runs when a browser
// opens it. With the path of a browser, the browser runs that check and then
// takes each picture as <name>.png. README.md, beside this file, has the rest.

import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

// The name of the protocol that asks for a file's chunks by their numbers.
const SYNC = 'Scionic Sync';

// The theme. The navy surfaces are those of the HORNET Storage relay panel.
// Sky and amber are the two colours of the HORNET Storage mark, each at the
// step that suits a dark surface. Green is yes and the branch, red is no.
// README.md says how the colours were checked for colour-blind readers.
const C = {
  canvas: '#121430',
  box: '#1D203E',
  raised: '#25284B',
  hair: '#343A66',
  ink: '#F4F6FF',
  ink2: '#B9C1DE',
  dark: '#0B1026',
  sky: '#3ea1c3',
  amber: '#ba8c02',
  green: '#06a763',
  red: '#f7502a',
};
const SANS = "'Segoe UI', system-ui, -apple-system, 'Helvetica Neue', Arial, sans-serif";
const MONO = "Consolas, 'Cascadia Mono', 'SF Mono', Menlo, monospace";

// The tree that the two tree pictures draw. The folder on top holds two
// folders and two files, and each of those folders holds three files. The
// names are in the order of their bytes, as a folder's entries are, and the
// pictures draw a folder's entries in that order. The first two names of TOP
// are the two folders. So that the two folders stand side by side, with the
// files to the right of them, the files of the folder on top have names that
// sort after both folders.
const TOP = ['model', 'samples', 'test.csv', 'train.csv'];
const LEFT = ['model.bin', 'tags.json', 'vocab.txt'];
const RIGHT = ['bird.wav', 'cat.wav', 'dog.wav'];
// How many chunks each file has.
const CHUNKS = { 'test.csv': 3, 'train.csv': 3, 'model.bin': 6, 'tags.json': 3, 'vocab.txt': 3, 'bird.wav': 3, 'cat.wav': 3, 'dog.wav': 3 };
// The files of the folder on top, which stand beside its two folders, and
// every file.
const BESIDE = TOP.slice(2);
const FILES = [...BESIDE, ...LEFT, ...RIGHT];
// The tree over a list of two items is one fork, which does not look like a
// tree. So every folder here has three entries or more, and every file three
// chunks or more.
for (const names of [TOP, LEFT, RIGHT]) {
  if (names.some((name, i) => i > 0 && Buffer.compare(Buffer.from(names[i - 1]), Buffer.from(name)) >= 0)) throw new Error(`a folder's names are out of order: ${names}`);
  if (names.length < 3) throw new Error(`a folder needs three entries or more, and this one has ${names.length}: ${names}`);
}
for (const name of FILES) {
  if (!(CHUNKS[name] >= 3)) throw new Error(`${name} needs three chunks or more`);
}
// The chunk that the branch picture proves: this place in the list of the
// first file of the first folder.
const PROVEN = 2;
if (!(PROVEN >= 0 && PROVEN < CHUNKS[LEFT[0]])) throw new Error(`${LEFT[0]} has no chunk ${PROVEN} to prove`);

const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const n = (v) => String(Math.round(v * 100) / 100);
const attrs = (o) =>
  Object.entries(o)
    .filter(([, v]) => v !== undefined && v !== null && v !== false)
    .map(([k, v]) => ` ${k}="${esc(typeof v === 'number' ? n(v) : v)}"`)
    .join('');

// mix lays colour a over colour b: all of a at 1, none of it at 0.
function mix(a, b, share) {
  const parts = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
  const [x, y] = [parts(a), parts(b)];
  return '#' + x.map((v, i) => Math.round(v * share + y[i] * (1 - share)).toString(16).padStart(2, '0')).join('');
}

// split says where node(o, m) of a list's tree is forked: the largest power of
// two that is less than m, as spec/FORMAT.md has it under "The tree of a list".
function split(m) {
  let k = 1;
  while (k * 2 < m) k *= 2;
  return k;
}

class Picture {
  constructor(name, w, h, title, desc) {
    Object.assign(this, { name, w, h, title, desc });
    this.parts = [];
  }

  // rect draws a box. One with an id is held to the layout check: it stays on
  // the canvas, inside its parent, and off every other box.
  rect(x, y, w, h, o = {}) {
    this.parts.push(
      `<rect${attrs({ x, y, width: w, height: h, rx: o.rx, fill: o.fill ?? 'none', stroke: o.stroke, 'stroke-width': o.stroke ? (o.sw ?? 1.5) : undefined, 'data-box': o.id, 'data-parent': o.parent })}/>`,
    );
  }

  // text draws one line. With `in`, the check holds it inside that box.
  text(x, y, runs, o = {}) {
    const list = typeof runs === 'string' ? [{ s: runs }] : runs;
    const body = list
      .map((r) => `<tspan${attrs({ dx: r.dx, fill: r.fill, 'font-weight': r.weight, 'font-size': r.size, 'font-family': r.mono ? MONO : undefined })}>${esc(r.s)}</tspan>`)
      .join('');
    this.parts.push(
      `<text${attrs({ x, y, 'font-family': o.mono ? MONO : SANS, 'font-size': o.size ?? 16, 'font-weight': o.weight, fill: o.fill ?? C.ink, 'text-anchor': o.anchor, 'data-in': o.in })}>${body}</text>`,
    );
  }

  line(x1, y1, x2, y2, o = {}) {
    this.parts.push(`<line${attrs({ x1, y1, x2, y2, stroke: o.stroke, 'stroke-width': o.sw ?? 1.5, 'stroke-linecap': 'round', 'data-edge': o.edge ? '1' : undefined })}/>`);
  }

  // path draws a line that is not straight. With `from` and `to`, it is a link
  // between two boxes, and the check keeps it off every other box and text.
  path(d, o = {}) {
    this.parts.push(
      `<path${attrs({ d, fill: o.fill ?? 'none', stroke: o.stroke, 'stroke-width': o.stroke ? (o.sw ?? 1.5) : undefined, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'data-from': o.from, 'data-to': o.to })}/>`,
    );
  }

  // shape draws a box whose outline is not a plain rectangle, as a folder's and
  // a file's are. The layout check holds it as it holds a rect with an id, by
  // the rectangle that all of it fits in. `inside` is a point that the box
  // covers, for whoever holds the rendered picture to the drawing.
  shape(d, o = {}) {
    this.parts.push(
      `<path${attrs({ d, fill: o.fill ?? 'none', stroke: o.stroke, 'stroke-width': o.stroke ? (o.sw ?? 1.5) : undefined, 'stroke-linejoin': 'round', 'data-box': o.id, 'data-parent': o.parent, 'data-inside': o.inside })}/>`,
    );
  }

  circle(cx, cy, r, o = {}) {
    this.parts.push(`<circle${attrs({ cx, cy, r, fill: o.fill ?? 'none', stroke: o.stroke, 'stroke-width': o.stroke ? (o.sw ?? 1.5) : undefined })}/>`);
  }

  // hex draws a six-sided cell with a corner on top: a node of a Classic
  // Merkle tree.
  hex(cx, cy, r, o = {}) {
    const points = [0, 1, 2, 3, 4, 5].map((k) => `${n(cx + r * Math.sin((k * Math.PI) / 3))},${n(cy - r * Math.cos((k * Math.PI) / 3))}`).join(' ');
    this.parts.push(
      `<polygon${attrs({ points, fill: o.fill ?? 'none', stroke: o.stroke, 'stroke-width': o.stroke ? (o.sw ?? 2) : undefined, 'stroke-linejoin': 'round', 'data-node': o.in ? '1' : undefined, 'data-in': o.in })}/>`,
    );
  }

  svg() {
    return [
      `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${this.w} ${this.h}" width="${this.w}" height="${this.h}" role="img" aria-labelledby="picture-title picture-desc">`,
      `<title id="picture-title">${esc(this.title)}</title>`,
      `<desc id="picture-desc">${esc(this.desc)}</desc>`,
      `<rect${attrs({ x: 0.5, y: 0.5, width: this.w - 1, height: this.h - 1, rx: 28, fill: C.canvas, stroke: C.hair, 'stroke-width': 1 })}/>`,
      ...this.parts,
      '</svg>',
      '',
    ].join('\n');
  }
}

// table draws the comparison of Merklized forms.
function table() {
  const rows = [
    { name: 'Numbered Chunks', lines: ["A file's chunks are numbered in order,", `facilitating ${SYNC}.`], marks: [false, true, false] },
    { name: 'Negentropy Symbiosis', lines: ['Sync Merkle roots and file hashes via Set Reconciliation,', `then each file's chunks via ${SYNC}.`], marks: [false, true, false] },
    { name: 'Folder Storage Support', lines: ['Convert folders containing many files', 'into a chunked, Merklized tree.'], marks: [true, true, false] },
    { name: 'Chunked Parent Leaves', lines: ['Avoid bloated parent leaves that grow', 'when the number of chunks rises.'], marks: [false, true, true] },
  ];
  // The three forms. The word that tells a form from the other two is written
  // large, over the rest of its name. Each has a small picture of itself, in
  // the colours of the two tree pictures: sky for the things that link by
  // hash, and amber for a Classic Merkle tree.
  const forms = [
    { word: 'IPFS', rest: 'Merkle DAGs', sign: 'links' },
    { word: 'Scionic', rest: 'Merkle Trees', sign: 'both' },
    { word: 'Classic', rest: 'Merkle Trees', sign: 'tree' },
  ];
  const said = (form) => `${form.word} ${form.rest}`;
  const ours = 1;
  // The table fills the picture. Its title stands in the corner that a table
  // leaves empty: left of the names of the forms, and over the names of the
  // rows.
  const left = 48, featureW = 428, formW = 196, headY = 56, headH = 136, rowH = 100;
  const rowsY = headY + headH + 20, bottom = rowsY + rows.length * rowH;
  const right = left + featureW + forms.length * formW, inset = 6;
  const p = new Picture(
    'merklized-forms',
    right - inset + left,
    bottom + 58,
    'Comparison of Many Merklized Forms',
    rows.map((r) => `${r.name}: ${r.lines.join(' ')} ${forms.map((f, j) => `${said(f)}: ${r.marks[j] ? 'yes' : 'no'}`).join(', ')}.`).join(' '),
  );
  const formX = (j) => left + featureW + j * formW;

  p.text(left, headY + 40, 'Comparison of Many', { size: 32, weight: 700 });
  p.text(left, headY + 84, 'Merklized Forms', { size: 32, weight: 700 });
  p.text(left, headY + 113, 'Scionic Merkle Trees: The Best of Both Worlds', { size: 17, fill: C.ink2 });

  // The small pictures. (cx, y) is the middle of a picture's top edge, and
  // each is 50 high.
  const amberTree = (nodes, links) => {
    for (const [a, b] of links) p.line(nodes[a][0], nodes[a][1], nodes[b][0], nodes[b][1], { stroke: C.amber, sw: 2 });
    for (const [x, y] of nodes) p.hex(x, y, 6.3, { fill: C.amber });
  };
  const signs = {
    // Boxes that link to boxes, one of them from two sides: a Merkle DAG.
    links: (cx, y) => {
      const at = [[cx, y + 5.5], [cx - 13.5, y + 25], [cx + 13.5, y + 25], [cx - 27, y + 44.5], [cx, y + 44.5], [cx + 27, y + 44.5]];
      for (const [a, b] of [[0, 1], [0, 2], [1, 3], [1, 4], [2, 4], [2, 5]]) p.line(at[a][0], at[a][1], at[b][0], at[b][1], { stroke: C.sky, sw: 2 });
      for (const [x, top] of at) p.rect(x - 7.5, top - 5.5, 15, 11, { rx: 3.5, fill: C.sky });
    },
    // A Classic Merkle tree.
    tree: (cx, y) =>
      amberTree(
        [[cx, y + 6.5], [cx - 15, y + 25], [cx + 15, y + 25], [cx - 22.5, y + 43.5], [cx - 7.5, y + 43.5], [cx + 7.5, y + 43.5], [cx + 22.5, y + 43.5]],
        [[0, 1], [0, 2], [1, 3], [1, 4], [2, 5], [2, 6]],
      ),
    // A box that links by hash, with a Classic Merkle tree inside it.
    both: (cx, y) => {
      p.rect(cx - 31, y, 62, 50, { rx: 10, fill: C.box, stroke: C.sky, sw: 2.25 });
      amberTree([[cx, y + 15], [cx - 15, y + 35], [cx + 15, y + 35]], [[0, 1], [0, 2]]);
    },
  };

  // The column of the Scionic Merkle Tree stands out from the other two: one
  // frame holds its name and its four marks.
  p.rect(formX(ours) + 2, headY - 8, formW - 4, bottom - headY + 18, { rx: 18, fill: C.raised, stroke: C.sky, sw: 1.75 });
  // A line under the names of the forms, half way to the first row, and one
  // between two rows.
  rows.forEach((_, i) => p.line(left, rowsY + i * rowH - (i ? 0 : 10), right - inset, rowsY + i * rowH - (i ? 0 : 10), { stroke: C.hair, sw: 1 }));

  forms.forEach((form, j) => {
    const mine = j === ours, cx = formX(j) + formW / 2, pad = mine ? 10 : inset;
    p.rect(formX(j) + pad, headY, formW - 2 * pad, headH, { id: `head${j}`, rx: mine ? 12 : 14, fill: mine ? mix(C.sky, C.raised, 0.22) : C.box, stroke: mine ? undefined : C.hair, sw: 1 });
    signs[form.sign](cx, headY + 12);
    p.text(cx, headY + 94, form.word, { size: 28, weight: 700, anchor: 'middle', in: `head${j}` });
    p.text(cx, headY + 123, form.rest, { size: 17, weight: 600, anchor: 'middle', in: `head${j}` });
  });

  rows.forEach((row, i) => {
    const top = rowsY + i * rowH, mid = top + rowH / 2;
    p.rect(left - 6, top + 6, featureW - 12, rowH - 12, { id: `feature${i}` });
    p.text(left, top + 40, row.name, { size: 21.5, weight: 600, in: `feature${i}` });
    row.lines.forEach((line, k) => p.text(left, top + 64 + k * 22, line, { size: 16, fill: C.ink2, in: `feature${i}` }));
    // A mark is a badge: a tick or a cross, and the word beside it, so that
    // neither the colour nor the shape says it alone.
    row.marks.forEach((yes, j) => {
      const cx = formX(j) + formW / 2, colour = yes ? C.green : C.red, under = j === ours ? C.raised : C.canvas;
      p.rect(cx - 56, mid - 23, 112, 46, { rx: 23, fill: mix(colour, under, 0.16), stroke: mix(colour, under, 0.5), sw: 1.25 });
      p.rect(formX(j) + 8, top + 8, formW - 16, rowH - 16, { id: `mark${i}.${j}` });
      p.circle(cx - 19, mid, 13, { fill: colour });
      p.path(yes ? `M${n(cx - 24.7)} ${n(mid + 0.4)} l3.9 3.8 l7.6 -8.1` : `M${n(cx - 23.5)} ${n(mid - 4.5)} l9 9 M${n(cx - 14.5)} ${n(mid - 4.5)} l-9 9`, { stroke: C.dark, sw: 2.6 });
      p.text(cx + 3, mid + 6.5, yes ? 'Yes' : 'No', { size: 18, weight: 600, in: `mark${i}.${j}` });
    });
  });
  return p;
}

// tree draws the Scionic Merkle Tree, and with mode 'branch' the same tree with
// the branch that proves one chunk of the first folder's first file.
function tree(mode) {
  const branch = mode === 'branch';
  // The tree pictures are a little wider than the table, so that the bottom
  // row holds every file with its chunks at a size that can be read.
  const W = 1424, edge = 48;
  const proven = LEFT[0];

  // What the branch shows, what the receiver works out, and what is sent
  // beside it. shape gives the last two as spec/FORMAT.md has them under
  // "Showing a part": a node none of whose items is shown is sent, and a node
  // over an item that is shown is worked out.
  const shown = new Set(), worked = new Set(), sent = new Set(), taken = new Set();
  if (branch) {
    const shape = (id, o, m, at) => {
      if (at < o || at >= o + m) return void sent.add(`${id}.n${o}.${m}`);
      worked.add(`${id}.n${o}.${m}`);
      if (m === 1) return;
      shape(id, o, split(m), at);
      shape(id, o + split(m), m - split(m), at);
    };
    shape('top', 0, TOP.length, 0);
    shape('left', 0, LEFT.length, 0);
    shape('left.f0', 0, CHUNKS[proven], PROVEN);
    for (const id of ['record', 'top.c0', 'left.c0', `left.f0.c${PROVEN}`, `left.f0.k${PROVEN}`]) shown.add(id);
    for (const id of ['root', 'top', 'left', 'left.f0']) worked.add(id);
    for (const id of ['root>record', 'record>top', 'top.c0>left', 'left.c0>left.f0', `left.f0.c${PROVEN}>left.f0.k${PROVEN}`]) taken.add(id);
  }
  const state = (id) => (!branch ? 'plain' : shown.has(id) ? 'shown' : worked.has(id) ? 'worked' : sent.has(id) ? 'sent' : 'rest');
  const fade = (colour, over) => mix(colour, over, 0.38);
  const faintInk = mix(C.ink2, C.canvas, 0.55);
  const faintBox = mix(C.box, C.canvas, 0.5);
  const boxLook = (id) =>
    ({
      plain: { fill: C.box, stroke: C.sky, sw: 1.5, ink: C.ink, ink2: C.ink2 },
      shown: { fill: mix(C.green, C.box, 0.22), stroke: C.green, sw: 2, ink: C.ink, ink2: C.ink2 },
      worked: { fill: C.box, stroke: C.green, sw: 2, ink: C.ink, ink2: C.ink2 },
      rest: { fill: faintBox, stroke: fade(C.sky, C.canvas), sw: 1.5, ink: faintInk, ink2: faintInk },
    })[state(id)];
  const linkLook = (id) => (!branch ? { stroke: C.sky, sw: 1.5 } : taken.has(id) ? { stroke: C.green, sw: 2.5 } : { stroke: fade(C.sky, C.canvas), sw: 1.5 });

  // Where everything stands. A folder and a file hold the same things: the
  // Classic Merkle tree over its list, and one chip for each item of the list
  // under the tree's leaves. A folder's chips are names, and a file's are the
  // numbers of its chunks. Their outlines tell them apart ("The boxes").
  const pad = 16, chipH = 26, step = 30, least = 160;
  // Two boxes that belong together stand `gap` apart, and two sets of them
  // stand `group` apart: the folders and the files of the second row, and the
  // two folders' files in the bottom row. The picture is as wide as the bottom
  // row is with these.
  const gap = 12, group = 26;
  const nameChip = { w: 2 * Math.ceil((Math.max(...[...TOP, ...LEFT, ...RIGHT].map((s) => s.length)) * 8.25 + 22) / 2), gap: 12 };
  const numberChip = { w: 42, gap: 12 };
  // A chunk is narrower than the step from one number to the next, by the room
  // that is left between two chunks.
  const tile = { w: 48, h: 38, drop: 30 };
  const levels = (count) => Math.max(1, Math.ceil(Math.log2(count)));
  // Every box has one height, that of the deepest tree, so that the boxes of a
  // row line up at the top and at the bottom. A tree of fewer levels is drawn
  // as tall as the deepest: its top stands where every top stands, and its
  // leaves where every leaf stands.
  const deepest = Math.max(...[TOP.length, LEFT.length, RIGHT.length, ...FILES.map((f) => CHUNKS[f])].map(levels));
  const boxH = 68 + deepest * step + 14 + chipH + 14;
  const boxW = (count, chip, min = least) => Math.max(min, count * (chip.w + chip.gap) - chip.gap + 2 * pad);
  const open = (id, kind, about, x, bottom, labels, chip, min) => {
    const w = boxW(labels.length, chip, min), y = bottom - boxH;
    const first = x + (w - labels.length * (chip.w + chip.gap) + chip.gap) / 2;
    const leafY = y + 68 + deepest * step, chipY = leafY + 14;
    const chips = labels.map((s, i) => ({ id: `${id}.c${i}`, s, x: first + i * (chip.w + chip.gap), y: chipY, w: chip.w, h: chipH, cx: first + i * (chip.w + chip.gap) + chip.w / 2 }));
    return { id, kind, about, x, y, w, h: boxH, cx: x + w / 2, leafY, rise: (deepest * step) / levels(labels.length), chips };
  };
  const folderW = (names, min) => boxW(names.length, nameChip, min);
  const folder = (id, names, x, bottom, min) => open(id, 'Folder', `${names.length} entries`, x, bottom, names, nameChip, min);
  const fileW = (f) => boxW(CHUNKS[f], numberChip);
  // The chunks of a file hang under it, and those of a row of files make one
  // row.
  const file = (id, f, x, bottom) => {
    const box = open(id, 'File', `${CHUNKS[f]} chunks`, x, bottom, Array.from({ length: CHUNKS[f] }, (_, i) => String(i)), numberChip);
    box.chunks = box.chips.map((c, i) => ({ id: `${id}.k${i}`, x: c.cx - tile.w / 2, y: bottom + tile.drop, w: tile.w, h: tile.h, cx: c.cx }));
    return box;
  };
  const rowW = (files) => files.reduce((sum, f) => sum + fileW(f), 0) + (files.length - 1) * gap;
  const row = (id, files, x, bottom) => {
    let at = x;
    return files.map((f, i) => {
      const box = file(`${id}.f${i}`, f, at, bottom);
      at += box.w + gap;
      return box;
    });
  };

  // Three rows of boxes, each in the middle of the picture.
  // - The root, the record and the folder on top stand with the key on both
  //   sides of them, and the folder on top is as wide as the record.
  // - The second row is the entries of the folder on top, in the order of
  //   their names: its two folders side by side, and then its files.
  // - The bottom row is the files of the first folder, and then those of the
  //   second.
  // The second and the third row are far enough apart for the links from the
  // second folder to pass under the chunks that hang from the second row's
  // files.
  const cx = W / 2;
  const root = { id: 'root', x: cx - 220, y: 138, w: 440, h: 38 };
  const record = { id: 'record', x: cx - 235, y: 194, w: 470, h: 66 };
  const bottom1 = 296 + boxH, bottom2 = bottom1 + 88 + boxH, bottom3 = bottom2 + 184 + boxH;
  const top = folder('top', TOP, cx - folderW(TOP, record.w) / 2, bottom1, record.w);
  const second = folderW(LEFT) + gap + folderW(RIGHT) + group + rowW(BESIDE);
  if (second > W - 2 * edge) throw new Error(`the second row takes ${second} units, and the picture has ${W - 2 * edge}`);
  const left = folder('left', LEFT, cx - second / 2, bottom2);
  const right = folder('right', RIGHT, left.x + left.w + gap, bottom2);
  const beside = row('top', BESIDE, right.x + right.w + group, bottom2);
  const third = rowW(LEFT) + group + rowW(RIGHT);
  if (third > W - 2 * edge) throw new Error(`the files of the bottom row take ${third} units, and the picture has ${W - 2 * edge}`);
  const leftFiles = row('left', LEFT, cx - third / 2, bottom3);
  const rightFiles = row('right', RIGHT, leftFiles.at(-1).x + leftFiles.at(-1).w + group, bottom3);
  const folders = [top, left, right], files = [...beside, ...leftFiles, ...rightFiles];
  const chunks = files.flatMap((f) => f.chunks);
  const others = files.length - 1;
  const said = (names) => (names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`);

  const p = new Picture(
    branch ? 'scionic-merkle-branch' : 'scionic-merkle-tree',
    W,
    bottom3 + tile.drop + tile.h + 52,
    branch ? 'Scionic Merkle Branch' : 'Scionic Merkle Tree',
    branch
      ? `The same tree, with the branch that proves chunk ${PROVEN} of ${proven} against the root. The record is sent. The folder on top, the folder ${TOP[0]} and the file ${proven} each show one item of their list, with a hash and a size for every part of the list that is not shown: ${sent.size} in all. The receiver works out every hash on the way up, until the top matches the root it trusts. The other ${CHUNKS[proven] - 1} chunks of ${proven}, the ${others} other files and the rest of every list are never sent.`
      : `The root is the hash of the record. The record names the folder on top, which lists ${TOP.length} entries by name: the folders ${TOP[0]} and ${TOP[1]}, and the ${BESIDE.length === 1 ? 'file' : 'files'} ${said(BESIDE)}. The folder ${TOP[0]} lists the files ${said(LEFT)}, and the folder ${TOP[1]} lists the files ${said(RIGHT)}. Every file lists its chunks by number: ${FILES.map((f) => `${f} has ${CHUNKS[f]}`).join(', ')}. Inside every folder and every file, a Classic Merkle tree covers its list. A chunk is its bytes and nothing else.`,
  );

  p.text(edge, 82, branch ? 'Scionic Merkle Branch' : 'Scionic Merkle Tree', { size: 32, weight: 700 });
  p.text(
    edge,
    112,
    branch
      ? `One chunk of ${proven}, proven against the root: one item of each list on the way, and a few hashes beside it.`
      : 'Folders and files link by hash, like a Merkle DAG. Inside each, a Classic Merkle tree covers its list.',
    { size: 17, fill: C.ink2 },
  );

  // The boxes.
  p.rect(root.x, root.y, root.w, root.h, { id: 'root', rx: root.h / 2, fill: branch ? C.green : C.sky });
  p.text(cx, root.y + 25, [{ s: 'Root', weight: 700 }, { s: 'bafkrei…', mono: true, dx: 12 }, { s: 'the hash of the record', dx: 12 }], { size: 16.5, fill: C.dark, anchor: 'middle', in: 'root' });
  {
    const look = boxLook('record');
    p.rect(record.x, record.y, record.w, record.h, { id: 'record', rx: 12, fill: look.fill, stroke: look.stroke, sw: look.sw });
    p.text(cx, record.y + 27, [{ s: 'Record', weight: 600, size: 18 }, { s: 'what is on top, and how much the tree holds', fill: look.ink2, dx: 10 }], { size: 14.5, fill: look.ink, anchor: 'middle', in: 'record' });
    p.text(cx, record.y + 50, 'kind · size · hash · chunks · chunk bytes · list bytes · extra data', { size: 14.5, fill: look.ink, anchor: 'middle', in: 'record' });
  }
  // A folder and a file are told apart by their outlines. A folder's box has a
  // tab over its top left corner, as a folder has, and a file's box has its top
  // right corner folded down, as a sheet has. Each also has a small picture of
  // itself beside its word. A box's place and size are those of its body: a
  // folder's tab stands over them.
  const radius = 12, tab = { h: 20, share: 0.32, slope: 24 }, ear = 22;
  const corner = (dx, dy) => `a${radius} ${radius} 0 0 1 ${dx * radius} ${dy * radius}`;
  const tabEnd = (b) => b.x + Math.round(b.w * tab.share);
  const tabTop = (b) => `V${n(b.y - tab.h + 10)} a10 10 0 0 1 10 -10 H${n(tabEnd(b) - tab.slope)} C${n(tabEnd(b) - tab.slope + 10)} ${n(b.y - tab.h)} ${n(tabEnd(b) - 10)} ${n(b.y)} ${n(tabEnd(b))} ${n(b.y)}`;
  const folderOutline = (b) => `M${n(b.x)} ${n(b.y + b.h - radius)} ${tabTop(b)} H${n(b.x + b.w - radius)} ${corner(1, 1)} V${n(b.y + b.h - radius)} ${corner(-1, 1)} H${n(b.x + radius)} ${corner(-1, -1)} Z`;
  const tabOutline = (b) => `M${n(b.x)} ${n(b.y)} ${tabTop(b)} Z`;
  const fileOutline = (b) => `M${n(b.x)} ${n(b.y + radius)} ${corner(1, -1)} H${n(b.x + b.w - ear)} L${n(b.x + b.w)} ${n(b.y + ear)} V${n(b.y + b.h - radius)} ${corner(-1, 1)} H${n(b.x + radius)} ${corner(-1, -1)} Z`;
  const folderIcon = (x, y, stroke, fill) => p.path(`M${n(x)} ${n(y + 3.5)} a2 2 0 0 1 2 -2 h5 l2.5 2.5 h8.5 a2 2 0 0 1 2 2 v8.5 a2 2 0 0 1 -2 2 h-16 a2 2 0 0 1 -2 -2 Z`, { stroke, fill, sw: 1.5 });
  const fileIcon = (x, y, stroke, fill) => {
    p.path(`M${n(x + 2)} ${n(y)} h6.5 l5.5 5.5 v10.5 a2 2 0 0 1 -2 2 h-10 a2 2 0 0 1 -2 -2 v-14 a2 2 0 0 1 2 -2 Z`, { stroke, fill, sw: 1.5 });
    p.path(`M${n(x + 8.5)} ${n(y)} v5.5 h5.5`, { stroke, sw: 1.5 });
  };
  for (const b of [...folders, ...files]) {
    const look = boxLook(b.id), isFolder = b.kind === 'Folder';
    // The tab, the folded corner and the small pictures are tinted in the tree
    // picture only. In the branch picture a fill says what is shown or sent,
    // so there they are outlines.
    const plain = state(b.id) === 'plain';
    const tint = plain ? mix(C.sky, C.box, 0.2) : look.fill;
    p.shape(isFolder ? folderOutline(b) : fileOutline(b), { id: b.id, fill: look.fill, stroke: look.stroke, sw: look.sw, inside: `${n(b.x + 4)} ${n(b.y + b.h / 2)}` });
    if (isFolder) {
      p.path(tabOutline(b), { fill: plain ? mix(C.sky, C.box, 0.38) : look.fill, stroke: look.stroke, sw: look.sw });
      folderIcon(b.x + pad, b.y + 15, look.stroke, tint);
    } else {
      p.path(`M${n(b.x + b.w - ear)} ${n(b.y)} V${n(b.y + ear)} H${n(b.x + b.w)} Z`, { fill: tint, stroke: look.stroke, sw: look.sw });
      fileIcon(b.x + pad, b.y + 14, look.stroke, tint);
    }
    p.text(b.x + pad + (isFolder ? 28 : 22), b.y + 30, [{ s: b.kind, weight: 600, size: 18 }, { s: b.about, fill: look.ink2, dx: 10 }], { size: 14.5, fill: look.ink, in: b.id });
  }
  for (const b of chunks) {
    const s = state(b.id);
    const look = s === 'plain' ? { fill: mix(C.sky, C.box, 0.16), stroke: C.sky, sw: 1.5, ink: C.ink } : s === 'shown' ? { fill: mix(C.green, C.box, 0.3), stroke: C.green, sw: 2, ink: C.ink } : { fill: faintBox, stroke: fade(C.sky, C.canvas), sw: 1.5, ink: faintInk };
    p.rect(b.x, b.y, b.w, b.h, { id: b.id, rx: 8, fill: look.fill, stroke: look.stroke, sw: look.sw });
    p.text(b.cx, b.y + b.h / 2 + 4.5, 'Chunk', { size: 12.25, weight: 600, fill: look.ink, anchor: 'middle', in: b.id });
  }

  // The links: an item of a list holds the hash of the thing under it.
  p.line(cx, root.y + root.h, cx, record.y, linkLook('root>record'));
  p.line(cx, record.y + record.h, cx, top.y, linkLook('record>top'));
  // A link leaves its chip straight down and reaches its box straight down, in
  // the middle of the box. fall and land say how far it keeps each of those two
  // directions, as a share of the way: a link with a long fall goes sideways
  // late, and so lower. A chip that stands almost over the middle of its box
  // has a straight link.
  const link = (parent, i, child, fall = 0.55, land = 0.55) => {
    const c = parent.chips[i], id = `${c.id}>${child.id}`;
    const from = parent.y + parent.h, to = child.y;
    const tx = Math.abs(c.cx - child.cx) <= 8 ? c.cx : child.cx;
    p.path(`M${n(c.cx)} ${n(c.y + c.h)} V${n(from)} C${n(c.cx)} ${n(from + (to - from) * fall)} ${n(tx)} ${n(to - (to - from) * land)} ${n(tx)} ${n(to)}`, { ...linkLook(id), from: c.id, to: child.id });
  };
  [left, right, ...beside].forEach((child, i) => link(top, i, child));
  // The links to the bottom row fall a long way before they go sideways, so
  // that they pass under the chunks that hang from the second row's files.
  leftFiles.forEach((child, i) => link(left, i, child, 0.85, 0.4));
  rightFiles.forEach((child, i) => link(right, i, child, 0.85, 0.4));
  for (const f of files) f.chunks.forEach((child, i) => link(f, i, child));

  // Inside each box: the Classic Merkle tree over its list, and the list.
  for (const b of [...folders, ...files]) {
    const look = boxLook(b.id);
    const at = new Map();
    const place = (o, m) => {
      let node;
      if (m === 1) node = { o, m, x: b.chips[o].cx, y: b.leafY, height: 0, under: [] };
      else {
        const l = place(o, split(m)), r = place(o + split(m), m - split(m));
        const height = Math.max(l.height, r.height) + 1;
        node = { o, m, x: (l.x + r.x) / 2, y: b.leafY - height * b.rise, height, under: [l, r] };
      }
      at.set(`${b.id}.n${o}.${m}`, node);
      return node;
    };
    const crown = place(0, b.chips.length);
    for (const [id, node] of at) {
      for (const kid of node.under) {
        const kidState = state(`${b.id}.n${kid.o}.${kid.m}`);
        const on = state(id) === 'worked' && (kidState === 'worked' || kidState === 'sent');
        p.line(node.x, node.y, kid.x, kid.y, !branch ? { stroke: C.amber, sw: 1.75, edge: true } : on ? { stroke: C.green, sw: 2.25, edge: true } : { stroke: fade(C.amber, look.fill), sw: 1.5, edge: true });
      }
    }
    for (const c of b.chips) {
      const s = state(c.id);
      p.line(c.cx, b.leafY, c.cx, c.y, !branch ? { stroke: C.amber, sw: 1.75 } : s === 'shown' ? { stroke: C.green, sw: 2.25 } : { stroke: fade(C.amber, look.fill), sw: 1.5 });
    }
    for (const [id, node] of at) {
      const s = state(id);
      p.hex(node.x, node.y, 9.5, { fill: look.fill });
      if (s === 'plain') p.hex(node.x, node.y, 7, { fill: C.amber, in: b.id });
      else if (s === 'worked') p.hex(node.x, node.y, 7, { fill: look.fill, stroke: C.green, sw: 2.25, in: b.id });
      else if (s === 'sent') p.hex(node.x, node.y, 8.5, { fill: C.green, in: b.id });
      else p.hex(node.x, node.y, 7, { fill: fade(C.amber, look.fill), in: b.id });
    }
    p.text(crown.x, b.y + 52, 'Classic Merkle root', { size: 13.5, fill: look.ink2, anchor: 'middle', in: b.id });
    for (const c of b.chips) {
      const s = state(c.id);
      const chip = s === 'plain' ? { fill: C.raised, stroke: C.hair, sw: 1, ink: C.ink } : s === 'shown' ? { fill: mix(C.green, C.box, 0.25), stroke: C.green, sw: 1.75, ink: C.ink } : { fill: mix(C.raised, look.fill, 0.45), stroke: undefined, ink: faintInk };
      p.rect(c.x, c.y, c.w, c.h, { id: c.id, parent: b.id, rx: 6, fill: chip.fill, stroke: chip.stroke, sw: chip.sw });
      p.text(c.cx, c.y + 18.5, c.s, { size: 15, mono: true, fill: chip.ink, anchor: 'middle', in: c.id });
    }
  }

  // The key stands on both sides of the root.
  const flank = [edge, record.x + record.w + 28];
  const flankW = [record.x - 28 - edge, W - edge - flank[1]];
  const key = (side, row, swatch, title, lines) => {
    const x = flank[side], y = 204 + row * 86, id = `key${side}${row}`;
    p.rect(x - 8, y - 22, flankW[side] + 8, 74, { id });
    swatch(x + 20, y - 5, id);
    p.text(x + 50, y, title, { size: 15.5, weight: 600, in: id });
    lines.forEach((line, k) => p.text(x + 50, y + 22 + k * 21, line, { size: 14.5, fill: C.ink2, in: id }));
  };
  const chipKey = (label, w, fill, stroke, sw) => (x, y, id) => {
    p.rect(x - w / 2, y - 12, w, 24, { rx: 6, fill, stroke, sw });
    p.text(x, y + 4.5, label, { size: 13, mono: true, anchor: 'middle', in: id });
  };
  // What is shown, as it stands at the foot of the picture: the number of the
  // chunk in its file's list, the link, and the chunk itself. A number alone
  // would not say what it is the number of.
  const shownKey = (x, y, id) => {
    p.line(x, y + 12, x, y + 26, { stroke: C.green, sw: 2.5 });
    p.rect(x - 17, y - 12, 34, 24, { rx: 6, fill: mix(C.green, C.box, 0.25), stroke: C.green, sw: 1.75 });
    p.text(x, y + 4.5, String(PROVEN), { size: 13, mono: true, anchor: 'middle', in: id });
    p.rect(x - 21, y + 26, 42, 26, { rx: 7, fill: mix(C.green, C.box, 0.3), stroke: C.green, sw: 2 });
    p.text(x, y + 43, 'Chunk', { size: 11, weight: 600, anchor: 'middle', in: id });
  };
  const boxKey = (fill, stroke, sw) => (x, y) => p.rect(x - 15, y - 11, 30, 22, { rx: 6, fill, stroke, sw });
  const tileKey = (fill, stroke, sw) => (x, y) => p.rect(x - 13, y - 10, 26, 20, { rx: 8, fill, stroke, sw });
  const linkKey = (stroke, sw) => (x, y) => p.path(`M${n(x - 13)} ${n(y - 11)} C${n(x - 13)} ${n(y + 4)} ${n(x + 13)} ${n(y - 4)} ${n(x + 13)} ${n(y + 11)}`, { stroke, sw });
  if (!branch) {
    const both = (x, y) => {
      folderIcon(x - 20, y - 9, C.sky, mix(C.sky, C.box, 0.2));
      fileIcon(x + 6, y - 9.5, C.sky, mix(C.sky, C.box, 0.2));
    };
    key(0, 0, both, 'Folder and file', ['Each is named by its hash. The same', 'content has the same hash anywhere.']);
    key(0, 1, (x, y) => p.hex(x, y, 9, { fill: C.amber }), 'Classic Merkle tree', ['Inside every folder and file, over its', 'list. Its top is the Classic Merkle root.']);
    key(0, 2, tileKey(mix(C.sky, C.box, 0.16), C.sky, 1.5), 'Chunk', ['Its bytes and nothing else, so the same', 'chunk in two files is stored once.']);
    key(1, 0, chipKey('name', 40, C.raised, C.hair, 1), "A folder's entry", ['kind · size · hash · name · extra data.', "Sorted by name. A file's hash holds no name."]);
    key(1, 1, chipKey(String(PROVEN), 28, C.raised, C.hair, 1), "A file's chunk number", ["A chunk's place in the file's list,", 'which holds its hash and its length.']);
    key(1, 2, linkKey(C.sky, 1.5), 'Link by hash', ['An item of a list holds the hash of', 'the folder, file or chunk under it.']);
  } else {
    key(0, 0, shownKey, 'Shown', ['The record, one item of each list', 'on the way, and the chunk itself.']);
    key(0, 1, (x, y) => p.hex(x, y, 9.5, { fill: C.green }), 'Sent beside it', ['A hash and a size, and in a folder a', 'name, for each part that is not shown.']);
    key(0, 2, (x, y) => p.hex(x, y, 8, { fill: C.box, stroke: C.green, sw: 2.25 }), 'Worked out by the receiver', ['Every hash on the way up, until', 'the top matches the root it trusts.']);
    key(1, 0, linkKey(C.green, 2.5), 'The way to the chunk', ['The hash worked out at each step must', 'match the one held just above it.']);
    key(1, 1, boxKey(mix(C.green, C.box, 0.22), C.green, 2), 'Sent, in all', [`The record, 3 items, ${sent.size} hashes with`, 'their sizes, and the chunk.']);
    key(1, 2, boxKey(faintBox, fade(C.sky, C.canvas), 1.5), 'Never sent', [`The ${CHUNKS[proven] - 1} other chunks of ${proven},`, `the ${others} other files, the rest of each list.`]);
  }
  return p;
}

// check runs in the browser, inside the page of a picture. It measures the
// drawing as the browser laid it out, and lists what is wrong with it.
function check() {
  const svg = document.querySelector('svg');
  const W = svg.viewBox.baseVal.width, H = svg.viewBox.baseVal.height;
  const wrong = [];
  const boxOf = (el) => {
    const b = el.getBBox();
    return { x: b.x, y: b.y, w: b.width, h: b.height };
  };
  const inside = (a, b, px, py) => a.x >= b.x + px && a.y >= b.y + py && a.x + a.w <= b.x + b.w - px && a.y + a.h <= b.y + b.h - py;
  const meet = (a, b, m) => a.x < b.x + b.w + m && b.x < a.x + a.w + m && a.y < b.y + b.h + m && b.y < a.y + a.h + m;
  const holds = (b, q, m) => q.x > b.x - m && q.x < b.x + b.w + m && q.y > b.y - m && q.y < b.y + b.h + m;
  const canvas = { x: 0, y: 0, w: W, h: H };

  const boxes = new Map();
  for (const el of svg.querySelectorAll('[data-box]')) boxes.set(el.dataset.box, Object.assign(boxOf(el), { parent: el.dataset.parent || null }));
  const above = (id) => {
    const out = new Set();
    for (let b = boxes.get(id); b && b.parent; b = boxes.get(b.parent)) out.add(b.parent);
    return out;
  };
  const ids = [...boxes.keys()];
  for (const id of ids) {
    const b = boxes.get(id);
    if (!inside(b, canvas, 24, 24)) wrong.push(`box ${id} is too near the edge`);
    if (b.parent && !boxes.has(b.parent)) wrong.push(`box ${id} names a parent that is not there`);
    else if (b.parent && !inside(b, boxes.get(b.parent), 4, 4)) wrong.push(`box ${id} leaves its parent ${b.parent}`);
  }
  for (let i = 0; i < ids.length; i++) {
    for (let j = i + 1; j < ids.length; j++) {
      if (above(ids[i]).has(ids[j]) || above(ids[j]).has(ids[i])) continue;
      if (meet(boxes.get(ids[i]), boxes.get(ids[j]), 4)) wrong.push(`boxes ${ids[i]} and ${ids[j]} touch`);
    }
  }

  const texts = [...svg.querySelectorAll('text')].map((el) => ({ s: el.textContent, b: boxOf(el), in: el.dataset.in || null }));
  for (const t of texts) {
    if (t.b.w < 2) wrong.push(`text has no width: "${t.s}"`);
    if (t.in && !boxes.has(t.in)) wrong.push(`text names a box that is not there: "${t.s}"`);
    else if (t.in && !inside(t.b, boxes.get(t.in), 5, 1)) wrong.push(`text leaves its box ${t.in}: "${t.s}" (${Math.round(t.b.w)} wide in ${Math.round(boxes.get(t.in).w)})`);
    else if (!t.in && !inside(t.b, canvas, 40, 30)) wrong.push(`text is too near the edge: "${t.s}"`);
    const own = t.in ? new Set([t.in, ...above(t.in)]) : new Set();
    for (const id of ids) if (!own.has(id) && boxes.get(id).parent !== null && meet(t.b, boxes.get(id), 1)) wrong.push(`text "${t.s}" is on box ${id}`);
    for (const id of ids) if (!own.has(id) && boxes.get(id).parent === null && !t.in && meet(t.b, boxes.get(id), 1)) wrong.push(`text "${t.s}" is on box ${id}`);
  }
  for (let i = 0; i < texts.length; i++) for (let j = i + 1; j < texts.length; j++) if (meet(texts[i].b, texts[j].b, 0)) wrong.push(`texts on each other: "${texts[i].s}" and "${texts[j].s}"`);

  for (const el of svg.querySelectorAll('[data-node]')) {
    const b = boxOf(el);
    if (!inside(b, boxes.get(el.dataset.in), 3, 3)) wrong.push(`a node leaves its box ${el.dataset.in}`);
    for (const t of texts) if (meet(b, t.b, 1)) wrong.push(`a node of ${el.dataset.in} is on the text "${t.s}"`);
  }
  const along = (el, each) => {
    const length = el.getTotalLength();
    for (let d = 0; d <= length; d += 2) each(el.getPointAtLength(d));
  };
  for (const el of svg.querySelectorAll('[data-edge]')) {
    const hit = new Set();
    along(el, (q) => {
      for (const t of texts) if (holds(t.b, q, 1.5)) hit.add(t.s);
    });
    for (const s of hit) wrong.push(`a line of a Classic Merkle tree crosses the text "${s}"`);
  }
  for (const el of svg.querySelectorAll('[data-from]')) {
    const own = new Set([el.dataset.from, el.dataset.to, ...above(el.dataset.from), ...above(el.dataset.to)]);
    const hit = new Set();
    along(el, (q) => {
      for (const id of ids) if (!own.has(id) && holds(boxes.get(id), q, 5)) hit.add(`box ${id}`);
      for (const t of texts) if (!own.has(t.in) && holds(t.b, q, 2)) hit.add(`the text "${t.s}"`);
    });
    for (const s of hit) wrong.push(`the link ${el.dataset.from} > ${el.dataset.to} crosses ${s}`);
  }

  // The fonts the picture is drawn with must be the ones it names first.
  const pen = document.createElement('canvas').getContext('2d');
  const width = (font) => {
    pen.font = font;
    return pen.measureText('Scionic Merkle 0123456789 mmmmlll').width;
  };
  const fonts = {
    'Segoe UI': width("16px 'Segoe UI', monospace") !== width('16px monospace'),
    Consolas: width('16px Consolas, serif') !== width('16px serif'),
  };
  document.getElementById('report').textContent = JSON.stringify({ wrong, boxes: ids.length, texts: texts.length, links: svg.querySelectorAll('[data-from]').length, nodes: svg.querySelectorAll('[data-node]').length, fonts });
}

const page = (p) =>
  `<!doctype html>\n<html lang="en">\n<meta charset="utf-8">\n<title>${esc(p.title)}</title>\n<style>html, body { margin: 0; background: transparent; } svg { display: block; }</style>\n${p.svg()}<pre id="report" hidden></pre>\n<script>(${check.toString()})();</script>\n</html>\n`;

const [folder, browser] = process.argv.slice(2);
if (!folder) {
  console.error('usage: node pictures.mjs <folder> [<path of Chrome or Edge>]');
  process.exit(2);
}
mkdirSync(folder, { recursive: true });
const profile = browser ? mkdtempSync(join(tmpdir(), 'scionic-pictures-')) : null;
let failed = false;
for (const p of [table(), tree('tree'), tree('branch')]) {
  writeFileSync(join(folder, `${p.name}.svg`), p.svg());
  writeFileSync(join(folder, `${p.name}.html`), page(p));
  if (!browser) {
    console.log(`${p.name}: ${p.w} x ${p.h}`);
    continue;
  }
  // The browser lays the picture out and runs the check. If nothing is wrong,
  // it takes the picture at two pixels to a unit, with see-through corners.
  const url = pathToFileURL(resolve(folder, `${p.name}.html`)).href;
  const run = (...more) =>
    spawnSync(browser, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--user-data-dir=${profile}`, ...more, url], { encoding: 'utf8', maxBuffer: 1 << 26, timeout: 120000 });
  const found = /<pre id="report"[^>]*>([^<]+)<\/pre>/.exec(run('--dump-dom').stdout ?? '');
  if (!found) {
    console.error(`${p.name}: the browser did not run the check`);
    failed = true;
    continue;
  }
  const report = JSON.parse(found[1].replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&'));
  const missing = Object.keys(report.fonts).filter((font) => !report.fonts[font]);
  for (const wrong of report.wrong) console.error(`${p.name}: ${wrong}`);
  for (const font of missing) console.error(`${p.name}: this machine has no ${font}, which the pictures are drawn with`);
  if (report.wrong.length || missing.length) {
    failed = true;
    continue;
  }
  const png = resolve(folder, `${p.name}.png`);
  rmSync(png, { force: true });
  run('--hide-scrollbars', '--force-device-scale-factor=2', `--window-size=${p.w},${p.h}`, '--default-background-color=00000000', `--screenshot=${png}`);
  if (!existsSync(png)) {
    console.error(`${p.name}: the browser took no picture`);
    failed = true;
    continue;
  }
  console.log(`${p.name}: ${p.w} x ${p.h}, checked: ${report.boxes} boxes, ${report.texts} texts, ${report.links} links and ${report.nodes} nodes are where they belong`);
}
if (profile) rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
if (failed) process.exit(1);
