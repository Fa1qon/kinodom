package httpx

import "net/http"

// OwnPlayer — поток читает свой плеер Kinodom (?own=1: ffmpeg плеера в браузере, плеер приложения): место
// он сообщает сам, угадывать его по чтению не нужно (спека цикла 18, раздел 4).
func OwnPlayer(r *http.Request) bool { return r.URL.Query().Get("own") == "1" }
