package torrents

import "time"

// TorrentState — стадия открытия раздачи для клиента.
type TorrentState string

const (
	StateConnecting TorrentState = "connecting" // ищем участников раздачи
	StateMetadata   TorrentState = "metadata"   // участники есть, получаем список файлов
	StateReady      TorrentState = "ready"      // список файлов есть
	StateError      TorrentState = "error"
)

// Пороги из спеки (раздел 9).
const (
	noPeersAfter    = 30 * time.Second
	noMetadataAfter = 90 * time.Second
)

const (
	errNoPeers    = "Нет раздающих: за 30 секунд не удалось подключиться ни к одному участнику раздачи"
	errNoMetadata = "Не удалось получить список файлов раздачи за 90 секунд"
)

// openObs — наблюдения за открываемой раздачей в момент now.
type openObs struct {
	now          time.Time
	haveInfo     bool
	activePeers  int
	noPeersSince time.Time // с какого момента нет пиров при готовой сети; ноль — сеть не готова или пиры есть
	peersSince   time.Time // когда впервые появились пиры; ноль — их не было
	stored       bool      // есть хранимые файлы — правила времени не применяются
}

// evalOpenState — правила открытия раздачи. Отдельная чистая функция, чтобы проверять
// правила без сети и часов.
func evalOpenState(o openObs, noPeers, noMeta time.Duration) (TorrentState, string) {
	switch {
	case o.haveInfo:
		return StateReady, ""
	case o.stored:
		if o.activePeers > 0 {
			return StateMetadata, ""
		}
		return StateConnecting, ""
	case o.activePeers == 0 && !o.noPeersSince.IsZero() && o.now.Sub(o.noPeersSince) >= noPeers:
		return StateError, errNoPeers
	case o.activePeers > 0 && !o.peersSince.IsZero() && o.now.Sub(o.peersSince) >= noMeta:
		return StateError, errNoMetadata
	case o.activePeers > 0:
		return StateMetadata, ""
	default:
		return StateConnecting, ""
	}
}
