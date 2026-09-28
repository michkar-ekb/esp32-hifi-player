#!/usr/bin/env python3
"""S3 Hi-Fi lid for the 160 × 88 base plate (stand_gen.py).
Sits on the plate and is held by 4 M3 screws from below through the plate holes (150 × 78 pitch).
Coordinates: x left to right (top view, rear at the bottom), y = 0 rear wall, y = D front wall,
z = 0 bottom of the lid (= top of the plate). Modules: DAC x 10.8–60.2, terminal board 101.2–149.2.
Run: python3 lid_gen.py  ->  lid.stl + lid_views.png
"""
import numpy as np, trimesh
from trimesh.creation import box, cylinder, icosphere

W, D = 160.0, 88.0
H = 28.0          # lid height above the plate
WALL, TOP = 2.0, 2.0
R = 3.0           # edge radius
STANDOFF = 5.0    # boards sit on 5 mm standoffs
BOSS_D, PILOT_D, PILOT_DEPTH = 8.0, 2.6, 14.0
FIX = [(5, 5), (155, 5), (5, 83), (155, 83)]

DAC_X0, DAC_X1 = 10.8, 60.2
TERM_CX = 125.2               # centre of the terminal board and the devkit
# --- connectors (measured on the real boards) ---
RCA = [(DAC_X1 - 12, STANDOFF + 8), (DAC_X1 - 27, STANDOFF + 8)]   # front; viewer's left = DAC x_max
RCA_SLOT_W = 9.4                                                   # RCA sleeve ~8.5 + clearance
JACK = (DAC_X0 + 11, STANDOFF + 4); JACK_D = 8.0                  # front
PWR = (DAC_X0 + 22, STANDOFF + 7); PWR_W, PWR_TOP = 10.8, STANDOFF + 1.6 + 12.5   # rear; square jack body ~10 × 12, sticks out past the board
# --- devkit USB-C: two 8.94 × 3.26 receptacles, symmetric, 3 mm apart ---
USB_Z = 5 + 1.6 + 8.5 + 2.5 + 1.6 + 3.26 / 2   # standoff + terminal board + sockets + header spacer + devkit + half receptacle ≈ 20.8
USB_C = [TERM_CX - (8.94 / 2 + 1.5), TERM_CX + (8.94 / 2 + 1.5)]
USB_PLUG_W, USB_H = 12.5, 8.5                  # plug overmold ~12 × 6.5 + height tolerance
USB = [TERM_CX]; USB_W = USB_C[1] - USB_C[0] + USB_PLUG_W   # one window for both connectors
# hand-cut terminal boards can be a bit longer -> recess inside the rear wall, open at the bottom
TERM_NOTCH_W, TERM_NOTCH_DEPTH, TERM_NOTCH_H = 54.0, 1.2, STANDOFF + 1.6 + 1.4

def at(m, x, y, z): m.apply_translation((x, y, z)); return m
def bx(x0, x1, y0, y1, z0, z1): return at(box((x1 - x0, y1 - y0, z1 - z0)), (x0 + x1) / 2, (y0 + y1) / 2, (z0 + z1) / 2)
def cyl_y(x, z, r, y0, y1):   # cylinder along Y
    c = cylinder(radius=r, height=y1 - y0, sections=48); c.apply_transform(trimesh.transformations.rotation_matrix(np.pi / 2, (1, 0, 0)))
    return at(c, x, (y0 + y1) / 2, z)
def slot(x, w, ztop, y0, y1):  # slot from the bottom up to ztop with a rounded top
    r = w / 2
    return [bx(x - r, x + r, y0, y1, -1, ztop - r), cyl_y(x, ztop - r, r, y0, y1)]

# outer shell: vertical and top edges rounded by R, flat bottom
pts = []
for cx in (R, W - R):
    for cy in (R, D - R):
        pts.append(at(icosphere(subdivisions=3, radius=R), cx, cy, H - R).vertices)
        pts.append(at(cylinder(radius=R, height=0.01, sections=64), cx, cy, 0.005).vertices)
outer = trimesh.convex.convex_hull(np.vstack(pts))
inner = bx(WALL, W - WALL, WALL, D - WALL, -1, H - TOP)
shell = trimesh.boolean.difference([outer, inner], engine="manifold")

bosses = [at(cylinder(radius=BOSS_D / 2, height=H - TOP + 0.5, sections=48), x, y, (H - TOP + 0.5) / 2) for x, y in FIX]
body = trimesh.boolean.union([shell] + bosses, engine="manifold")

cuts = [at(cylinder(radius=PILOT_D / 2, height=PILOT_DEPTH + 1, sections=32), x, y, (PILOT_DEPTH - 1) / 2) for x, y in FIX]
yf0, yf1 = D - WALL - 0.5, D + 1          # front wall
yr0, yr1 = -1, WALL + 0.5                 # rear wall
for x, z in RCA: cuts += slot(x, RCA_SLOT_W, z + RCA_SLOT_W / 2, yf0, yf1)
cuts.append(cyl_y(JACK[0], JACK[1], JACK_D / 2, yf0, yf1))
cuts.append(bx(PWR[0] - PWR_W / 2, PWR[0] + PWR_W / 2, yr0, yr1, -1, PWR_TOP))
cuts.append(bx(TERM_CX - TERM_NOTCH_W / 2, TERM_CX + TERM_NOTCH_W / 2, WALL - TERM_NOTCH_DEPTH, WALL + 0.5, -1, TERM_NOTCH_H))
for x in USB:
    cuts.append(bx(x - USB_W / 2, x + USB_W / 2, yr0, yr1, USB_Z - USB_H / 2, USB_Z + USB_H / 2))
lid = trimesh.boolean.difference([body] + cuts, engine="manifold")
assert lid.is_watertight
# printed upside down: top on the bed
lid.apply_transform(trimesh.transformations.rotation_matrix(np.pi, (1, 0, 0))); lid.apply_translation((0, D, H))
lid.export("lid.stl")
print("STL", lid.bounds.round(2).tolist(), "watertight", lid.is_watertight, "volume cm3", round(lid.volume / 1000, 1))

# --- front and rear views for checking ---
import matplotlib; matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.patches import Rectangle, Circle, FancyBboxPatch
fig, axs = plt.subplots(2, 1, figsize=(10, 5.6), dpi=110)
for ax, title, front in ((axs[0], "FRONT (as seen by the viewer)", True), (axs[1], "REAR (as seen by the viewer)", False)):
    sx = (lambda x: W - x) if front else (lambda x: x)   # mirrored: from the front the viewer's left is large x
    ax.add_patch(FancyBboxPatch((0, 0), W, H, boxstyle=f"round,pad=0,rounding_size={R}", fc="#ddd", ec="k"))
    ax.add_patch(Rectangle((0, -3), W, 3, fc="#aaa", ec="k"))
    if front:
        for x, z in RCA:
            ax.add_patch(Rectangle((sx(x) - RCA_SLOT_W / 2, 0), RCA_SLOT_W, z, fc="w", ec="k")); ax.add_patch(Circle((sx(x), z), RCA_SLOT_W / 2, fc="w", ec="k"))
            ax.text(sx(x), z + 7, "RCA", ha="center", fontsize=8)
        ax.add_patch(Circle((sx(JACK[0]), JACK[1]), JACK_D / 2, fc="w", ec="k")); ax.text(sx(JACK[0]), JACK[1] + 6, "jack", ha="center", fontsize=8)
    else:
        ax.add_patch(Rectangle((PWR[0] - PWR_W / 2, 0), PWR_W, PWR_TOP, fc="w", ec="k"))
        ax.add_patch(Rectangle((TERM_CX - TERM_NOTCH_W / 2, 0), TERM_NOTCH_W, TERM_NOTCH_H, fc="none", ec="#c0392b", ls="--"))
        ax.text(TERM_CX, TERM_NOTCH_H + 1, "1.2 mm inner recess", ha="center", fontsize=7, color="#c0392b")
        ax.text(PWR[0], PWR_TOP + 2, "12 V", ha="center", fontsize=8)
        for x in USB:
            ax.add_patch(Rectangle((x - USB_W / 2, USB_Z - USB_H / 2), USB_W, USB_H, fc="#fff3c4", ec="k")); ax.text(x, USB_Z, "USB-C ×2", ha="center", va="center", fontsize=7)
    ax.set_xlim(-5, W + 5); ax.set_ylim(-6, H + 6); ax.set_aspect("equal"); ax.set_title(title, fontsize=10)
plt.tight_layout(); plt.savefig("lid_views.png")
