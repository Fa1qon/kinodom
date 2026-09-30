; Установщик Kinodom (спека этапа 11a, раздел 5). Собирает build.ps1 -Installer:
; ISCC /DAppVersion=… /DNumVersion=… installer\kinodom.iss → dist\Kinodom-<версия>-setup.exe.
; Установщик только копирует файлы и вызывает команды kinodom.exe: stop, install, uninstall.

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
UninstallDisplayIcon={app}\kinodom.exe
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
Source: "..\bin\kinodomw.exe"; DestDir: "{app}"; Flags: ignoreversion; AfterInstall: RunKinodomInstall

[INI]
; Ярлыки — ссылки на пульт в браузере.
Filename: "{autoprograms}\Kinodom.url"; Section: "InternetShortcut"; Key: "URL"; String: "http://localhost:{code:APIPort}"
Filename: "{autodesktop}\Kinodom.url"; Section: "InternetShortcut"; Key: "URL"; String: "http://localhost:{code:APIPort}"; Tasks: desktopicon

[UninstallDelete]
Type: files; Name: "{autoprograms}\Kinodom.url"
Type: files; Name: "{autodesktop}\Kinodom.url"

[Run]
Filename: "http://localhost:{code:APIPort}"; Description: "Открыть Kinodom"; Flags: postinstall shellexec nowait skipifsilent

[Code]
const
  DRIVE_FIXED = 3;
  GB = 1073741824;

function GetDriveType(lpRootPathName: String): UInt;
  external 'GetDriveTypeW@kernel32.dll stdcall';

var
  DownloadsPage: TInputDirWizardPage;
  WasInstalled, InstallFailed: Boolean;

{ Kinodom уже стоит — обновление поверх: папку загрузок не спрашиваем, настройки не трогаем. }
function IsInstalled: Boolean;
begin
  Result := RegKeyExists(HKLM, 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{#AppGuid}_is1');
end;

{ Порт пульта — из kinodom.json, иначе 8090. }
function APIPort(Param: String): String;
var
  S: AnsiString;
  I, J: Integer;
begin
  Result := '8090';
  if not LoadStringFromFile(ExpandConstant('{commonappdata}\Kinodom\kinodom.json'), S) then
    Exit;
  I := Pos('"apiPort"', S);
  if I = 0 then
    Exit;
  I := I + Length('"apiPort"');
  while (I <= Length(S)) and ((S[I] = ':') or (S[I] = ' ')) do
    I := I + 1;
  J := I;
  while (J <= Length(S)) and (S[J] >= '0') and (S[J] <= '9') do
    J := J + 1;
  if J > I then
    Result := Copy(S, I, J - I);
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

{ Обновление поверх: служба останавливается до замены файлов. Не остановилась — установка не
  начинается, старая версия цела. }
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Exe: String;
  Code: Integer;
begin
  Result := '';
  Exe := ExpandConstant('{app}\kinodom.exe');
  if FileExists(Exe) then
    if not Exec(Exe, 'stop', '', SW_HIDE, ewWaitUntilTerminated, Code) or (Code <> 0) then
      Result := 'Служба Kinodom не остановилась. Закройте просмотр на телевизорах и телефонах и запустите установку ещё раз.';
end;

{ Путь в кавычках для командной строки: «\» перед закрывающей кавычкой её бы экранировал. }
function QuotedPath(P: String): String;
begin
  if Copy(P, Length(P), 1) = '\' then
    P := P + '.';
  Result := '"' + P + '"';
end;

{ Системная часть — kinodom.exe install. Отказ — его текст в окне, установка откатывается. }
procedure RunKinodomInstall;
var
  Params, ResultFile, Msg: String;
  Lines: TArrayOfString;
  Code, I: Integer;
begin
  ResultFile := ExpandConstant('{tmp}\install-result.txt');
  Params := 'install --result ' + QuotedPath(ResultFile);
  if not WasInstalled then
    Params := Params + ' --downloads ' + QuotedPath(DownloadsPage.Values[0]);
  WizardForm.StatusLabel.Caption := 'Настраиваю службу Kinodom…';
  if Exec(ExpandConstant('{app}\kinodom.exe'), Params, '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0) then
    Exit;
  Msg := '';
  if LoadStringsFromFile(ResultFile, Lines) then
    for I := 0 to GetArrayLength(Lines) - 1 do
      Msg := Msg + Lines[I] + #13#10;
  if Msg = '' then
    Msg := SysErrorMessage(Code);
  InstallFailed := True;
  RaiseException('Kinodom не установлен: ' + Msg);
end;

{ Обновление не удалось и откатилось: прежние файлы на месте — прежняя служба запускается снова. }
procedure DeinitializeSetup;
var
  Code: Integer;
begin
  if InstallFailed and WasInstalled then
    Exec(ExpandConstant('{sys}\sc.exe'), 'start Kinodom', '', SW_HIDE, ewWaitUntilTerminated, Code);
end;

{ Удаление: вопрос о данных, затем kinodom.exe uninstall — пока файлы на месте. }
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Params: String;
  Code: Integer;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Params := 'uninstall';
  if not UninstallSilent and
     (MsgBox('Удалить также скачанное и настройки?', mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES) then
    Params := 'uninstall --purge';
  Exec(ExpandConstant('{app}\kinodom.exe'), Params, '', SW_HIDE, ewWaitUntilTerminated, Code);
end;
