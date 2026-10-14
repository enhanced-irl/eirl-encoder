package network

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// QRSVG: the QR of a text as SVG (a single path, 4-module margin), no CDN or JS: the API serves it
// and the web puts it in an <img>. Error correction M: copes well with a photo with reflections.
func QRSVG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := c.Size + 8
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return []byte(b.String()), nil
}

// QRRows: the QR as rows of '1' and '0' (so the engine can draw it on the screen without libraries).
func QRRows(text string) ([]string, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	rows := make([]string, c.Size)
	for y := range rows {
		var r strings.Builder
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) {
				r.WriteByte('1')
			} else {
				r.WriteByte('0')
			}
		}
		rows[y] = r.String()
	}
	return rows, nil
}
