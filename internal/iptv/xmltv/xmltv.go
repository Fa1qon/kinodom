// Package xmltv — разбор телепрограммы XMLTV (спека этапа 8, раздел 5.7): справочник каналов и
// передачи в окне времени. Файл разбирается потоком, результат живёт в памяти.
package xmltv

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Channel — канал телепрограммы.
type Channel struct {
	ID    string
	Names []string // все названия, первое — основное
	Icon  string
}

// Programme — передача; время абсолютное.
type Programme struct {
	Start time.Time `json:"start"`
	Stop  time.Time `json:"stop"`
	Title string    `json:"title"`
}

// Guide — разобранная телепрограмма.
type Guide struct {
	Channels []Channel
	byID     map[string]int
	progs    map[string][]Programme // по началу
}

// Parse читает XMLTV (.xml или .xml.gz — по первым байтам). Передачи хранятся только те, что
// пересекают окно [from, to).
func Parse(r io.Reader, from, to time.Time) (*Guide, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	if head, err := br.Peek(2); err == nil && head[0] == 0x1f && head[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("телепрограмма: %w", err)
		}
		defer zr.Close()
		br = bufio.NewReaderSize(zr, 1<<16)
	}
	g := &Guide{byID: map[string]int{}, progs: map[string][]Programme{}}
	titles := map[string]string{} // одинаковые названия — одна строка
	dec := xml.NewDecoder(br)
	dec.Strict = false
	sawTV := false
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("телепрограмма не читается: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "tv":
			sawTV = true
		case "channel":
			c, err := readChannel(dec, se)
			if err != nil {
				return nil, err
			}
			if c.ID != "" {
				if _, dup := g.byID[c.ID]; !dup {
					g.byID[c.ID] = len(g.Channels)
					g.Channels = append(g.Channels, c)
				}
			}
		case "programme":
			id, p, err := readProgramme(dec, se)
			if err != nil {
				return nil, err
			}
			if id == "" || p.Start.IsZero() || !p.Stop.After(from) || !p.Start.Before(to) {
				continue
			}
			if t, ok := titles[p.Title]; ok {
				p.Title = t
			} else {
				titles[p.Title] = p.Title
			}
			g.progs[id] = append(g.progs[id], p)
		}
	}
	if !sawTV {
		return nil, errors.New("это не телепрограмма XMLTV: нет элемента <tv>")
	}
	for _, ps := range g.progs {
		sort.Slice(ps, func(i, j int) bool { return ps[i].Start.Before(ps[j].Start) })
	}
	return g, nil
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// readChannel — <channel id><display-name>…</display-name><icon src/></channel>.
func readChannel(dec *xml.Decoder, se xml.StartElement) (Channel, error) {
	c := Channel{ID: attr(se, "id")}
	err := walk(dec, func(child xml.StartElement, text func() (string, error)) error {
		switch child.Name.Local {
		case "display-name":
			s, err := text()
			if err != nil {
				return err
			}
			if s = strings.TrimSpace(s); s != "" {
				c.Names = append(c.Names, s)
			}
		case "icon":
			if c.Icon == "" {
				c.Icon = attr(child, "src")
			}
		}
		return nil
	})
	return c, err
}

// readProgramme — <programme start stop channel><title>…</title>…</programme>.
func readProgramme(dec *xml.Decoder, se xml.StartElement) (string, Programme, error) {
	var p Programme
	p.Start, _ = parseTime(attr(se, "start"))
	p.Stop, _ = parseTime(attr(se, "stop"))
	err := walk(dec, func(child xml.StartElement, text func() (string, error)) error {
		if child.Name.Local == "title" && p.Title == "" {
			s, err := text()
			if err != nil {
				return err
			}
			p.Title = strings.TrimSpace(s)
		}
		return nil
	})
	if p.Stop.IsZero() || p.Stop.Before(p.Start) {
		p.Stop = p.Start.Add(time.Hour)
	}
	return attr(se, "channel"), p, err
}

// walk обходит прямых детей элемента до его закрывающего тега. text читает текст ребёнка (и
// закрывает его); если ребёнка не прочли, он пропускается целиком.
func walk(dec *xml.Decoder, fn func(child xml.StartElement, text func() (string, error)) error) error {
	for {
		tok, err := dec.RawToken()
		if err != nil {
			return fmt.Errorf("телепрограмма не читается: %w", err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.StartElement:
			done := false
			text := func() (string, error) {
				done = true
				return readText(dec)
			}
			if err := fn(t, text); err != nil {
				return err
			}
			if !done {
				if err := skip(dec); err != nil {
					return err
				}
			}
		}
	}
}

// readText — текст до закрывающего тега текущего элемента (вложенные теги пропускаются).
func readText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	depth := 0
	for {
		tok, err := dec.RawToken()
		if err != nil {
			return "", fmt.Errorf("телепрограмма не читается: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			if depth == 0 {
				b.Write(t)
			}
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				return b.String(), nil
			}
			depth--
		}
	}
}

func skip(dec *xml.Decoder) error {
	depth := 0
	for {
		tok, err := dec.RawToken()
		if err != nil {
			return fmt.Errorf("телепрограмма не читается: %w", err)
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth == 0 {
				return nil
			}
			depth--
		}
	}
}

// parseTime — «20260929190000 +0300»; без пояса — UTC.
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) > 14 {
		return time.Parse("20060102150405 -0700", s[:14]+" "+strings.TrimSpace(s[14:]))
	}
	return time.ParseInLocation("20060102150405", s, time.UTC)
}

// Channel — канал по id.
func (g *Guide) Channel(id string) (Channel, bool) {
	i, ok := g.byID[id]
	if !ok {
		return Channel{}, false
	}
	return g.Channels[i], true
}

// Programmes — передачи канала, пересекающие [from, to). shift — сдвиг региональной версии в часах:
// версия «+N» показывает программу на N часов раньше (спека этапа 8, раздел 5.7).
func (g *Guide) Programmes(id string, shift int, from, to time.Time) []Programme {
	d := time.Duration(shift) * time.Hour
	var out []Programme
	for _, p := range g.progs[id] {
		p.Start, p.Stop = p.Start.Add(-d), p.Stop.Add(-d)
		if p.Stop.After(from) && p.Start.Before(to) {
			out = append(out, p)
		}
	}
	return out
}

// NowNext — передача, которая идёт в момент at, и следующая; nil — нет.
func (g *Guide) NowNext(id string, shift int, at time.Time) (now, next *Programme) {
	d := time.Duration(shift) * time.Hour
	ps := g.progs[id]
	i := sort.Search(len(ps), func(i int) bool { return ps[i].Stop.Add(-d).After(at) })
	if i < len(ps) && !ps[i].Start.Add(-d).After(at) {
		p := ps[i]
		p.Start, p.Stop = p.Start.Add(-d), p.Stop.Add(-d)
		now = &p
		i++
	}
	if i < len(ps) {
		p := ps[i]
		p.Start, p.Stop = p.Start.Add(-d), p.Stop.Add(-d)
		next = &p
	}
	return now, next
}

// Has — у канала есть передачи в окне.
func (g *Guide) Has(id string) bool { return len(g.progs[id]) > 0 }
