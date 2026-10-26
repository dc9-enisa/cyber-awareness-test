package main

// Minimalny zapis PDF 1.7 dla zaświadczeń: fonty TrueType osadzone w całości (Type0/CIDFontType2,
// Identity-H, mapa ToUnicode – tekst da się zaznaczyć i przeszukać), obraz PNG z kanałem alfa,
// prostokąty, linie i łącza. Wyłącznie biblioteka standardowa.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image/png"
	"sort"
	"strings"
	"unicode/utf16"
)

// ---------- TrueType ----------

type TTF struct {
	Name       string
	raw        []byte
	unitsPerEm int
	ascent     int
	descent    int
	capHeight  int
	bbox       [4]int
	advances   []uint16
	cmap       map[rune]uint16
}

func ParseTTF(name string, raw []byte) (*TTF, error) {
	if len(raw) < 12 {
		return nil, errors.New("ttf: za krótki plik")
	}
	be := binary.BigEndian
	n := int(be.Uint16(raw[4:]))
	tables := map[string][]byte{}
	for i := 0; i < n; i++ {
		rec := 12 + 16*i
		if rec+16 > len(raw) {
			return nil, errors.New("ttf: uszkodzony katalog tabel")
		}
		off, ln := int(be.Uint32(raw[rec+8:])), int(be.Uint32(raw[rec+12:]))
		if off < 0 || ln < 0 || off+ln > len(raw) {
			return nil, errors.New("ttf: tabela poza plikiem")
		}
		tables[string(raw[rec:rec+4])] = raw[off : off+ln]
	}
	for _, t := range []string{"head", "hhea", "hmtx", "cmap", "maxp"} {
		if tables[t] == nil {
			return nil, fmt.Errorf("ttf: brak tabeli %s", t)
		}
	}
	f := &TTF{Name: name, raw: raw}
	head, hhea := tables["head"], tables["hhea"]
	if len(head) < 54 || len(hhea) < 36 {
		return nil, errors.New("ttf: uszkodzone head/hhea")
	}
	f.unitsPerEm = int(be.Uint16(head[18:]))
	for i := 0; i < 4; i++ {
		f.bbox[i] = int(int16(be.Uint16(head[36+2*i:])))
	}
	f.ascent = int(int16(be.Uint16(hhea[4:])))
	f.descent = int(int16(be.Uint16(hhea[6:])))
	f.capHeight = f.ascent * 7 / 10
	if os2 := tables["OS/2"]; len(os2) >= 90 && be.Uint16(os2) >= 2 {
		f.capHeight = int(int16(be.Uint16(os2[88:])))
	}
	nh := int(be.Uint16(hhea[34:]))
	hmtx := tables["hmtx"]
	if len(hmtx) < 4*nh {
		return nil, errors.New("ttf: uszkodzone hmtx")
	}
	for i := 0; i < nh; i++ {
		f.advances = append(f.advances, be.Uint16(hmtx[4*i:]))
	}
	var err error
	if f.cmap, err = parseCmap(tables["cmap"]); err != nil {
		return nil, err
	}
	return f, nil
}

func parseCmap(t []byte) (map[rune]uint16, error) {
	be := binary.BigEndian
	if len(t) < 4 {
		return nil, errors.New("ttf: uszkodzone cmap")
	}
	best, bestScore := -1, 0
	for i := 0; i < int(be.Uint16(t[2:])); i++ {
		r := 4 + 8*i
		if r+8 > len(t) {
			break
		}
		plat, enc, off := be.Uint16(t[r:]), be.Uint16(t[r+2:]), int(be.Uint32(t[r+4:]))
		if off+2 > len(t) {
			continue
		}
		format := be.Uint16(t[off:])
		score := 0
		switch {
		case format == 12 && (plat == 3 && enc == 10 || plat == 0):
			score = 3
		case format == 4 && (plat == 3 && enc == 1 || plat == 0):
			score = 2
		}
		if score > bestScore {
			best, bestScore = off, score
		}
	}
	if best < 0 {
		return nil, errors.New("ttf: brak obsługiwanej tabeli cmap (Unicode, format 4 lub 12)")
	}
	m := map[rune]uint16{}
	s := t[best:]
	switch be.Uint16(s) {
	case 12:
		if len(s) < 16 {
			return nil, errors.New("ttf: uszkodzone cmap 12")
		}
		ng := int(be.Uint32(s[12:]))
		for g := 0; g < ng && 16+12*g+12 <= len(s); g++ {
			p := s[16+12*g:]
			start, end, gid := be.Uint32(p), be.Uint32(p[4:]), be.Uint32(p[8:])
			for c := start; c <= end && c-start < 0x10000; c++ {
				m[rune(c)] = uint16(gid + c - start)
			}
		}
	case 4:
		if len(s) < 14 {
			return nil, errors.New("ttf: uszkodzone cmap 4")
		}
		seg := int(be.Uint16(s[6:])) / 2
		ends, starts := 14, 14+2*seg+2
		deltas, ranges := starts+2*seg, starts+4*seg
		if ranges+2*seg > len(s) {
			return nil, errors.New("ttf: uszkodzone cmap 4")
		}
		for i := 0; i < seg; i++ {
			end, start := int(be.Uint16(s[ends+2*i:])), int(be.Uint16(s[starts+2*i:]))
			delta, ro := int(int16(be.Uint16(s[deltas+2*i:]))), int(be.Uint16(s[ranges+2*i:]))
			for c := start; c <= end && c != 0xFFFF; c++ {
				var g int
				if ro == 0 {
					g = (c + delta) & 0xFFFF
				} else {
					p := ranges + 2*i + ro + 2*(c-start)
					if p+2 > len(s) {
						continue
					}
					if g = int(be.Uint16(s[p:])); g != 0 {
						g = (g + delta) & 0xFFFF
					}
				}
				if g != 0 {
					m[rune(c)] = uint16(g)
				}
			}
		}
	}
	return m, nil
}

func (f *TTF) glyph(r rune) uint16 { return f.cmap[r] } // 0 = .notdef

func (f *TTF) advance(g uint16) int {
	if int(g) < len(f.advances) {
		return int(f.advances[g])
	}
	return int(f.advances[len(f.advances)-1])
}

// Width – szerokość tekstu w punktach przy danym rozmiarze.
func (f *TTF) Width(s string, size float64) float64 {
	u := 0
	for _, r := range s {
		u += f.advance(f.glyph(r))
	}
	return float64(u) * size / float64(f.unitsPerEm)
}

func (f *TTF) scale(v int) int { return v * 1000 / f.unitsPerEm }

// ---------- dokument ----------

type pdfFont struct {
	ttf  *TTF
	res  string          // nazwa zasobu, np. F1
	used map[uint16]rune // glify użyte w dokumencie → znak (ToUnicode)
}

type pdfImage struct {
	res        string
	w, h       int
	rgb, alpha []byte
}

type pdfLink struct {
	x0, y0, x1, y1 float64
	uri            string
}

type pdfPage struct {
	buf   bytes.Buffer // strumień treści strony
	links []pdfLink
}

type PDF struct {
	W, H   float64
	fonts  []*pdfFont
	images []*pdfImage
	pages  []*pdfPage
	cur    *pdfPage
	Title  string
	Lang   string
}

// NewPDF tworzy dokument z jedną pustą stroną; kolejne dodaje AddPage.
func NewPDF(w, h float64) *PDF {
	p := &PDF{W: w, H: h, Lang: "pl-PL"}
	p.AddPage()
	return p
}

func (p *PDF) AddPage() {
	p.cur = &pdfPage{}
	p.pages = append(p.pages, p.cur)
}

func (p *PDF) AddFont(f *TTF) *pdfFont {
	pf := &pdfFont{ttf: f, res: fmt.Sprintf("F%d", len(p.fonts)+1), used: map[uint16]rune{}}
	p.fonts = append(p.fonts, pf)
	return pf
}

func (p *PDF) AddPNG(data []byte) (*pdfImage, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	im := &pdfImage{res: fmt.Sprintf("Im%d", len(p.images)+1), w: b.Dx(), h: b.Dy()}
	im.rgb = make([]byte, 0, 3*im.w*im.h)
	im.alpha = make([]byte, 0, im.w*im.h)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			// image zwraca wartości przemnożone przez alfę; PDF z SMask oczekuje koloru nieprzemnożonego
			if a > 0 && a < 0xffff {
				r, g, bl = r*0xffff/a, g*0xffff/a, bl*0xffff/a
			}
			im.rgb = append(im.rgb, byte(r>>8), byte(g>>8), byte(bl>>8))
			im.alpha = append(im.alpha, byte(a>>8))
		}
	}
	p.images = append(p.images, im)
	return im, nil
}

type RGB struct{ R, G, B float64 }

func Hex(s string) RGB {
	var r, g, b int
	fmt.Sscanf(strings.TrimPrefix(s, "#"), "%02x%02x%02x", &r, &g, &b)
	return RGB{float64(r) / 255, float64(g) / 255, float64(b) / 255}
}

// Text rysuje tekst od lewego punktu linii bazowej (x, y – od lewego górnego rogu strony).
func (p *PDF) Text(f *pdfFont, size float64, c RGB, x, y float64, s string) {
	var hex strings.Builder
	for _, r := range s {
		g := f.ttf.glyph(r)
		f.used[g] = r
		fmt.Fprintf(&hex, "%04X", g)
	}
	fmt.Fprintf(&p.cur.buf, "BT /%s %.2f Tf %.3f %.3f %.3f rg %.2f %.2f Td <%s> Tj ET\n",
		f.res, size, c.R, c.G, c.B, x, p.H-y, hex.String())
}

// TextRight / TextCenter – wyrównanie do prawej krawędzi x lub środka x.
func (p *PDF) TextRight(f *pdfFont, size float64, c RGB, x, y float64, s string) {
	p.Text(f, size, c, x-f.ttf.Width(s, size), y, s)
}

func (p *PDF) TextCenter(f *pdfFont, size float64, c RGB, x, y float64, s string) {
	p.Text(f, size, c, x-f.ttf.Width(s, size)/2, y, s)
}

// Wrap dzieli tekst na wiersze o szerokości nie większej niż w (łamanie na spacjach).
func Wrap(f *TTF, size, w float64, s string) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if cur != "" && f.Width(try, size) > w {
			lines = append(lines, cur)
			cur = word
		} else {
			cur = try
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func (p *PDF) Rect(c RGB, x, y, w, h float64) {
	fmt.Fprintf(&p.cur.buf, "%.3f %.3f %.3f rg %.2f %.2f %.2f %.2f re f\n", c.R, c.G, c.B, x, p.H-y-h, w, h)
}

func (p *PDF) Line(c RGB, width, x0, y0, x1, y1 float64) {
	fmt.Fprintf(&p.cur.buf, "%.3f %.3f %.3f RG %.2f w %.2f %.2f m %.2f %.2f l S\n", c.R, c.G, c.B, width, x0, p.H-y0, x1, p.H-y1)
}

// Image rysuje obraz o wysokości h z zachowaniem proporcji; zwraca szerokość.
func (p *PDF) Image(im *pdfImage, x, y, h float64) float64 {
	w := h * float64(im.w) / float64(im.h)
	fmt.Fprintf(&p.cur.buf, "q %.2f 0 0 %.2f %.2f %.2f cm /%s Do Q\n", w, h, x, p.H-y-h, im.res)
	return w
}

func (p *PDF) Link(x, y, w, h float64, uri string) {
	p.cur.links = append(p.cur.links, pdfLink{x, p.H - y - h, x + w, p.H - y, uri})
}

// ---------- serializacja ----------

type objWriter struct {
	out  bytes.Buffer
	offs []int
}

func (o *objWriter) reserve() int { o.offs = append(o.offs, 0); return len(o.offs) }

func (o *objWriter) put(id int, body string) {
	o.offs[id-1] = o.out.Len()
	fmt.Fprintf(&o.out, "%d 0 obj\n%s\nendobj\n", id, body)
}

func (o *objWriter) stream(id int, dict string, data []byte, compress bool) {
	if compress {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(data)
		zw.Close()
		data = z.Bytes()
		dict += " /Filter /FlateDecode"
	}
	o.offs[id-1] = o.out.Len()
	fmt.Fprintf(&o.out, "%d 0 obj\n<<%s /Length %d>>\nstream\n", id, dict, len(data))
	o.out.Write(data)
	o.out.WriteString("\nendstream\nendobj\n")
}

// pdfText – napis PDF w UTF-16BE (polskie znaki w metadanych).
func pdfText(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

func pdfASCII(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`)
	return "(" + r.Replace(s) + ")"
}

func (p *PDF) Bytes() []byte {
	o := &objWriter{}
	o.out.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	catalog, pages, info := o.reserve(), o.reserve(), o.reserve()

	var fontRes, imgRes strings.Builder
	for _, f := range p.fonts {
		t := f.ttf
		file, desc, cid, uni, top := o.reserve(), o.reserve(), o.reserve(), o.reserve(), o.reserve()
		o.stream(file, fmt.Sprintf(" /Length1 %d", len(t.raw)), t.raw, true)
		o.put(desc, fmt.Sprintf("<</Type /FontDescriptor /FontName /%s /Flags 32 /FontBBox [%d %d %d %d] /ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV 80 /FontFile2 %d 0 R>>",
			t.Name, t.scale(t.bbox[0]), t.scale(t.bbox[1]), t.scale(t.bbox[2]), t.scale(t.bbox[3]),
			t.scale(t.ascent), t.scale(t.descent), t.scale(t.capHeight), file))
		gids := make([]int, 0, len(f.used))
		for g := range f.used {
			gids = append(gids, int(g))
		}
		sort.Ints(gids)
		var wArr, cmap strings.Builder
		for _, g := range gids {
			fmt.Fprintf(&wArr, "%d [%d] ", g, t.scale(t.advance(uint16(g))))
		}
		o.put(cid, fmt.Sprintf("<</Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo <</Registry (Adobe) /Ordering (Identity) /Supplement 0>> /FontDescriptor %d 0 R /DW %d /W [%s] /CIDToGIDMap /Identity>>",
			t.Name, desc, t.scale(t.advance(0)), wArr.String()))
		cmap.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n/CIDSystemInfo <</Registry (Adobe) /Ordering (UCS) /Supplement 0>> def\n/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
		for i := 0; i < len(gids); i += 100 {
			chunk := gids[i:min(i+100, len(gids))]
			fmt.Fprintf(&cmap, "%d beginbfchar\n", len(chunk))
			for _, g := range chunk {
				fmt.Fprintf(&cmap, "<%04X> <", g)
				for _, u := range utf16.Encode([]rune{f.used[uint16(g)]}) {
					fmt.Fprintf(&cmap, "%04X", u)
				}
				cmap.WriteString(">\n")
			}
			cmap.WriteString("endbfchar\n")
		}
		cmap.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
		o.stream(uni, "", []byte(cmap.String()), true)
		o.put(top, fmt.Sprintf("<</Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R>>", t.Name, cid, uni))
		fmt.Fprintf(&fontRes, "/%s %d 0 R ", f.res, top)
	}
	for _, im := range p.images {
		mask, obj := o.reserve(), o.reserve()
		o.stream(mask, fmt.Sprintf(" /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8", im.w, im.h), im.alpha, true)
		o.stream(obj, fmt.Sprintf(" /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask %d 0 R", im.w, im.h, mask), im.rgb, true)
		fmt.Fprintf(&imgRes, "/%s %d 0 R ", im.res, obj)
	}
	var kids strings.Builder
	for _, pg := range p.pages {
		page, content := o.reserve(), o.reserve()
		var annots strings.Builder
		for _, l := range pg.links {
			id := o.reserve()
			o.put(id, fmt.Sprintf("<</Type /Annot /Subtype /Link /Rect [%.2f %.2f %.2f %.2f] /Border [0 0 0] /A <</S /URI /URI %s>>>>", l.x0, l.y0, l.x1, l.y1, pdfASCII(l.uri)))
			fmt.Fprintf(&annots, "%d 0 R ", id)
		}
		o.stream(content, "", pg.buf.Bytes(), true)
		o.put(page, fmt.Sprintf("<</Type /Page /Parent %d 0 R /MediaBox [0 0 %.0f %.0f] /Resources <</Font <<%s>> /XObject <<%s>>>> /Contents %d 0 R /Annots [%s]>>",
			pages, p.W, p.H, fontRes.String(), imgRes.String(), content, annots.String()))
		fmt.Fprintf(&kids, "%d 0 R ", page)
	}
	o.put(pages, fmt.Sprintf("<</Type /Pages /Kids [%s] /Count %d>>", kids.String(), len(p.pages)))
	o.put(catalog, fmt.Sprintf("<</Type /Catalog /Pages %d 0 R /Lang %s /ViewerPreferences <</DisplayDocTitle true>>>>", pages, pdfASCII(p.Lang)))
	o.put(info, fmt.Sprintf("<</Title %s /Producer (Cyberbezpieczenstwo w codziennej pracy - aplikacja testowa)>>", pdfText(p.Title)))

	xref := o.out.Len()
	fmt.Fprintf(&o.out, "xref\n0 %d\n0000000000 65535 f \n", len(o.offs)+1)
	for _, off := range o.offs {
		fmt.Fprintf(&o.out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&o.out, "trailer\n<</Size %d /Root %d 0 R /Info %d 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(o.offs)+1, catalog, info, xref)
	return o.out.Bytes()
}
