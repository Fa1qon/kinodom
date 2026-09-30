package rutracker

import (
	"slices"
	"testing"
	"time"

	"kinodom/internal/source"
	"kinodom/internal/source/rutracker/rutrackertest"
)

func TestCategoriesFromTree(t *testing.T) {
	s := rutrackertest.NewServer(t)
	cats, err := newRutracker(t, s, nil).Categories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	find := func(id string) (source.Category, bool) {
		i := slices.IndexFunc(cats, func(c source.Category) bool { return c.ID == id })
		if i < 0 {
			return source.Category{}, false
		}
		return cats[i], true
	}
	c20, ok1 := find("c20")
	f46, ok2 := find("46")
	f2076, ok3 := find("2076")
	if !ok1 || !ok2 || !ok3 || c20.Name != "Документалистика и юмор" || f46.ParentID != "c20" || f2076.ParentID != "46" || f2076.Name != "[Док] Космос" {
		t.Fatalf("дерево: %+v %+v %+v", c20, f46, f2076)
	}
}

func TestForumsUnderSearchCategories(t *testing.T) {
	s := rutrackertest.NewServer(t)
	tree, err := newRutracker(t, s, nil).forumTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allowed := tree.forumsUnder("2", "18", "20", "10")
	if !allowed["46"] || !allowed["2076"] || allowed["c20"] {
		t.Fatalf("разделы поиска: 46=%v 2076=%v", allowed["46"], allowed["2076"])
	}
}

// Весь список раздела (limit ≤ 0) — для каталога без ограничения по количеству (спека 11b, 7.2).
func TestTopWholeList(t *testing.T) {
	s := rutrackertest.NewServer(t)
	r := newRutracker(t, s, nil)
	rs, err := r.Top(ctx, "56", 0)
	if err != nil || len(rs) != 1279 {
		t.Fatalf("весь список: %d, %v", len(rs), err)
	}
	for i := 1; i < len(rs); i++ {
		if rs[i].Seeders > rs[i-1].Seeders {
			t.Fatal("не по раздающим")
		}
	}
}

func TestTopFromPVC(t *testing.T) {
	s := rutrackertest.NewServer(t)
	rs, err := newRutracker(t, s, nil).Top(ctx, "2076", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 10 || !slices.IsSortedFunc(rs, func(a, b source.Release) int { return b.Seeders - a.Seeders }) {
		t.Fatalf("топ: %d строк", len(rs))
	}
	for _, r := range rs {
		if r.Tracker != Name || r.CategoryID != "2076" || len(r.InfoHash) != 40 || r.Size <= 0 || r.Added.IsZero() || r.Title != "" {
			t.Fatalf("строка топа: %+v", r)
		}
	}
	if s.Hits("/v1/static/pvc/f/2076") != 1 || s.Hits("/v1/static/pvc/f/56") != 0 {
		t.Fatal("топ раздела — только сам раздел, без подразделов (спека, раздел 6)")
	}
}

func TestTopOfForumWithoutTopicsIsEmpty(t *testing.T) {
	s := rutrackertest.NewServer(t)
	rs, err := newRutracker(t, s, nil).Top(ctx, "9999", 10)
	if err != nil || len(rs) != 0 {
		t.Fatalf("пустой раздел: %d строк, %v", len(rs), err)
	}
}

func TestTopRejectsBadCategory(t *testing.T) {
	s := rutrackertest.NewServer(t)
	if _, err := newRutracker(t, s, nil).Top(ctx, "c20", 10); err == nil {
		t.Fatal("категория — не раздел: ошибки нет")
	}
}

func TestRecentFromAtom(t *testing.T) {
	s := rutrackertest.NewServer(t)
	rs, err := newRutracker(t, s, nil).Recent(ctx, "313")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 50 {
		t.Fatalf("записей %d, в образце 50", len(rs))
	}
	first := rs[0]
	if first.TopicID != "6900203" || first.CategoryID != "313" || first.InfoHash != "bfeccf2f58d1bd9bd21cfe91f7efb81716230ee2" ||
		!first.Added.Equal(time.Date(2026, 9, 27, 21, 26, 33, 0, time.UTC)) || first.Title[:len("Иностранец / The Foreigner")] != "Иностранец / The Foreigner" {
		t.Fatalf("первая запись: %+v", first)
	}
}
