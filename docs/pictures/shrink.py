"""shrink.py gives a picture a palette of 256 colours, which makes its file
about a third of the size without a change that an eye can see.

    python docs/pictures/shrink.py <picture.png> <smaller.png>

The colours that fill the most pixels are kept exactly as they are, and every
flat colour of a drawing is among them. The other places in the palette go,
one at a time, to the colour that is furthest from every colour kept so far.
So no shade along an edge or a letter ends far from where it was. How far two
colours are apart is the largest difference in any one of their channels.

The line it prints says how far the furthest colour moved. If that is more
than 8 of 255, the picture needs more than 256 colours: the program writes
nothing, leaves what is at <smaller.png> as it was, and ends with an error.

It needs Pillow.
"""

import os
import sys

from PIL import Image, ImageChops

KEPT_AS_THEY_ARE = 96
PALETTE = 256
FURTHEST = 8


def apart(a, b):
    # The measure the picture is held to at the end, so that the palette is
    # chosen by it too.
    return max(abs(x - y) for x, y in zip(a, b))


def main(source, target):
    picture = Image.open(source).convert("RGBA")
    counted = sorted(picture.getcolors(maxcolors=1 << 24), reverse=True)
    colours = [colour for _, colour in counted]
    kept = colours[:KEPT_AS_THEY_ARE]
    rest = colours[KEPT_AS_THEY_ARE:]

    # The furthest colour from the palette joins it, until the palette is full.
    gap = [min(apart(colour, k) for k in kept) for colour in rest]
    while len(kept) < PALETTE and rest:
        furthest = max(range(len(rest)), key=gap.__getitem__)
        if gap[furthest] == 0:
            break
        kept.append(rest[furthest])
        gap = [min(g, apart(colour, kept[-1])) for g, colour in zip(gap, rest)]

    place = {colour: i for i, colour in enumerate(kept)}
    for colour in rest:
        if colour not in place:
            place[colour] = min(range(len(kept)), key=lambda i: apart(colour, kept[i]))
    table = {bytes(colour): i for colour, i in place.items()}
    pixels = picture.tobytes()
    small = Image.frombytes("P", picture.size, bytes(table[pixels[i : i + 4]] for i in range(0, len(pixels), 4)))
    small.putpalette([value for colour in kept for value in colour[:3]])

    # The picture is written beside its place, read back, and held to the
    # picture it came from. It takes its place only if it passes.
    draft = target + ".draft"
    small.save(draft, format="PNG", optimize=True, transparency=bytes(colour[3] for colour in kept))
    with Image.open(draft) as written:
        moved = ImageChops.difference(picture, written.convert("RGBA"))
    furthest = max(band.getextrema()[1] for band in moved.split())
    if furthest > FURTHEST:
        os.remove(draft)
        sys.exit(
            f"{os.path.basename(target)}: a colour would move by {furthest} of 255, and {FURTHEST} is the most. "
            f"This picture needs more than {PALETTE} colours. Nothing was written."
        )
    os.replace(draft, target)
    exact = sum(count for count, _ in counted[:KEPT_AS_THEY_ARE]) / (picture.size[0] * picture.size[1])
    print(
        f"{os.path.basename(target)}: {picture.size[0]} x {picture.size[1]}, {os.path.getsize(source)} -> {os.path.getsize(target)} bytes; "
        f"{exact:.1%} of the pixels are exactly as they were, and no colour moved by more than {furthest} of 255"
    )


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: python shrink.py <picture.png> <smaller.png>")
    main(sys.argv[1], sys.argv[2])
