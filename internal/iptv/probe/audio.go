package probe

import "strings"

// Звук у источника (отзыв заказчика 2026-09-30: у «России 24» стоял поток без звука): по таблице
// дорожек MPEG-TS (PAT → PMT), иначе по CODECS мастер-плейлиста HLS. nil — не понять.

// audioStreamTypes — типы потоков PMT со звуком: MPEG-1/2 аудио, AAC (ADTS и LATM), AC-3, E-AC-3,
// MPEG-4 аудио без упаковки.
var audioStreamTypes = map[byte]bool{0x03: true, 0x04: true, 0x0F: true, 0x11: true, 0x81: true, 0x87: true, 0x1C: true}

// audioDescriptors — у «частного» потока (0x06) звук, если есть дескриптор AC-3, E-AC-3, DTS или AAC.
var audioDescriptors = map[byte]bool{0x6A: true, 0x7A: true, 0x7B: true, 0x7C: true}

// tsAudio — есть ли звук по таблице дорожек в начале данных MPEG-TS; nil — не MPEG-TS или таблицы нет.
func tsAudio(b []byte) *bool {
	start := -1
	for i := 0; i+188 <= len(b) && i < 188; i++ {
		if b[i] == 0x47 && (i+188 >= len(b) || b[i+188] == 0x47) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	pmt := map[int]bool{}
	for off := start; off+188 <= len(b); off += 188 {
		p := b[off : off+188]
		if p[0] != 0x47 || p[1]&0x40 == 0 { // нужен только пакет с началом раздела
			continue
		}
		pid := int(p[1]&0x1F)<<8 | int(p[2])
		payload := p[4:]
		switch (p[3] >> 4) & 3 {
		case 2: // только адаптационное поле
			continue
		case 3:
			if int(p[4])+1 >= len(payload) {
				continue
			}
			payload = payload[1+int(p[4]):]
		}
		if len(payload) < 1 || int(payload[0])+1 >= len(payload) {
			continue
		}
		sec := payload[1+int(payload[0]):]
		switch {
		case pid == 0:
			for _, v := range patPrograms(sec) {
				pmt[v] = true
			}
		case pmt[pid]:
			if r := pmtAudio(sec); r != nil {
				return r
			}
		}
	}
	return nil
}

// sectionBody — тело раздела PSI без CRC; nil — раздел оборван или не той таблицы.
func sectionBody(sec []byte, tableID byte) []byte {
	if len(sec) < 3 || sec[0] != tableID {
		return nil
	}
	l := int(sec[1]&0x0F)<<8 | int(sec[2])
	if l < 4 || 3+l > len(sec) {
		return nil
	}
	return sec[3 : 3+l-4]
}

// patPrograms — PID таблиц PMT из PAT (программа 0 — сетевая, пропускается).
func patPrograms(sec []byte) []int {
	body := sectionBody(sec, 0x00)
	if len(body) < 5 {
		return nil
	}
	var out []int
	for i := 5; i+4 <= len(body); i += 4 {
		program := int(body[i])<<8 | int(body[i+1])
		if program != 0 {
			out = append(out, int(body[i+2]&0x1F)<<8|int(body[i+3]))
		}
	}
	return out
}

// pmtAudio — есть ли в программе поток со звуком.
func pmtAudio(sec []byte) *bool {
	body := sectionBody(sec, 0x02)
	if len(body) < 9 {
		return nil
	}
	i := 9 + (int(body[7]&0x0F)<<8 | int(body[8]))
	found := false
	for i+5 <= len(body) {
		st := body[i]
		n := int(body[i+3]&0x0F)<<8 | int(body[i+4])
		desc := body[min(i+5, len(body)):min(i+5+n, len(body))]
		if audioStreamTypes[st] {
			found = true
		}
		if st == 0x06 {
			for d := 0; d+2 <= len(desc); d += 2 + int(desc[d+1]) {
				if audioDescriptors[desc[d]] {
					found = true
				}
			}
		}
		i += 5 + n
	}
	return &found
}

// codecsAudio — есть ли звук по CODECS варианта HLS (и группе AUDIO с EXT-X-MEDIA); nil — CODECS нет.
func codecsAudio(codecs string, audioGroup bool) *bool {
	if audioGroup {
		yes := true
		return &yes
	}
	if strings.TrimSpace(codecs) == "" {
		return nil
	}
	found := false
	for _, c := range strings.Split(strings.ToLower(codecs), ",") {
		c = strings.TrimSpace(c)
		for _, a := range []string{"mp4a", "ac-3", "ec-3", "opus", "mp3", "dtsc", "flac"} {
			if strings.HasPrefix(c, a) {
				found = true
			}
		}
	}
	return &found
}
