package settings

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"kinodom/internal/trackerpack"
)

// fakeProfiles — профили по домену.
type fakeProfiles map[string]trackerpack.Profile

func (f fakeProfiles) Lookup(addr string) (trackerpack.Profile, error) {
	d, err := trackerpack.Domain(addr)
	if err != nil {
		return trackerpack.Profile{}, err
	}
	if p, ok := f[d]; ok {
		return p, nil
	}
	return trackerpack.Profile{}, trackerpack.ErrUnknown
}

var profiles = fakeProfiles{
	"forum.example":  {Name: "Форум", Engine: "t2"},
	"mirror.example": {Name: "Форум", Engine: "t2"},
	"top.example":    {Name: "Топ", Engine: "t1"},
	"open.example":   {Name: "Открытый", Engine: "cardigann", Def: "open-def"},
}

// Список адресов раскладывается по движкам из профилей; название — из профиля; список целиком заменяет прежний.
func TestTrackersByProfile(t *testing.T) {
	db := openDB(t)
	s := New(db, load(t, db, nil), nil)
	if _, err := s.Update(ctx, patch(t, `{"trackers":["forum.example"]}`)); err == nil {
		t.Fatal("без профилей список принят")
	}
	s.SetProfiles(profiles)
	v, err := s.Update(ctx, patch(t, `{"trackers":["https://forum.example/index.php", "http://www.top.example", "open.example", " "]}`))
	if err != nil {
		t.Fatal(err)
	}
	if v.t2Address != "https://forum.example" || v.t1Address != "http://top.example" ||
		!reflect.DeepEqual(v.OpenTrackers, []OpenTracker{{Def: "open-def", Address: "https://open.example"}}) {
		t.Fatalf("разложено: %q %q %+v", v.t2Address, v.t1Address, v.OpenTrackers)
	}
	view := s.View()
	var names []string
	for _, tr := range view.Trackers {
		names = append(names, tr.Name+"="+tr.Engine)
	}
	if strings.Join(names, ",") != "Форум=t2,Топ=t1,Открытый=cardigann" {
		t.Fatalf("в пульте: %v", names)
	}
	// сохранено в базу: после перезапуска то же
	if got := load(t, db, nil); !reflect.DeepEqual(got.OpenTrackers, v.OpenTrackers) || got.t1Address != v.t1Address {
		t.Fatalf("после перезапуска: %+v", got)
	}
	for js, want := range map[string]string{
		`{"trackers":["unknown.example"]}`:                "не поддерживается",
		`{"trackers":["forum.example","mirror.example"]}`: "уже в списке",
		`{"trackers":["ftp://forum.example"]}`:            "http",
	} {
		_, err := s.Update(ctx, patch(t, js))
		var fe *FieldError
		if !errors.As(err, &fe) || !strings.HasPrefix(fe.Field, "Трекер") || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, ждали «%s»", js, err, want)
		}
	}
	v, err = s.Update(ctx, patch(t, `{"trackers":[]}`))
	if err != nil || v.t2Address != "" || v.t1Address != "" || v.OpenTrackers == nil || len(v.OpenTrackers) != 0 {
		t.Fatalf("пустой список: %+v, %v", v, err)
	}
}

// Настройки списка ещё нет (установка до него) — nil: приложение возьмёт уже скачанные описания.
func TestOpenTrackersUnsetIsNil(t *testing.T) {
	db := openDB(t)
	if v := load(t, db, nil); v.OpenTrackers != nil {
		t.Fatalf("без настройки: %+v", v.OpenTrackers)
	}
	db.SetSetting(ctx, KeyOpenTrackers, "[]")
	if v := load(t, db, nil); v.OpenTrackers == nil {
		t.Fatal("пустой список прочитан как «нет настройки»")
	}
}
