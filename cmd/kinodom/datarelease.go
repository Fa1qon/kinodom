package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kinodom/internal/updates"
)

// cmdDataRelease — файлы релиза баз data-ГГГГ.ММ.ДД с подписью (docs/data-packs-and-updates.md):
//
//	kinodom data-release -out dist\data recs=..\kinosovet\data\recs-model.db:1
//
// Ключ — файл из KINODOM_SIGN_KEY (по умолчанию %USERPROFILE%\.kinodom-sign\data.key). Выкладывает
// владелец: gh release create data-<версия> --latest=false <файлы>.
func cmdDataRelease(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("data-release", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", filepath.Join("dist", "data"), "папка для файлов релиза")
	version := fs.String("version", time.Now().Format("2006.01.02"), "выпуск баз")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var in []updates.Input
	for _, a := range fs.Args() {
		pack, rest, ok1 := strings.Cut(a, "=")
		i := strings.LastIndex(rest, ":")
		schema, err := strconv.Atoi(rest[i+1:])
		if !ok1 || i < 0 || err != nil {
			fmt.Fprintf(stderr, "ожидается имя=путь:схема, а не %q\n", a)
			return 2
		}
		in = append(in, updates.Input{Pack: pack, Path: rest[:i], Schema: schema})
	}
	if len(in) == 0 {
		fmt.Fprintln(stderr, "не указаны базы: recs=путь:схема")
		return 2
	}
	keyPath := os.Getenv("KINODOM_SIGN_KEY")
	if keyPath == "" {
		home, _ := os.UserHomeDir()
		keyPath = filepath.Join(home, ".kinodom-sign", "data.key")
	}
	key, err := updates.LoadKey(keyPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dir := filepath.Join(*out, updates.DataPrefix+*version)
	m, err := updates.BuildRelease(dir, *version, key, in)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, f := range m.Files {
		fmt.Fprintf(stdout, "%s  %s схема %d  %.1f МБ\n", f.Name, f.Pack, f.Schema, float64(f.Size)/(1<<20))
	}
	fmt.Fprintf(stdout, "готово: %s\n", dir)
	return 0
}
