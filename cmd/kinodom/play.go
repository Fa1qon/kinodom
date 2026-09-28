package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"kinodom/internal/config"
	"kinodom/internal/httpx"
	"kinodom/internal/torrents"
)

// cmdPlay — проверка движка без каталога: открывает раздачу на запущенном сервере
// (kinodom run или служба), ждёт буфер и печатает ссылки для VLC на ПК и телевизоре.
func cmdPlay(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("play", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fileIdx := fs.Int("file", -1, "номер файла в раздаче (по умолчанию — первая серия)")
	vlc := fs.Bool("vlc", false, "открыть поток в VLC на этом ПК")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "использование: kinodom play [--file N] [--vlc] <magnet-ссылка или путь к .torrent>")
		return 2
	}
	boot, err := config.LoadBootstrap(config.NewPaths(config.DefaultHome()).Bootstrap)
	if err != nil {
		fmt.Fprintln(stderr, "ошибка конфигурации:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	p := &player{
		base:   fmt.Sprintf("http://127.0.0.1:%d", boot.APIPort),
		out:    stdout,
		client: &http.Client{Timeout: 30 * time.Second},
		poll:   time.Second,
		limit:  5 * time.Minute,
	}
	streamURL, err := p.play(ctx, fs.Arg(0), *fileIdx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "\nГотово. На этом ПК:", streamURL)
	for _, l := range lanURLs(streamURL) {
		fmt.Fprintf(stdout, "Для телевизора (%s): %s\n", l.iface, l.url)
	}
	if *vlc {
		if err := openVLC(streamURL); err != nil {
			fmt.Fprintln(stderr, "VLC:", err)
		}
	}
	fmt.Fprintln(stdout, "Файл докачивается на сервере; эту команду можно закрыть.")
	return 0
}

// player ведёт раздачу через API сервера: открыть → список файлов → prepare → буфер.
type player struct {
	base   string
	out    io.Writer
	client *http.Client
	poll   time.Duration
	limit  time.Duration // сколько всего ждать списка файлов и буфера
}

func (p *player) play(ctx context.Context, src string, fileIdx int) (string, error) {
	req := map[string]any{}
	if strings.HasPrefix(src, "magnet:") {
		req["magnet"] = src
	} else {
		b, err := os.ReadFile(src)
		if err != nil {
			return "", fmt.Errorf("не удалось прочитать %s: %w", src, err)
		}
		req["torrent"] = b
	}
	var opened struct {
		Hash string `json:"hash"`
	}
	if err := p.call(ctx, http.MethodPost, "/api/v1/torrents", req, &opened); err != nil {
		return "", err
	}
	st, err := p.waitTorrent(ctx, opened.Hash)
	if err != nil {
		return "", err
	}
	f, err := chooseFile(st.Files, fileIdx)
	if err != nil {
		return "", err
	}
	if len(st.Files) > 1 {
		fmt.Fprintln(p.out, "Файлы раздачи:")
		for _, x := range st.Files {
			mark := " "
			if x.Index == f.Index {
				mark = "→"
			}
			fmt.Fprintf(p.out, " %s --file %d  %s\n", mark, x.Index, x.Name)
		}
	}
	path := fmt.Sprintf("/api/v1/torrents/%s/files/%d", opened.Hash, f.Index)
	if err := p.call(ctx, http.MethodPost, path+"/prepare", struct{}{}, nil); err != nil {
		return "", err
	}
	return p.waitBuffer(ctx, path)
}

func (p *player) waitTorrent(ctx context.Context, hash string) (torrents.TorrentStatus, error) {
	deadline := time.Now().Add(p.limit)
	for {
		var st torrents.TorrentStatus
		if err := p.call(ctx, http.MethodGet, "/api/v1/torrents/"+hash, nil, &st); err != nil {
			return st, err
		}
		switch st.State {
		case torrents.StateReady:
			return st, nil
		case torrents.StateError:
			return st, errors.New(st.Error)
		}
		fmt.Fprintf(p.out, "  %s · пиров: %d\n", torrentStateText(st.State), st.Peers)
		if time.Now().After(deadline) {
			return st, errors.New("не дождались списка файлов раздачи")
		}
		if err := sleepCtx(ctx, p.poll); err != nil {
			return st, err
		}
	}
}

func (p *player) waitBuffer(ctx context.Context, path string) (string, error) {
	deadline := time.Now().Add(p.limit)
	for {
		var fs struct {
			torrents.FileStatus
			StreamURL string `json:"streamUrl"`
		}
		if err := p.call(ctx, http.MethodGet, path, nil, &fs); err != nil {
			return "", err
		}
		switch fs.State {
		case torrents.FileReady:
			return fs.StreamURL, nil
		case torrents.FileError:
			return "", errors.New(fs.Error)
		}
		fmt.Fprintf(p.out, "  буферизация %d %% · пиров %d · %s/с", fs.BufferPercent, fs.Peers, humanBytes(fs.Speed))
		if fs.SmoothInSec > 0 {
			fmt.Fprintf(p.out, " · без остановок через ~%d мин", (fs.SmoothInSec+59)/60)
		}
		fmt.Fprintln(p.out)
		if time.Now().After(deadline) {
			return "", errors.New("не дождались буфера")
		}
		if err := sleepCtx(ctx, p.poll); err != nil {
			return "", err
		}
	}
}

func chooseFile(files []torrents.FileInfo, idx int) (torrents.FileInfo, error) {
	if len(files) == 0 {
		return torrents.FileInfo{}, errors.New("в раздаче нет видеофайлов")
	}
	if idx < 0 {
		return files[0], nil
	}
	for _, f := range files {
		if f.Index == idx {
			return f, nil
		}
	}
	return torrents.FileInfo{}, fmt.Errorf("файла %d нет среди видеофайлов раздачи", idx)
}

// call — запрос к API сервера; ошибка API возвращается её текстом.
func (p *player) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("сервер Kinodom не отвечает (%s) — запущен ли он? %w", p.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e httpx.ErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func torrentStateText(s torrents.TorrentState) string {
	switch s {
	case torrents.StateConnecting:
		return "ищем участников раздачи"
	case torrents.StateMetadata:
		return "получаем список файлов"
	}
	return string(s)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f КБ", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d Б", n)
}

type lanURL struct{ iface, url string }

// lanURLs — та же ссылка с частными IPv4-адресами интерфейсов ПК: по ним сервер видит
// телевизор. Адрес VPN тоже попадёт в список — выбирайте адаптер домашней сети.
func lanURLs(streamURL string) []lanURL {
	u, err := url.Parse(streamURL)
	if err != nil {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []lanURL
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			v := *u
			v.Host = net.JoinHostPort(ip4.String(), u.Port())
			out = append(out, lanURL{iface: ifc.Name, url: v.String()})
		}
	}
	return out
}

// openVLC запускает VLC с потоком; путь к VLC — из реестра (его пишет установщик VLC).
func openVLC(streamURL string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\VideoLAN\VLC`, registry.QUERY_VALUE)
	if err != nil {
		return errors.New("VLC не найден — установлен ли он?")
	}
	defer k.Close()
	exe, _, err := k.GetStringValue("")
	if err != nil || exe == "" {
		return errors.New("в реестре нет пути к VLC")
	}
	return exec.Command(exe, streamURL).Start()
}
