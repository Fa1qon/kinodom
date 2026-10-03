package playback

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// tail — последние байты вывода ошибок ffmpeg: в журнал при отказе.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.TrimSpace(string(t.b)) }

// output — запустить bin и вернуть его вывод; процесс скрыт и в задании (не переживёт сервер).
func (t Tools) output(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	hide(cmd)
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	var errs tail
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s не запустился: %w", baseOf(bin), err)
	}
	adopt(cmd.Process)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w: %s", baseOf(bin), err, errs.String())
	}
	return out.Bytes(), nil
}

func baseOf(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
