package catalog

import "context"

// enrichLoop и enrichStep — догрузка раздач (задача 4); пока ничего не догружают.
func (c *Catalog) enrichLoop(ctx context.Context, tracker string) error {
	<-ctx.Done()
	return nil
}

func (c *Catalog) enrichStep(ctx context.Context, tracker string) (bool, error) { return false, nil }
