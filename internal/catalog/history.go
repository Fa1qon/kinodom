package catalog

import (
	"context"
	"net/http"
	"strings"
	"time"

	"kinodom/internal/httpx"
)

// historyKeep — сколько последних запросов помнит история поиска.
const historyKeep = 20

// HistoryItem — запрос из истории поиска.
type HistoryItem struct {
	Query string    `json:"query"`
	At    time.Time `json:"at"`
}

// historyKey — запрос без учёта регистра и лишних пробелов: «  Космос » и «космос» — один запрос.
func historyKey(q string) string { return strings.ToLower(strings.Join(strings.Fields(q), " ")) }

// rememberQuery — запрос в историю поиска: он становится последним, старше двадцатого — забываются.
func (c *Catalog) rememberQuery(ctx context.Context, q string) error {
	key := historyKey(q)
	if key == "" {
		return nil
	}
	tx, err := c.db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO search_history(key, query, at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET query = excluded.query, at = excluded.at`,
		key, strings.Join(strings.Fields(q), " "), ms(c.now())); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM search_history WHERE key NOT IN (SELECT key FROM search_history ORDER BY at DESC, rowid DESC LIMIT ?)`,
		historyKeep); err != nil {
		return err
	}
	return tx.Commit()
}

// History — история поиска, новые первыми.
func (c *Catalog) History(ctx context.Context) ([]HistoryItem, error) {
	rows, err := c.db.R.QueryContext(ctx, `SELECT query, at FROM search_history ORDER BY at DESC, rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryItem{}
	for rows.Next() {
		var it HistoryItem
		var at int64
		if err := rows.Scan(&it.Query, &at); err != nil {
			return nil, err
		}
		it.At = fromMS(at)
		out = append(out, it)
	}
	return out, rows.Err()
}

// ForgetQuery убирает запрос из истории; q = "" — очистить всю историю.
func (c *Catalog) ForgetQuery(ctx context.Context, q string) error {
	if strings.TrimSpace(q) == "" {
		_, err := c.db.W.ExecContext(ctx, `DELETE FROM search_history`)
		return err
	}
	_, err := c.db.W.ExecContext(ctx, `DELETE FROM search_history WHERE key = ?`, historyKey(q))
	return err
}

// SearchView — поиск для API: пульт повторяет запрос с poll=1 раз в секунду, пока complete = false
// (основная спека, раздел 7).
type SearchView struct {
	Query    string            `json:"query"`
	Results  []EntryView       `json:"results"`
	Complete bool              `json:"complete"`
	Trackers map[string]string `json:"trackers"` // трекер → «ok», «идёт» или текст ошибки
}

// handleSearch — поиск; запрос без poll=1 попадает в историю (повторные опросы того же поиска — нет).
func (c *Catalog) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		httpx.WriteError(w, http.StatusBadRequest, "пустой поисковый запрос")
		return
	}
	poll := r.URL.Query().Get("poll") == "1"
	st, err := c.search(r.Context(), q, poll)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "поиск не удался: "+err.Error())
		return
	}
	if !poll {
		if err := c.rememberQuery(r.Context(), q); err != nil {
			c.log.Error("поиск: запрос не записался в историю", "err", err)
		}
	}
	out := SearchView{Query: st.Query, Results: []EntryView{}, Complete: st.Complete, Trackers: st.Trackers}
	for _, e := range st.Results {
		out.Results = append(out.Results, e.View())
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (c *Catalog) handleHistory(w http.ResponseWriter, r *http.Request) {
	h, err := c.History(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "история поиска не читается: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h)
}

// handleForget — убрать запрос из истории или очистить её. С любого устройства: история не секрет и
// ничего не ломает (спека этапа 7, раздел 5.4).
func (c *Catalog) handleForget(w http.ResponseWriter, r *http.Request) {
	if err := c.ForgetQuery(r.Context(), r.URL.Query().Get("q")); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "история поиска не изменилась: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
