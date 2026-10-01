; Установщик Kinodom (спека этапа 11a, раздел 5). Собирает build.ps1 -Installer:
; ISCC /DAppVersion=… /DNumVersion=… installer\kinodom.iss → dist\Kinodom-<версия>-setup.exe.
; Установщик только копирует файлы и вызывает команды kinodom.exe: stop, install --check (до
; копирования), install (после), uninstall. Исключение в AfterInstall Inno не откатывает установку —
; поэтому отказ до копирования — в PrepareToInstall, после — честная страница «Не установлен» и удаление.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef NumVersion
  #define NumVersion "0.0.0.0"
#endif
; Постоянный: по нему установщик новой версии узнаёт установленную (обновление поверх).
#define AppGuid "8F6A5C2E-4B1D-4E7A-9C3F-2D5B6A7E8F90"

[Setup]
AppId={{{#AppGuid}}
AppName=Kinodom
AppVersion={#AppVersion}
AppVerName=Kinodom {#AppVersion}
AppPublisher=Kinodom
VersionInfoVersion={#NumVersion}
DefaultDirName={autopf}\Kinodom
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyMemo=yes
PrivilegesRequired=admin
MinVersion=10.0
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\dist
OutputBaseFilename=Kinodom-{#AppVersion}-setup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=kinodom.ico
UninstallDisplayIcon={app}\kinodom.ico
UninstallDisplayName=Kinodom
; Службу останавливает kinodom.exe stop (PrepareToInstall), а не «Перезапуск приложений» Windows.
CloseApplications=no
SetupLogging=yes

[Languages]
Name: "ru"; MessagesFile: "compiler:Languages\Russian.isl"

[Tasks]
Name: "desktopicon"; Description: "Ярлык на рабочем столе"; Flags: unchecked

[Files]
Source: "..\bin\kinodom.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\bin\kinodomw.exe"; DestDir: "{app}"; Flags: ignoreversion
; Приложение для Android (спека этапа 13, 5.3): сервер раздаёт его по /app; сборка без Android SDK — без него.
Source: "..\bin\kinodom.apk"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "..\bin\kinodom.apk.json"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "kinodom.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; Ярлык «Kinodom» — как у UMS: запускает сервер (если его остановили «Выходом»), значок в трее и пульт.
Name: "{autoprograms}\Kinodom"; Filename: "{app}\kinodomw.exe"; Parameters: "tray --open"; WorkingDir: "{app}"; IconFilename: "{app}\kinodom.ico"
Name: "{autodesktop}\Kinodom"; Filename: "{app}\kinodomw.exe"; Parameters: "tray --open"; WorkingDir: "{app}"; IconFilename: "{app}\kinodom.ico"; Tasks: desktopicon

[UninstallDelete]
; Ярлыки-ссылки первых сборок этапа 11a.
Type: files; Name: "{autoprograms}\Kinodom.url"
Type: files; Name: "{autodesktop}\Kinodom.url"

[Run]
; Пульт — флажком на странице «Готово». Значок в трее запускает CurStepChanged сразу после удачного
; kinodom install (строка [Run] без postinstall выполняется раньше и проверку не проходит).
Filename: "{app}\kinodomw.exe"; Parameters: "tray --open"; Description: "Открыть Kinodom"; Flags: postinstall nowait skipifsilent; Check: InstallSucceeded

[Code]
const
  DRIVE_FIXED = 3;
  GB = 1073741824;

function GetDriveType(lpRootPathName: String): UInt;
  external 'GetDriveTypeW@kernel32.dll stdcall';

var
  DownloadsPage: TInputDirWizardPage;
  WasInstalled: Boolean;
  StoppedByUs: Boolean; { PrepareToInstall остановил службу }
  CopyStarted: Boolean; { файлы начали копироваться: папка программы известна, есть что убирать }
  InstallOK: Boolean;   { kinodom.exe install прошёл }
  FailText: String;     { почему не установлен — для страницы «Готово» }

function InstallSucceeded: Boolean;
begin
  Result := InstallOK;
end;

{ Kinodom уже стоит (ключ удаления — с фигурными скобками, как AppId) или остались его настройки
  (удаление без данных): папку загрузок не спрашиваем, выбор человека не трогаем. }
function IsInstalled: Boolean;
begin
  Result := RegKeyExists(HKLM, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{' + '{#AppGuid}' + '}_is1')
    or FileExists(ExpandConstant('{commonappdata}\Kinodom\data\kinodom.db'));
end;

{ Диск ПК с наибольшим свободным местом. }
function BestDrive: String;
var
  I: Integer;
  Root: String;
  Free, Total, Best: Int64;
begin
  Result := 'C:';
  Best := -1;
  for I := Ord('C') to Ord('Z') do
  begin
    Root := Chr(I) + ':\';
    if (GetDriveType(Root) = DRIVE_FIXED) and GetSpaceOnDisk64(Root, Free, Total) and (Free > Best) then
    begin
      Best := Free;
      Result := Chr(I) + ':';
    end;
  end;
end;

{ «C: — 120 ГБ свободно», по строке на диск. }
function DrivesText: String;
var
  I: Integer;
  Root: String;
  Free, Total: Int64;
begin
  Result := '';
  for I := Ord('C') to Ord('Z') do
  begin
    Root := Chr(I) + ':\';
    if (GetDriveType(Root) = DRIVE_FIXED) and GetSpaceOnDisk64(Root, Free, Total) then
      Result := Result + Chr(I) + ': — ' + IntToStr(Free div GB) + ' ГБ свободно' + #13#10;
  end;
end;

procedure InitializeWizard;
begin
  WasInstalled := IsInstalled;
  DownloadsPage := CreateInputDirPage(wpWelcome, 'Папка загрузок', 'Сюда Kinodom скачивает фильмы и сериалы',
    DrivesText, False, '');
  DownloadsPage.Add('');
  DownloadsPage.Values[0] := BestDrive + '\Kinodom';
end;

function ShouldSkipPage(PageID: Integer): Boolean;
begin
  Result := WasInstalled and (PageID = DownloadsPage.ID);
end;

{ Путь в кавычках для командной строки: «\» перед закрывающей кавычкой её бы экранировал. }
function QuotedPath(P: String): String;
begin
  if Copy(P, Length(P), 1) = '\' then
    P := P + '.';
  Result := '"' + P + '"';
end;

{ Текст отказа команды kinodom.exe из файла --result (UTF-8). }
function ResultText(F: String; Code: Integer): String;
var
  Lines: TArrayOfString;
  I: Integer;
begin
  Result := '';
  if LoadStringsFromFile(F, Lines) then
    for I := 0 to GetArrayLength(Lines) - 1 do
      Result := Result + Lines[I] + #13#10;
  if Result = '' then
    Result := 'kinodom.exe завершился с кодом ' + IntToStr(Code);
end;

{ Параметры kinodom.exe install: папка загрузок — только при первой установке и только «по умолчанию»
  (сохранённый выбор человека не перезаписывается). }
function InstallParams(ResultFile: String): String;
begin
  Result := '--result ' + QuotedPath(ResultFile);
  if not WasInstalled then
    Result := Result + ' --downloads-default ' + QuotedPath(DownloadsPage.Values[0]);
end;

{ До копирования файлов: обновление — служба останавливается (не остановилась — старая версия цела);
  затем kinodom.exe из установщика проверяет, пройдёт ли установка (права, папка загрузок, порт). Отказ —
  текст на этой странице, ничего не скопировано. }
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Exe, ResultFile: String;
  Code: Integer;
begin
  Result := '';
  Exe := ExpandConstant('{app}\kinodom.exe');
  if FileExists(Exe) then
  begin
    if Exec(Exe, 'stop', '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0) then
      StoppedByUs := True
    else
    begin
      Result := 'Служба Kinodom не остановилась. Закройте просмотр на телевизорах и телефонах и запустите установку ещё раз.';
      Exit;
    end;
  end;
  ExtractTemporaryFile('kinodom.exe');
  ResultFile := ExpandConstant('{tmp}\check-result.txt');
  if not Exec(ExpandConstant('{tmp}\kinodom.exe'), 'install --check ' + InstallParams(ResultFile), '', SW_HIDE,
     ewWaitUntilTerminated, Code) or (Code <> 0) then
    Result := 'Kinodom не установлен: ' + ResultText(ResultFile, Code);
end;

{ После копирования — системная часть: kinodom.exe install. Отказ — окно с его текстом и страница
  «Готово» с ним же; убрать или починить — в DeinitializeSetup. }
procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultFile: String;
  Code: Integer;
begin
  if CurStep = ssInstall then
    CopyStarted := True;
  if CurStep = ssPostInstall then
  begin
    ResultFile := ExpandConstant('{tmp}\install-result.txt');
    WizardForm.StatusLabel.Caption := 'Настраиваю службу Kinodom…';
    if Exec(ExpandConstant('{app}\kinodom.exe'), 'install ' + InstallParams(ResultFile), '', SW_HIDE,
       ewWaitUntilTerminated, Code) and (Code = 0) then
    begin
      InstallOK := True;
      { Значок в трее — от имени пользователя, запустившего установку (сам установщик — от администратора);
        и при тихом обновлении: stop его закрывал. }
      ExecAsOriginalUser(ExpandConstant('{app}\kinodomw.exe'), 'tray', '', SW_SHOWNORMAL, ewNoWait, Code);
    end
    else
    begin
      FailText := ResultText(ResultFile, Code);
      if not WizardSilent then
        MsgBox('Kinodom не установлен:' + #13#10 + FailText, mbError, MB_OK);
    end;
  end;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (CurPageID = wpFinished) and CopyStarted and not InstallOK then
  begin
    WizardForm.FinishedHeadingLabel.Caption := 'Kinodom не установлен';
    WizardForm.FinishedLabel.Caption := FailText;
  end;
end;

{ Установка не дошла до конца. Первая — удаляет себя целиком (служба, правила и ссылка уже убраны
  kinodom install). Обновление или отказ после остановки службы — kinodom.exe install той версии, что
  на месте, чинит установку: возвращает перезапуск при сбое и запускает службу. }
procedure DeinitializeSetup;
var
  Code: Integer;
begin
  if InstallOK then
    Exit;
  if StoppedByUs and FileExists(ExpandConstant('{app}\kinodom.exe')) then
    Exec(ExpandConstant('{app}\kinodom.exe'), 'install', '', SW_HIDE, ewWaitUntilTerminated, Code)
  else if CopyStarted and FileExists(ExpandConstant('{app}\unins000.exe')) then
    Exec(ExpandConstant('{app}\unins000.exe'), '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART', '', SW_HIDE,
      ewWaitUntilTerminated, Code);
end;

{ Удаление — после стандартного «Вы действительно хотите удалить Kinodom?»: вопрос о данных, затем
  kinodom.exe uninstall, пока файлы на месте. Отказ (служба не остановилась) — его текст и Abort: файлы
  не удаляются, иначе осталась бы служба без программы. }
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Params, ResultFile, Msg: String;
  Code: Integer;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Params := 'uninstall';
  if not UninstallSilent and
     (MsgBox('Удалить также скачанное и настройки?', mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
    Params := 'uninstall --purge';
  ResultFile := AddBackslash(GetTempDir) + 'kinodom-uninstall-result.txt';
  DeleteFile(ResultFile);
  if Exec(ExpandConstant('{app}\kinodom.exe'), Params + ' --result ' + QuotedPath(ResultFile), '', SW_HIDE,
     ewWaitUntilTerminated, Code) and (Code = 0) then
    Exit;
  Msg := ResultText(ResultFile, Code);
  if not UninstallSilent then
    MsgBox('Kinodom не удалён: ' + Msg, mbError, MB_OK);
  Abort;
end;
