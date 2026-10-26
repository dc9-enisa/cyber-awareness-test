package main

import (
	_ "embed"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // strefa Europe/Warsaw także w obrazie bez /usr/share/zoneinfo
)

//go:embed assets/SchibstedGrotesk-Regular.ttf
var fontRegular []byte

//go:embed assets/SchibstedGrotesk-Bold.ttf
var fontBold []byte

//go:embed assets/SchibstedGrotesk-ExtraBold.ttf
var fontXB []byte

//go:embed assets/eu_finansowane.png
var euMark []byte

//go:embed assets/enisa_sygnet.png
var enisaMark []byte

var warsaw, _ = time.LoadLocation("Europe/Warsaw")

type certFonts struct{ reg, bold, xb *TTF }

func loadCertFonts() (*certFonts, error) {
	var f certFonts
	var err error
	if f.reg, err = ParseTTF("SchibstedGrotesk-Regular", fontRegular); err != nil {
		return nil, err
	}
	if f.bold, err = ParseTTF("SchibstedGrotesk-Bold", fontBold); err != nil {
		return nil, err
	}
	if f.xb, err = ParseTTF("SchibstedGrotesk-ExtraBold", fontXB); err != nil {
		return nil, err
	}
	return &f, nil
}

var (
	cBlue  = Hex("003399")
	cInk   = Hex("172334")
	cMuted = Hex("536172")
	cLine  = Hex("D6DDE6")
	cStone = Hex("EDF1F6")
)

func plDate(t time.Time) string { return t.In(warsaw).Format("02.01.2006") }

// CertificatePDF – zaświadczenie uczestnictwa A4 poziomo (842 × 595 pt): strona 1 – zaświadczenie,
// strona 2 – zakres szkolenia (moduły i zagadnienia).
func (a *App) CertificatePDF(rec *Record) ([]byte, error) {
	p := NewPDF(842, 595)
	p.Title = "Zaświadczenie uczestnictwa – " + rec.displayName()
	c := &certDoc{a: a, p: p, rec: rec,
		reg: p.AddFont(a.fonts.reg), bold: p.AddFont(a.fonts.bold), xb: p.AddFont(a.fonts.xb)}
	var err error
	if c.eu, err = p.AddPNG(euMark); err != nil {
		return nil, err
	}
	if c.enisa, err = p.AddPNG(enisaMark); err != nil {
		return nil, err
	}
	c.page1()
	p.AddPage()
	c.page2()
	return p.Bytes(), nil
}

const certL, certR = 72.0, 842 - 60.0

type certDoc struct {
	a             *App
	p             *PDF
	rec           *Record
	reg, bold, xb *pdfFont
	eu, enisa     *pdfImage
}

func (c *certDoc) page1() {
	p, a, rec := c.p, c.a, c.rec
	const L, R = certL, certR
	w := R - L

	p.Rect(cBlue, 0, 0, 16, 595)
	p.Text(c.bold, 12, cMuted, L, 70, a.cfg.OrgName)
	p.Text(c.xb, 46, cBlue, L, 150, "Zaświadczenie")
	p.Text(c.bold, 20, cInk, L, 184, "uczestnictwa w szkoleniu e-learningowym")

	name := rec.displayName()
	size := 34.0
	for size > 20 && a.fonts.xb.Width(name, size) > w {
		size--
	}
	p.Text(c.reg, 12, cMuted, L, 236, "Uczestnik / uczestniczka")
	p.Text(c.xb, size, cInk, L, 274, name)
	p.Line(cLine, 1, L, 290, R, 290)

	y := 324.0
	body := fmt.Sprintf("Szkolenie „Cyberbezpieczeństwo w codziennej pracy” (%d modułów) zakończone testem końcowym z wynikiem %d z %d (próg zaliczenia: %d). Zakres szkolenia – na stronie 2.",
		len(a.bank.Modules), rec.Correct, rec.Total, a.bank.Threshold)
	for _, ln := range Wrap(a.fonts.reg, 15, w, body) {
		p.Text(c.reg, 15, cInk, L, y, ln)
		y += 21
	}

	// pas danych zaświadczenia
	const by = 420.0
	p.Rect(cStone, L, by, w, 58)
	p.Text(c.reg, 9.5, cMuted, L+16, by+21, "Data ukończenia")
	p.Text(c.bold, 13, cInk, L+16, by+41, plDate(rec.Finished))
	p.Text(c.reg, 9.5, cMuted, L+150, by+21, "Numer zaświadczenia")
	p.Text(c.bold, 13, cInk, L+150, by+41, rec.CertID)
	verify := a.cfg.BaseURL + "/weryfikacja/" + rec.CertID
	p.Text(c.reg, 9.5, cMuted, L+340, by+21, "Weryfikacja (dla zalogowanych pracowników organizacji)")
	vs := 10.5 // długi adres serwera: mniejszy krój, aby zmieścić się w pasie
	for vs > 7 && a.fonts.bold.Width(verify, vs) > R-(L+340)-12 {
		vs -= 0.5
	}
	p.Text(c.bold, vs, cBlue, L+340, by+41, verify)
	p.Link(L+340, by+28, a.fonts.bold.Width(verify, vs), 16, verify)
	c.footer(1)
}

// page2 – zakres: moduły w dwóch kolumnach, przy każdym zagadnienia z kursu.
func (c *certDoc) page2() {
	p, a, rec := c.p, c.a, c.rec
	const L, R = certL, certR
	p.Rect(cBlue, 0, 0, 16, 595)
	p.Text(c.xb, 26, cBlue, L, 76, "Zakres szkolenia")
	p.Text(c.reg, 10.5, cMuted, L, 98, "Załącznik do zaświadczenia nr "+rec.CertID+" · "+rec.displayName())

	const gap = 28.0
	colW := (R - L - gap) / 2
	mods := a.bank.Modules
	half := (len(mods) + 1) / 2
	// rozmiar dobierany tak, aby obie kolumny zmieściły się nad stopką
	ts, is := 11.0, 9.0
	height := func(ms []Module) float64 {
		h := 0.0
		for _, m := range ms {
			h += ts*1.35*float64(len(Wrap(a.fonts.bold, ts, colW, m.Code+". "+m.Title))) +
				is*1.4*float64(len(Wrap(a.fonts.reg, is, colW, strings.Join(m.Items, "; ")+"."))) + 9
		}
		return h
	}
	for ts > 8 && max(height(mods[:half]), height(mods[half:])) > 400 {
		ts, is = ts-0.5, is-0.5
	}
	for col, ms := range [][]Module{mods[:half], mods[half:]} {
		x, y := L+float64(col)*(colW+gap), 130.0
		for _, m := range ms {
			for _, ln := range Wrap(a.fonts.bold, ts, colW, m.Code+". "+m.Title) {
				p.Text(c.bold, ts, cBlue, x, y, ln)
				y += ts * 1.35
			}
			if len(m.Items) > 0 {
				for _, ln := range Wrap(a.fonts.reg, is, colW, strings.Join(m.Items, "; ")+".") {
					p.Text(c.reg, is, cInk, x, y, ln)
					y += is * 1.4
				}
			}
			y += 9
		}
	}
	c.footer(2)
}

// footer – nota o wydawcy, znaki ENISA i UE (białe pole, wolna przestrzeń; znak UE nie mniejszy niż ENISA), numer strony.
func (c *certDoc) footer(page int) {
	p, a := c.p, c.a
	const L, R, y = certL, certR, 526.0
	note := "Zaświadczenie wydaje " + strings.TrimRight(a.cfg.OrgName, ".") + ". Potwierdza udział w szkoleniu i wynik testu końcowego. " +
		"Nie jest certyfikatem ani kwalifikacją zawodową i nie zostało wydane ani poświadczone przez ENISA ani Unię Europejską."
	ny := y + 8
	for _, ln := range Wrap(a.fonts.reg, 8, R-L-330, note) {
		p.Text(c.reg, 8, cMuted, L, ny, ln)
		ny += 11
	}
	p.Text(c.reg, 8, cMuted, L, ny+4, fmt.Sprintf("Strona %d z 2", page))
	// od prawej: znak UE 40 pt (flaga ≥ 1 cm), separator, sygnet ENISA 32 pt z łączem do strony agencji
	ew := 40 * float64(c.eu.w) / float64(c.eu.h)
	p.Image(c.eu, R-ew, y, 40)
	sx := R - ew - 18
	p.Line(Hex("B9C3D0"), 0.8, sx, y+4, sx, y+36)
	nw := 32 * float64(c.enisa.w) / float64(c.enisa.h)
	p.Image(c.enisa, sx-18-nw, y+4, 32)
	p.Link(sx-18-nw, y+4, nw, 32, "https://www.enisa.europa.eu/")
}

func (r *Record) displayName() string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return r.UPN
}
