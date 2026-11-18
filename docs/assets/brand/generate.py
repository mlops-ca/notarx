
import sys, os
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
FONTS=sys.argv[1]; OUT=sys.argv[2]
os.makedirs(OUT, exist_ok=True)
def load(name, wght):
    f=TTFont(os.path.join(FONTS,name)); return instancer.instantiateVariableFont(f,{"wght":wght})
brico=load("bricolage-grotesque-latin-variable.woff2",800)
mono=load("jetbrains-mono-latin-variable.woff2",500)
def text_path(font, s, x, y, size, tracking=0.0):
    upm=font['head'].unitsPerEm; gs=font.getGlyphSet(); cmap=font.getBestCmap(); hmtx=font['hmtx']
    sc=size/upm; parts=[]; cx=x
    for ch in s:
        g=cmap.get(ord(ch))
        if g is None: cx+=size*0.3; continue
        pen=SVGPathPen(gs)
        gs[g].draw(TransformPen(pen,(sc,0,0,-sc,cx,y)))
        parts.append(pen.getCommands()); cx+=hmtx[g][0]*sc+tracking*size
    return " ".join(parts), cx-x-tracking*size
INK="#0B0C0A"; BONE="#ECEBE3"; LIME="#C8F135"; LTXT="#3F5C00"; LBG="#EDECE3"; LINK="#12140E"; MUTED="#9CA091"; LMUTED="#555A4A"
# Mark geometry (64 unit): open ring + lime node in the gap + check inside = "the loop verifies"
RING='<path d="M52.67 24.48A22 22 0 1 1 37.69 10.75" fill="none" stroke="{c}" stroke-width="7"/>'
CHECK='<path d="M21 33l6.5 6.5 11.5-12.5" fill="none" stroke="{c}" stroke-width="5.5" stroke-linecap="square" stroke-linejoin="miter"/>'
NODE='<circle cx="46.9" cy="15.8" r="5" fill="#C8F135"/>'
def glyph(ring): return RING.format(c=ring)+CHECK.format(c=ring)+NODE
def svg(w,h,body,vb=None): return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb or f"0 0 {w} {h}"}" width="{w}" height="{h}">{body}</svg>\n'
def save(n,s): open(os.path.join(OUT,n),"w").write(s)
save("notarx-mark.svg", svg(64,64,f'<title>notarx</title><rect width="64" height="64" rx="10" fill="{INK}"/>'+glyph(BONE)))
save("notarx-mark-light.svg", svg(64,64,f'<title>notarx</title><rect width="64" height="64" rx="10" fill="{LBG}"/>'+glyph(LINK)))
save("notarx-glyph-dark.svg", svg(64,64,'<title>notarx</title>'+glyph(BONE)))   # transparent, for dark surfaces
save("notarx-glyph-light.svg", svg(64,64,'<title>notarx</title>'+glyph(LINK)))  # transparent, for light surfaces
# Lockups: glyph + "notar" + accent "x"
def lockup(fg, accent, bg=None):
    size=46; base=46
    p1,w1=text_path(brico,"notar",78,base,size,-0.04)
    p2,w2=text_path(brico,"x",78+w1+size*-0.04,base,size,-0.04)
    W=int(78+w1+w2+12); H=64
    body=(f'<rect width="{W}" height="{H}" fill="{bg}"/>' if bg else '')+f'<g>{glyph(fg)}</g><path d="{p1}" fill="{fg}"/><path d="{p2}" fill="{accent}"/>'
    return svg(W,H,'<title>notarx</title>'+body)
save("notarx-lockup-dark.svg", lockup(BONE, LIME))     # for dark backgrounds
save("notarx-lockup-light.svg", lockup(LINK, LTXT))    # for light backgrounds
# OG / hero 1200x630
def og(W,H):
    b=[f'<rect width="{W}" height="{H}" fill="{INK}"/>']
    # instrument grid with 1px hairlines
    for gx in range(0,W,60): b.append(f'<path d="M{gx}.5 0V{H}" stroke="#161812" stroke-width="1"/>')
    for gy in range(0,H,60): b.append(f'<path d="M0 {gy}.5H{W}" stroke="#161812" stroke-width="1"/>')
    s=3.2; b.append(f'<g transform="translate(96 150) scale({s})">{glyph(BONE)}</g>')
    p1,w1=text_path(brico,"notar",330,300,150,-0.045); p2,_=text_path(brico,"x",330+w1-150*0.045,300,150,-0.045)
    b.append(f'<path d="{p1}" fill="{BONE}"/><path d="{p2}" fill="{LIME}"/>')
    t1,_=text_path(mono,"VERIFIED ARTIFACTS. PRIVATE BY DEFAULT.",100,430,26,0.12); b.append(f'<path d="{t1}" fill="{LIME}"/>')
    t2,_=text_path(mono,"OCI · MAVEN · NPM · PYPI · GO · CARGO · HELM · NUGET",100,480,20,0.10); b.append(f'<path d="{t2}" fill="{MUTED}"/>')
    t3,_=text_path(mono,"MLOPS.CA",100,560,18,0.14); b.append(f'<path d="{t3}" fill="{MUTED}"/>')
    b.append(f'<rect x="100" y="520" width="{W-200}" height="1" fill="#2a2d24"/>')
    return svg(W,H,'<title>notarx — verified artifacts, private by default</title>'+"".join(b))
save("og-image.svg", og(1200,630))
print("ok")
