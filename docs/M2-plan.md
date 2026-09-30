# M2 – PDF-Objektschicht (Plan)

Annahme: Die Spezifikation liegt nicht im Repo. M2 ist hier abgeleitet aus
dem, was fehlt: M0 Harness, M1 Rasterizer-Kern, M3 Display-List und
Interpretation, M4 Glyph-Cache, M5/M7 Shadings/Bilder, M6 Transparenz.
Übrig bleibt die untere Hälfte des „PDF-Frontends (M2/M3)“ aus
`harness/engine/stilus.go`: Datei lesen, Objekte auflösen, Streams
dekodieren, Seiten und Ressourcen finden, Content-Streams tokenisieren.
M2 endet an der Schnittstelle, die M3 konsumiert; es wird noch nichts
gezeichnet.

## Ziel und Abnahme

| Kriterium | Messung |
|---|---|
| Alle 33 Korpusdateien + Kundendateien öffnen, Seitenzahl und Seitenboxen = PDFium | Harness, neue Spalte `open` |
| Dekodierte Content-Streams byte-gleich mit go-pdfkit/reader (Differenztest) | Harness, `-engines stilus,pdfkit` |
| Öffnen + alle Content-Streams dekodieren schneller als der Parser-Anteil von go-pdfkit (aus dem vorhandenen CPU-Split) | Harness `report.md` |
| Tokenizer über einen Content-Stream: 0 Allokationen nach Warm-up | Alloc-Gate wie `TestScenesNoAllocs` |
| `Doc` gleichzeitig lesbar aus mehreren Goroutinen (Band-Rendering) | `-race`-Test |
| Kein Panic, Budgets greifen; Fuzz-Ziele laufen 10 min ohne Fund | `FuzzLex`, `FuzzXref`, `FuzzFilter`, `FuzzOpen` |

## Paket und Abhängigkeiten

`github.com/timzifer/stilus/pdf`, nur Standardbibliothek
(`compress/zlib`, `crypto/aes`, `crypto/rc4`, `crypto/md5`,
`crypto/sha256`). Das Wurzelpaket bleibt unabhängig von PDF; `pdf`
importiert `stilus` nicht (erst M3 verbindet beide).

## Arbeitspakete

1. **Lexer und Objektmodell.** Zero-Copy über `[]byte` (mmap-fähig):
   Zahlen, Namen (mit `#xx`), Literal- und Hex-Strings, Arrays, Dicts,
   indirekte Referenzen. Objekte als kompakter Tagged-Wert statt
   `interface{}`-Bäumen; Namen interniert, damit Dict-Lookups
   Vergleiche von Kennungen sind. Toleranz wie PDFium (fehlende
   Whitespaces, `endobj` fehlt, Zahlen wie `--5`, `1.2.3`).
2. **Dateistruktur.** `startxref`, klassische Xref-Tabellen, Xref-Streams,
   Hybrid-Dateien, `/Prev`-Kette (inkrementelle Updates), Objekt-Streams.
   Auflösung lazy, mit Zyklenerkennung; Cache pro Objekt.
3. **Reparatur.** Falscher `startxref`, kaputte Offsets, fehlende Trailer:
   Scan nach `n g obj` und `trailer`, Rekonstruktion wie PDFium/pdf.js.
   Falsche `/Length`: bis `endstream` suchen.
4. **Filter.** Flate (+ PNG/TIFF-Prädiktoren), LZW (EarlyChange),
   ASCII85, ASCIIHex, RunLength, Filterketten, DecodeParms.
   Dekoder schreiben in wiederverwendete Puffer. DCT, JPX, JBIG2 und
   CCITT werden nur als Rohdaten + Parameter durchgereicht (M7).
5. **Verschlüsselung.** Standard Security Handler R2–R6: RC4 40/128,
   AES-128, AES-256; leeres Benutzerpasswort automatisch, sonst
   Passwort-API. Strings und Streams erst bei Zugriff entschlüsseln,
   `/Identity`-Crypt-Filter und unverschlüsselte Metadaten beachten.
6. **Dokument-API.** Seitenbaum mit Vererbung (Resources, MediaBox,
   CropBox, Rotate, UserUnit), Seitenzugriff per Index ohne den ganzen
   Baum aufzulösen, Seitenmatrix (Box, Rotation, DPI → Gerät, wie
   PDFium), Ressourcen-Lookup (Font, XObject, ExtGState, ColorSpace,
   Pattern, Shading, Properties).
7. **Content-Tokenizer (Schnittstelle zu M3).** Iterator über
   Operatoren mit Operanden-Slice, verketteten Content-Arrays und
   Inline-Bildern (`BI … ID … EI`, inklusive der bekannten
   EI-Mehrdeutigkeit). Operatoren als Enum statt String. Form-XObjects
   liefern denselben Iterator.
8. **Budgets und Robustheit.** Maximale Verschachtelungstiefe,
   Objektzahl, Xref-Größe, Dekompressionsgröße und -verhältnis
   (Zip-Bomben), Referenztiefe. Fehler statt Panics, analog zu
   `Canvas.Err`.
9. **Harness.** `engine/stilus.go` öffnet echte PDFs: Seitenzahl,
   Seitengrößen, Zeit für Open/Dekodieren, Allokationen; Vergleich mit
   PDFium (Größen) und go-pdfkit/reader (Stream-Bytes). Seiten werden
   bis M3 weiß gerendert und als „nicht gezeichnet“ markiert, damit
   Genauigkeitswerte nicht irreführen.

Reihenfolge: 1 → 2 → 4 → 6 → 7 liefern den Durchstich (arXiv-Paper
öffnen, Content tokenisieren). 3, 5, 8 und 9 laufen danach parallel;
Fuzzing ab Paket 1 mitlaufend.

## Übernahmen aus dem M1-Review (83c0b92)

- **Dichte Dashes:** Unterhalb des Budgets kostet ein Strich jetzt bis
  ~0,5 s (2000 px Linie, Periode 0,004 px; vorher 64 µs, aber falsch
  deckend). Vorschlag: Liegt die Periode im Gerät unter ~0,25 px, solid
  zeichnen mit Deckung × Tastverhältnis (bei Butt-Caps flächengleich),
  sonst exakt; Budget deutlich senken. Wichtig vor M3, weil echte PDFs
  solche Muster enthalten und PDF-Viewer sie nie weglassen.
- **Stilles Verwerfen:** Nicht-endliche Gerätekoordinaten und
  `DashPhase` NaN/Inf verwerfen Pfade ohne `Err()`. Für den
  Harness-Report sollte das zählbar sein.
- **`ClipRect` mit invertiertem Rect ist jetzt leer.** M3 muss
  `re`-Operanden mit negativer Breite/Höhe normalisieren, bevor
  `ClipRect` aufgerufen wird (bzw. `W n` auf `re` über Pfad-Clip).

## Offene Entscheidungen

1. Stimmt der Zuschnitt von M2 mit der Spezifikation überein?
2. Eigener Parser (Plan oben) oder `go-pdfkit/reader` wiederverwenden?
   Eigener Parser: Zero-Alloc, Nebenläufigkeit, Budgets unter eigener
   Kontrolle; Wiederverwendung: schneller am Ziel, aber Allokationsprofil
   und API fremdbestimmt. Empfehlung: eigener Parser, reader als Orakel
   im Differenztest.
3. Umfang Verschlüsselung (R6/AES-256 in M2 oder später)?
