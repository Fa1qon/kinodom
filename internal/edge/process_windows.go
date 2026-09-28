package edge

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// bindOnce — kinodom входит в объект задания один раз: при старте модуля edge или при первом
// проходе Edge — что раньше.
var (
	bindOnce sync.Once
	bindErr  error
)

// BindChildren привязывает дочерние процессы kinodom к нему самому (объект задания Windows): Edge
// не переживёт аварию kinodom. Повторный вызов ничего не делает и возвращает первый итог.
func BindChildren() error {
	bindOnce.Do(func() { bindErr = bindChildren() })
	return bindErr
}

// bindChildren — дочерние процессы (Edge и его помощники) умирают вместе с kinodom. Процесс
// входит в объект задания Windows с флагом «убить всех при закрытии»; дети наследуют задание.
// Если kinodom убьют жёстко, система закроет его описатель задания и завершит Edge. Иначе
// оставшийся Edge держал бы профиль, и все следующие проходы падали бы (ревью этапа 4).
// Задание — на весь процесс: Go не умеет создать процесс сразу внутри задания, а привязка после
// запуска опаздывает — Edge за миллисекунды заводит свои дочерние процессы.
func bindChildren() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return err
	}
	// Описатель не закрываем: он живёт до конца процесса, и его закрытие убивает детей.
	return nil
}

// profileBusy — профилем пользуется другой Edge. Запущенный Edge держит файл lockfile в папке
// профиля открытым без общего доступа и удаляет его при выходе (проверено 2026-09-28).
func profileBusy(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "lockfile"), os.O_RDWR, 0)
	if err == nil {
		f.Close()
		return false
	}
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
