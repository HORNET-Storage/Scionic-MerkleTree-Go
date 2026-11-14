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

// The tree that the two tree pictures draw. The folder on top holds a folder,
// a file and a folder, and each of those two folders holds two files. Every
// file has several chunks, and is drawn with its own Classic Merkle tree. The
// names are in the order of their bytes, as a folder's entries are.
const TOP = ['model', 'notes.pdf', 'samples'];
const LEFT = ['model.bin', 'vocab.json'];
const RIGHT = ['cat.wav', 'dog.wav'];
// How many chunks each file has. Every file has two or more, so that its tree
// can be seen.
const CHUNKS = { 'model.bin': 8, 'vocab.json': 3, 'notes.pdf': 2, 'cat.wav': 4, 'dog.wav': 3 };
for (const names of [TOP, LEFT, RIGHT]) {
  if (names.some((name, i) => i > 0 && Buffer.compare(Buffer.from(names[i - 1]), Buffer.from(name)) >= 0)) throw new Error(`a folder's names are out of order: ${names}`);
}
for (const name of [TOP[1], ...LEFT, ...RIGHT]) {
  if (!(CHUNKS[name] >= 2)) throw new Error(`${name} needs two chunks or more, so that its tree can be seen`);
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
  const forms = ['IPFS Merkle DAGs', 'Scionic Merkle Trees', 'Classic Merkle Trees'];
  const ours = 1;
  const W = 1344, left = 56, featureW = 508, formW = 240, headY = 124, headH = 76, rowH = 122;
  const bottom = headY + headH + rows.length * rowH;
  const p = new Picture(
    'merklized-forms',
    W,
    bottom + 56,
    'Comparison of Many Merklized Forms',
    rows.map((r) => `${r.name}: ${r.lines.join(' ')} ${forms.map((f, j) => `${f}: ${r.marks[j] ? 'yes' : 'no'}`).join(', ')}.`).join(' '),
  );
  const formX = (j) => left + featureW + j * formW;

  p.text(left, 82, 'Comparison of Many Merklized Forms', { size: 32, weight: 700 });

  // The column of the Scionic Merkle Tree stands out from the other two.
  p.rect(formX(ours) + 8, headY - 6, formW - 16, bottom - headY + 18, { rx: 16, fill: C.raised, stroke: C.sky, sw: 1.5 });

  forms.forEach((form, j) => {
    p.rect(formX(j) + 12, headY, formW - 24, headH, { id: `head${j}` });
    p.text(formX(j) + formW / 2, headY + 46, form, { size: 19, weight: 600, anchor: 'middle', in: `head${j}` });
  });

  rows.forEach((row, i) => {
    const top = headY + headH + i * rowH;
    p.line(left, top, left + featureW + forms.length * formW, top, { stroke: C.hair, sw: 1 });
    p.rect(left - 6, top + 6, featureW - 12, rowH - 12, { id: `feature${i}` });
    p.text(left, top + 44, row.name, { size: 22, weight: 600, in: `feature${i}` });
    row.lines.forEach((line, k) => p.text(left, top + 72 + k * 23, line, { size: 16.5, fill: C.ink2, in: `feature${i}` }));
    row.marks.forEach((yes, j) => {
      const cx = formX(j) + formW / 2 - 19, cy = top + rowH / 2;
      p.rect(formX(j) + 22, top + 16, formW - 44, rowH - 32, { rx: 12, fill: mix(yes ? C.green : C.red, j === ours ? C.raised : C.canvas, 0.12) });
      p.rect(formX(j) + 12, top + 6, formW - 24, rowH - 12, { id: `mark${i}.${j}` });
      p.circle(cx, cy, 16, { fill: yes ? C.green : C.red });
      p.path(yes ? `M${n(cx - 7)} ${n(cy + 0.5)} l4.8 4.7 l9.4 -10` : `M${n(cx - 5.5)} ${n(cy - 5.5)} l11 11 M${n(cx + 5.5)} ${n(cy - 5.5)} l-11 11`, { stroke: C.dark, sw: 3 });
      p.text(cx + 26, cy + 6, yes ? 'Yes' : 'No', { size: 17, weight: 600, in: `mark${i}.${j}` });
    });
  });
  return p;
}

// tree draws the Scionic Merkle Tree, and with mode 'branch' the same tree with
// the branch that proves one chunk of the first folder's first file.
function tree(mode) {
  const branch = mode === 'branch';
  const W = 1344, edge = 48;
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

  // Where everything stands. A folder and a file are drawn the same way: a box
  // with the Classic Merkle tree over its list, and one chip for each item of
  // the list under the tree's leaves. A folder's chips are names, and a file's
  // are the numbers of its chunks.
  const pad = 16, gap = 12, chipH = 26, step = 30, numberW = 44, least = 176;
  const tile = { w: 50, h: 40, drop: 34 };
  const levels = (count) => Math.max(1, Math.ceil(Math.log2(count)));
  // Every box has one height, that of the deepest tree, so that the boxes of a
  // row line up at the top and at the bottom. A tree of fewer levels is drawn
  // as tall as the deepest: its top stands where every top stands, and its
  // leaves where every leaf stands.
  const deepest = Math.max(...[TOP.length, LEFT.length, RIGHT.length, ...[TOP[1], ...LEFT, ...RIGHT].map((name) => CHUNKS[name])].map(levels));
  const boxH = 68 + deepest * step + 14 + chipH + 14;
  const openW = (count, chipW) => Math.max(least, count * (chipW + gap) - gap + 2 * pad);
  const open = (id, kind, about, x, y, labels, chipW) => {
    const w = openW(labels.length, chipW);
    const first = x + (w - labels.length * (chipW + gap) + gap) / 2;
    const leafY = y + 68 + deepest * step, chipY = leafY + 14;
    const chips = labels.map((s, i) => ({ id: `${id}.c${i}`, s, x: first + i * (chipW + gap), y: chipY, w: chipW, h: chipH, cx: first + i * (chipW + gap) + chipW / 2 }));
    return { id, kind, about, x, y, w, h: boxH, cx: x + w / 2, leafY, rise: (deepest * step) / levels(labels.length), chips };
  };
  const nameW = (names) => 2 * Math.ceil((Math.max(...names.map((s) => s.length)) * 8.25 + 22) / 2);
  const folder = (id, names, cx, bottom) => open(id, 'Folder', `${names.length} entries`, cx - openW(names.length, nameW(names)) / 2, bottom - boxH, names, nameW(names));
  const fileW = (name) => openW(CHUNKS[name], numberW);
  // The chunks of a file hang under it, and those of a row of files make one
  // row.
  const file = (id, name, x, bottom) => {
    const count = CHUNKS[name];
    const box = open(id, 'File', `${count} chunks`, x, bottom - boxH, Array.from({ length: count }, (_, i) => String(i)), numberW);
    box.chunks = box.chips.map((c, i) => ({ id: `${id}.k${i}`, x: c.cx - tile.w / 2, y: bottom + tile.drop, w: tile.w, h: tile.h, cx: c.cx }));
    return box;
  };

  // The bottom row fills the picture from edge to edge: the two files of the
  // first folder, the room that is left, and the two files of the second
  // folder. Each folder stands over its own files. The file between the
  // folders stands over that room, and its chunks hang in it.
  const leftW = fileW(LEFT[0]) + gap + fileW(LEFT[1]), rightW = fileW(RIGHT[0]) + gap + fileW(RIGHT[1]);
  const room = W - 2 * edge - leftW - rightW;
  const hanging = CHUNKS[TOP[1]] * (numberW + gap) - (numberW + gap - tile.w);
  if (room < hanging + 2 * gap) throw new Error(`the files of the bottom row leave ${room} units for the chunks of ${TOP[1]}, which take ${hanging + 2 * gap}`);
  const x0 = edge, rightX = x0 + leftW + room;
  const y1 = 286;
  const bottom2 = y1 + boxH + 88 + boxH;
  const bottom3 = bottom2 + 96 + boxH;
  const leftFiles = [file('left.f0', LEFT[0], x0, bottom3), file('left.f1', LEFT[1], x0 + fileW(LEFT[0]) + gap, bottom3)];
  const rightFiles = [file('right.f0', RIGHT[0], rightX, bottom3), file('right.f1', RIGHT[1], rightX + fileW(RIGHT[0]) + gap, bottom3)];
  const left = folder('left', LEFT, x0 + leftW / 2, bottom2);
  const right = folder('right', RIGHT, rightX + rightW / 2, bottom2);
  const middle = file('top.f1', TOP[1], x0 + leftW + room / 2 - fileW(TOP[1]) / 2, bottom2);
  // The root, the record and the folder on top stand in the middle of the
  // picture, with the key on both sides of them.
  const cx = W / 2;
  const top = folder('top', TOP, cx, y1 + boxH);
  const root = { id: 'root', x: cx - 220, y: 138, w: 440, h: 38 };
  const record = { id: 'record', x: cx - 235, y: 194, w: 470, h: 66 };
  const folders = [top, left, right], files = [middle, ...leftFiles, ...rightFiles];
  const chunks = files.flatMap((f) => f.chunks);
  const others = files.length - 1;

  const p = new Picture(
    branch ? 'scionic-merkle-branch' : 'scionic-merkle-tree',
    W,
    bottom3 + tile.drop + tile.h + 52,
    branch ? 'Scionic Merkle Branch' : 'Scionic Merkle Tree',
    branch
      ? `The same tree, with the branch that proves chunk ${PROVEN} of ${proven} against the root. The record is sent. The folder on top, the folder ${TOP[0]} and the file ${proven} each show one item of their list, with a hash and a size for every part of the list that is not shown: ${sent.size} in all. The receiver works out every hash on the way up, until the top matches the root it trusts. The other ${CHUNKS[proven] - 1} chunks of ${proven}, the ${others} other files and the rest of every list are never sent.`
      : `The root is the hash of the record. The record names the folder on top, which lists three entries by name: the folder ${TOP[0]}, the file ${TOP[1]} and the folder ${TOP[2]}. The folder ${TOP[0]} lists the files ${LEFT[0]} and ${LEFT[1]}, and the folder ${TOP[2]} lists the files ${RIGHT[0]} and ${RIGHT[1]}. Every file lists its chunks by number: ${files.map((f, i) => `${[TOP[1], ...LEFT, ...RIGHT][i]} has ${f.chips.length}`).join(', ')}. Inside every folder and every file, a Classic Merkle tree covers its list. A chunk is its bytes and nothing else.`,
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
  for (const b of [...folders, ...files]) {
    const look = boxLook(b.id);
    p.rect(b.x, b.y, b.w, b.h, { id: b.id, rx: 12, fill: look.fill, stroke: look.stroke, sw: look.sw });
    p.text(b.x + pad, b.y + 30, [{ s: b.kind, weight: 600, size: 18 }, { s: b.about, fill: look.ink2, dx: 10 }], { size: 14.5, fill: look.ink, in: b.id });
  }
  for (const b of chunks) {
    const s = state(b.id);
    const look = s === 'plain' ? { fill: mix(C.sky, C.box, 0.16), stroke: C.sky, sw: 1.5, ink: C.ink } : s === 'shown' ? { fill: mix(C.green, C.box, 0.3), stroke: C.green, sw: 2, ink: C.ink } : { fill: faintBox, stroke: fade(C.sky, C.canvas), sw: 1.5, ink: faintInk };
    p.rect(b.x, b.y, b.w, b.h, { id: b.id, rx: 8, fill: look.fill, stroke: look.stroke, sw: look.sw });
    p.text(b.cx, b.y + 24.5, 'Chunk', { size: 13, weight: 600, fill: look.ink, anchor: 'middle', in: b.id });
  }

  // The links: an item of a list holds the hash of the thing under it.
  p.line(cx, root.y + root.h, cx, record.y, linkLook('root>record'));
  p.line(cx, record.y + record.h, cx, top.y, linkLook('record>top'));
  const link = (parent, i, child) => {
    const c = parent.chips[i], id = `${c.id}>${child.id}`;
    const from = parent.y + parent.h, to = child.y, bend = (to - from) * 0.55;
    const tx = child.x + child.w / 2;
    p.path(`M${n(c.cx)} ${n(c.y + c.h)} V${n(from)} C${n(c.cx)} ${n(from + bend)} ${n(tx)} ${n(to - bend)} ${n(tx)} ${n(to)}`, { ...linkLook(id), from: c.id, to: child.id });
  };
  [left, middle, right].forEach((child, i) => link(top, i, child));
  leftFiles.forEach((child, i) => link(left, i, child));
  rightFiles.forEach((child, i) => link(right, i, child));
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
  const boxKey = (fill, stroke, sw) => (x, y) => p.rect(x - 15, y - 11, 30, 22, { rx: 6, fill, stroke, sw });
  const tileKey = (fill, stroke, sw) => (x, y) => p.rect(x - 13, y - 10, 26, 20, { rx: 8, fill, stroke, sw });
  const linkKey = (stroke, sw) => (x, y) => p.path(`M${n(x - 13)} ${n(y - 11)} C${n(x - 13)} ${n(y + 4)} ${n(x + 13)} ${n(y - 4)} ${n(x + 13)} ${n(y + 11)}`, { stroke, sw });
  if (!branch) {
    key(0, 0, boxKey(C.box, C.sky, 1.5), 'Folder and file', ['Each is named by its hash. The same', 'content has the same hash anywhere.']);
    key(0, 1, (x, y) => p.hex(x, y, 9, { fill: C.amber }), 'Classic Merkle tree', ['Inside every folder and file, over its', 'list. Its top is the Classic Merkle root.']);
    key(0, 2, tileKey(mix(C.sky, C.box, 0.16), C.sky, 1.5), 'Chunk', ['Its bytes and nothing else, so the same', 'chunk in two files is stored once.']);
    key(1, 0, chipKey('name', 40, C.raised, C.hair, 1), "A folder's entry", ['kind · size · hash · name · extra data.', "Sorted by name. A file's hash holds no name."]);
    key(1, 1, chipKey(String(PROVEN), 28, C.raised, C.hair, 1), "A file's chunk number", ["A chunk's place in the file's list,", 'which holds its hash and its length.']);
    key(1, 2, linkKey(C.sky, 1.5), 'Link by hash', ['An item of a list holds the hash of', 'the folder, file or chunk under it.']);
  } else {
    key(0, 0, chipKey(String(PROVEN), 28, mix(C.green, C.box, 0.25), C.green, 1.75), 'Shown', ['The record, one item of each list', 'on the way, and the chunk itself.']);
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
