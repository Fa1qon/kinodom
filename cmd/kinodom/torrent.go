package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"kinodom/internal/netx"
	"kinodom/internal/store"
	"kinodom/internal/torrents"
)

// cmdTorrent — проверка движка вживую без сервера: kinodom torrent info <magnet> — метаинфо раздачи от
// пиров во временной раздаче без хранилища (подписка на новые серии, спека 11b, 6.3.1).
func cmdTorrent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("torrent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	wait := fs.Duration("wait", 5*time.Minute, "сколько ждать метаинфо")
	if len(args) == 0 || args[0] != "info" {
		fmt.Fprintln(stderr, "использование: kinodom torrent info [--wait 5m] <magnet-ссылка>")
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "использование: kinodom torrent info [--wait 5m] <magnet-ссылка>")
		return 2
	}
	tmp, err := os.MkdirTemp("", "kinodom-torrent-")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(tmp)
	port, err := netx.FreeTCPUDPPort("")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	eng, err := torrents.NewEngine(torrents.Config{DownloadsDir: filepath.Join(tmp, "dl"), StateDir: filepath.Join(tmp, "state"), ListenPort: port})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer eng.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	db, err := store.Open(ctx, filepath.Join(tmp, "kinodom.db"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	s := torrents.NewService(eng, torrents.NewRegistry(db), nil)
	ctx, cancel := context.WithTimeout(ctx, *wait)
	defer cancel()
	start := time.Now()
	raw, err := s.FetchInfo(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Метаинфо за %s: «%s», %d файлов\n", time.Since(start).Round(time.Second), info.BestName(), len(info.UpvertedFiles()))
	for i, f := range info.UpvertedFiles() {
		fmt.Fprintf(stdout, "%3d  %s  %d\n", i, strings.Join(f.BestPath(), "/"), f.Length)
	}
	return 0
}
