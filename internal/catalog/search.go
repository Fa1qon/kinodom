package catalog

import (
	"context"
	"time"
)

// Поиск — задача 5; пока ничего не ищет.

const (
	SearchOK      = "ok"
	SearchRunning = "идёт"
)

var searchLimit = 30 * time.Second

type SearchState struct {
	Query    string
	Results  []Entry
	Complete bool
	Trackers map[string]string
}

type searchRun struct{}

func (c *Catalog) Search(ctx context.Context, query string) (SearchState, error) {
	return SearchState{}, nil
}
