//go:build !windows

package updates

import "errors"

// На Android сервер обновляется вместе с приложением, на остальных — вручную.
func launch(string) error { return errors.ErrUnsupported }

const canInstall = false
