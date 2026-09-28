#!/usr/bin/env python3
"""S3 Hi-Fi base plate for the modules: DAC | DC-DC | ESP32-S3 terminal board.
All modules lie along Y. Plate depth = DAC length + lid walls:
front (y = D): DAC RCA and headphone jack; rear (y = 0): DAC 12 V jack and the two devkit USB-C.
The lid is screwed through 4 holes at the left and right ends, away from the boards.
Run: python3 stand_gen.py  ->  platform.stl + platform_top.png
"""
import trimesh, numpy as np
from trimesh.creation import box, cylinder

PLATE_H = 3.0      # plate thickness
POST_H = 5.0       # standoff height above the plate
POST_D = 8.0       # standoff diameter
HOLE_D = 2.6       # for an M3 self-tapping screw (through)
GAP = 10.0         # gap between modules
W, D = 160.0, 88.0 # plate size: DAC 81.6 + 2 × (wall 2 + clearance 1.2)
FIX_D, FIX_IN = 3.2, 5.0   # lid screws: 4 × M3, 5 mm from the corners, 150 × 78 pitch

# modules: (name, width X, depth Y, holes relative to the module centre)
MODULES = [
    ("DAC ES9038Q2M", 49.4, 81.6, [(sx * 43 / 2, sy * 75 / 2) for sx in (-1, 1) for sy in (-1, 1)]),
    ("DC-DC", 21.0, 43.0, [(-17 / 2, -29 / 2), (17 / 2, 29 / 2)]),   # diagonal
    ("ESP32-S3", 48.0, 69.65, [(sx * 41 / 2, sy * 62.65 / 2) for sx in (-1, 1) for sy in (-1, 1)]),
]

total = sum(m[1] for m in MODULES) + GAP * (len(MODULES) - 1)
x = (W - total) / 2
my = (D - max(m[2] for m in MODULES)) / 2
layout = []
for name, mw, md, holes in MODULES:
    cx, cy = x + mw / 2, my + md / 2          # rear edges aligned at y = my
    layout.append((name, x, my, mw, md, [(cx + hx, cy + hy) for hx, hy in holes]))
    x += mw + GAP

plate = box((W, D, PLATE_H)); plate.apply_translation((W / 2, D / 2, PLATE_H / 2))
parts = [plate]
holes = []
for *_, pts in layout:
    for px, py in pts:
        post = cylinder(radius=POST_D / 2, height=POST_H + 0.01, sections=64)
        post.apply_translation((px, py, PLATE_H + POST_H / 2 - 0.005))
        parts.append(post)
        h = cylinder(radius=HOLE_D / 2, height=PLATE_H + POST_H + 2, sections=48)
        h.apply_translation((px, py, (PLATE_H + POST_H) / 2)); holes.append(h)
for fx in (FIX_IN, W - FIX_IN):
    for fy in (FIX_IN, D - FIX_IN):
        h = cylinder(radius=FIX_D / 2, height=PLATE_H + 2, sections=48)
        h.apply_translation((fx, fy, PLATE_H / 2)); holes.append(h)

solid = trimesh.boolean.union(parts, engine="manifold")
solid = trimesh.boolean.difference([solid] + holes, engine="manifold")
assert solid.is_watertight
solid.export("platform.stl")
print("STL:", solid.bounds.round(2).tolist(), "watertight", solid.is_watertight)

# ---- top view for checking ----
import matplotlib; matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import Rectangle, Circle
fig, ax = plt.subplots(figsize=(10, 6.6), dpi=110)
ax.add_patch(Rectangle((0, 0), W, D, fc="#e8e8e8", ec="k"))
for name, mx, my_, mw, md, pts in layout:
    ax.add_patch(Rectangle((mx, my_), mw, md, fc="none", ec="#1f5fbf", ls="--"))
    ax.text(mx + mw / 2, my_ + md + 2.5, f"{name}\n{mw}×{md}", ha="center", va="bottom", fontsize=8, color="#1f5fbf")
    for px, py in pts:
        ax.add_patch(Circle((px, py), POST_D / 2, fc="#999", ec="k")); ax.add_patch(Circle((px, py), HOLE_D / 2, fc="w", ec="k"))
for fx in (FIX_IN, W - FIX_IN):
    for fy in (FIX_IN, D - FIX_IN):
        ax.add_patch(Circle((fx, fy), FIX_D / 2, fc="w", ec="k"))
ax.text(W / 2, -4, "REAR: DAC 12 V jack, 2 × USB-C", ha="center", va="top", fontsize=9)
ax.text(W / 2, D + 11, "FRONT: RCA, 3.5 mm jack", ha="center", va="bottom", fontsize=9)
ax.set_xlim(-5, W + 5); ax.set_ylim(-12, D + 18); ax.set_aspect("equal"); ax.set_title(f"Plate {W:g}×{D:g}×{PLATE_H:g} mm, standoffs Ø{POST_D:g}×{POST_H:g}, holes Ø{HOLE_D}")
plt.savefig("platform_top.png", bbox_inches="tight")
for name, mx, my_, mw, md, pts in layout:
    print(name, "holes:", [(round(a, 2), round(b, 2)) for a, b in pts])
