# Regenerates icons/icon-{16,32,48,128}.png: python3 tools/make_icons.py icons
import struct, zlib, sys, os
out = sys.argv[1]
def png(n):
    bg=(37,99,235); fg=(255,255,255)
    rows=[]
    r=n*0.2
    for y in range(n):
        row=bytearray([0])
        for x in range(n):
            # rounded square mask
            cx=min(max(x+0.5,r),n-r); cy=min(max(y+0.5,r),n-r)
            inside=((x+0.5-cx)**2+(y+0.5-cy)**2)<=r*r
            if not inside:
                row+=bytes((0,0,0,0)); continue
            u=(x+0.5)/n; v=(y+0.5)/n
            white=False
            if 0.42<=u<=0.58 and 0.18<=v<=0.52: white=True          # shaft
            if 0.52<=v<=0.70 and abs(u-0.5)<=(0.70-v)*1.25: white=True # head
            if 0.76<=v<=0.84 and 0.24<=u<=0.76: white=True           # tray
            c=fg if white else bg
            row+=bytes((*c,255))
        rows.append(bytes(row))
    raw=b''.join(rows)
    def chunk(t,d): return struct.pack('>I',len(d))+t+d+struct.pack('>I',zlib.crc32(t+d)&0xffffffff)
    return b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('>IIBBBBB',n,n,8,6,0,0,0))+chunk(b'IDAT',zlib.compress(raw,9))+chunk(b'IEND',b'')
for n in (16,32,48,128):
    open(os.path.join(out,f'icon-{n}.png'),'wb').write(png(n))
