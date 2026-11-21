"""Makes screenshots smaller: shrink.py SRC DST.

Every SRC/*.png is written to DST with at most 256 colours, which keeps a
screenshot of the UI sharp, at under 400 KB. pngquant or oxipng are used
when installed; otherwise Pillow does it. DST's old PNG and WebM files are
removed first, so a shot that is no longer taken does not linger.
"""
import glob
import os
import shutil
import subprocess
import sys

from PIL import Image

LIMIT = 400 * 1024


def shrink(src, dst):
    if shutil.which("pngquant"):
        subprocess.run(["pngquant", "--force", "--skip-if-larger", "--quality=70-95", "--output", dst, src], check=False)
    if not os.path.exists(dst):
        im = Image.open(src).convert("RGB")
        colours = 256
        while True:
            im.quantize(colors=colours, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE).save(dst, optimize=True)
            if os.path.getsize(dst) <= LIMIT or colours <= 32:
                break
            colours //= 2
    if shutil.which("oxipng"):
        subprocess.run(["oxipng", "-q", "-o", "3", "--strip", "safe", dst], check=False)


def main():
    src, dst = sys.argv[1], sys.argv[2]
    os.makedirs(dst, exist_ok=True)
    for old in glob.glob(os.path.join(dst, "*.png")) + glob.glob(os.path.join(dst, "*.webm")):
        os.remove(old)
    total = 0
    for f in sorted(glob.glob(os.path.join(src, "*.png"))):
        out = os.path.join(dst, os.path.basename(f))
        shrink(f, out)
        size = os.path.getsize(out)
        total += size
        flag = "  (over 400 KB)" if size > LIMIT else ""
        print(f"{os.path.basename(f):40} {os.path.getsize(f) // 1024:5} KB -> {size // 1024:4} KB{flag}")
    print(f"{'total':40} {'':8}    {total // 1024:4} KB")


if __name__ == "__main__":
    main()
