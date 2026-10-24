; S3 Hi-Fi server — Windows installer (NSIS 3, build: makensis -DVERSION=x.y.z s3hifi.nsi)
; Puts s3hifi.exe into Program Files, starts the server with Windows (icon in the notification area),
; opens the firewall for the player and the phone remote. The music folder is chosen in the server
; itself (it runs as the user and sees mapped network drives, which this elevated installer does not).
; The language follows Windows: Russian on a Russian system, English on any other.
; ffmpeg (optional, ticked by default) is downloaded by get-ffmpeg.ps1 into the program folder.

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

!ifndef VERSION
  !define VERSION "dev"
!endif
!define APP      "S3 Hi-Fi"
!define EXE      "s3hifi.exe"
!define REGKEY   "Software\S3HiFi"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\S3HiFi"

Name "${APP} ${VERSION}"
OutFile "s3hifi-setup-${VERSION}.exe"
InstallDir "$PROGRAMFILES64\S3 Hi-Fi"
InstallDirRegKey HKLM "${REGKEY}" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma

!define MUI_ICON   "..\server\winres\s3hifi.ico"
!define MUI_UNICON "..\server\winres\s3hifi.ico"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TEXT "$(T_WELCOME)"
!insertmacro MUI_PAGE_WELCOME
!define MUI_COMPONENTSPAGE_TEXT_TOP "$(T_COMP_TOP)"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY

!insertmacro MUI_PAGE_INSTFILES

!define MUI_FINISHPAGE_TEXT "$(T_FINISH)"
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "$(T_RUN)"
!define MUI_FINISHPAGE_RUN_FUNCTION RunNow
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; English first: it is the language for any Windows that is not Russian
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "Russian"

LangString T_WELCOME ${LANG_ENGLISH} "This installs the ${APP} server: it keeps the music, streams it to the ESP32-S3 player and serves the remote for your phone.$\r$\n$\r$\nThe server starts with Windows and lives as an icon in the notification area (next to the clock). Click the icon for the menu: the remote, choosing the music folder, exit.$\r$\n$\r$\nThe server asks for the music folder on its first start."
LangString T_WELCOME ${LANG_RUSSIAN} "Программа установит сервер ${APP}: он хранит музыку, раздаёт её плееру на ESP32-S3 и показывает пульт для телефона.$\r$\n$\r$\nСервер запускается вместе с Windows и живёт значком в трее (рядом с часами). Меню значка — по щелчку: пульт, выбор папки с музыкой, выход.$\r$\n$\r$\nПапку с музыкой сервер спросит сам при первом запуске."
LangString T_COMP_TOP ${LANG_ENGLISH} "ffmpeg is recommended. It is needed for Hi-Res files above 96 kHz (24/192, 24/176.4): the server converts them to 96 / 88.2 kHz, otherwise the player's Wi-Fi cannot keep up and the sound breaks up. It also plays DTS, APE, WavPack, ALAC, M4A and OGG. About 80 MB is downloaded. Without it FLAC, WAV, MP3 and DSD up to 96 kHz still play."
LangString T_COMP_TOP ${LANG_RUSSIAN} "ffmpeg рекомендуется. Он нужен для файлов Hi-Res выше 96 кГц (24/192, 24/176,4): сервер понижает их до 96 / 88,2 кГц, иначе плееру не хватит скорости Wi-Fi и звук будет прерываться. Ещё с ним играют DTS, APE, WavPack, ALAC, M4A и OGG. Скачивается около 80 МБ. Без него FLAC, WAV, MP3 и DSD до 96 кГц всё равно играют."
LangString T_SEC_MAIN ${LANG_ENGLISH} "S3 Hi-Fi server"
LangString T_SEC_MAIN ${LANG_RUSSIAN} "Сервер S3 Hi-Fi"
LangString T_SEC_FF ${LANG_ENGLISH} "ffmpeg (Hi-Res above 96 kHz, DTS, APE, ALAC)"
LangString T_SEC_FF ${LANG_RUSSIAN} "ffmpeg (Hi-Res выше 96 кГц, DTS, APE, ALAC)"
LangString T_DESC_MAIN ${LANG_ENGLISH} "The server itself, the tray icon, autostart and the firewall rule."
LangString T_DESC_MAIN ${LANG_RUSSIAN} "Сам сервер, значок в трее, автозапуск и правило брандмауэра."
LangString T_DESC_FF ${LANG_ENGLISH} "Downloads an LGPL build of ffmpeg (about 80 MB) into the program folder."
LangString T_DESC_FF ${LANG_RUSSIAN} "Скачает LGPL-сборку ffmpeg (около 80 МБ) в папку программы."
LangString T_FF_DL ${LANG_ENGLISH} "Downloading ffmpeg (about 80 MB), this can take a few minutes..."
LangString T_FF_DL ${LANG_RUSSIAN} "Скачиваю ffmpeg (около 80 МБ), это может занять несколько минут..."
LangString T_FF_HAVE ${LANG_ENGLISH} "ffmpeg is already in the program folder."
LangString T_FF_HAVE ${LANG_RUSSIAN} "ffmpeg уже есть в папке программы."
LangString T_FF_FAIL ${LANG_ENGLISH} "Could not download or unpack ffmpeg (no internet, or an antivirus blocked it). Details: $INSTDIR\ffmpeg-install.log$\r$\n$\r$\nThe server is installed and plays FLAC, WAV, MP3 and DSD up to 96 kHz. For files above 96 kHz run the installer again later, or put ffmpeg.exe and ffprobe.exe into the program folder yourself."
LangString T_FF_FAIL ${LANG_RUSSIAN} "Не удалось скачать или распаковать ffmpeg (нет интернета или помешал антивирус). Подробности: $INSTDIR\ffmpeg-install.log$\r$\n$\r$\nСервер установлен и играет FLAC, WAV, MP3 и DSD до 96 кГц. Для файлов выше 96 кГц запустите установщик ещё раз позже или положите ffmpeg.exe и ffprobe.exe в папку программы сами."
LangString T_FINISH ${LANG_ENGLISH} "The server is installed.$\r$\n$\r$\nRemote: http://<this computer's address>:8097 — open it on a phone in the same network. The player finds the server by itself."
LangString T_FINISH ${LANG_RUSSIAN} "Сервер установлен.$\r$\n$\r$\nПульт: http://<адрес этого компьютера>:8097 — откройте с телефона в той же сети. Плеер найдёт сервер сам."
LangString T_RUN ${LANG_ENGLISH} "Start the server (it asks for the music folder)"
LangString T_RUN ${LANG_RUSSIAN} "Запустить сервер (он спросит папку с музыкой)"
LangString T_UNINST ${LANG_ENGLISH} "Uninstall ${APP}"
LangString T_UNINST ${LANG_RUSSIAN} "Удалить ${APP}"
LangString T_AUTOSTART ${LANG_ENGLISH} "Could not add the server to autostart — an antivirus seems to have blocked it.$\r$\n$\r$\nThe server is installed and works, but will not start with Windows by itself. Allow it in the antivirus and run the installer again, or start the server from Start → ${APP}."
LangString T_AUTOSTART ${LANG_RUSSIAN} "Не удалось добавить сервер в автозагрузку — похоже, это заблокировал антивирус.$\r$\n$\r$\nСервер установлен и работает, но с Windows сам не запустится. Разрешите это в антивирусе и запустите установщик ещё раз, либо запускайте сервер из меню «Пуск» → ${APP}."

VIProductVersion "0.0.0.0"
VIAddVersionKey /LANG=${LANG_ENGLISH} "ProductName" "${APP}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "FileDescription" "${APP} server setup"
VIAddVersionKey /LANG=${LANG_ENGLISH} "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_ENGLISH} "LegalCopyright" "MIT License, github.com/michkar-ekb/esp32-hifi-player"

Function .onInit
  SetRegView 64
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

Function RunNow
  ; through explorer, so the server runs as the normal user, not elevated like the installer
  Exec '"$WINDIR\explorer.exe" "$SMPROGRAMS\${APP}\${APP}.lnk"'
FunctionEnd

Section "!$(T_SEC_MAIN)" SecMain
  SectionIn RO
  SetShellVarContext all
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Sleep 500

  SetOutPath "$INSTDIR"
  File "build\${EXE}"
  File "..\LICENSE"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateDirectory "$SMPROGRAMS\${APP}"
  CreateShortCut "$SMPROGRAMS\${APP}\${APP}.lnk" "$INSTDIR\${EXE}" '-tray' "$INSTDIR\${EXE}" 0
  Delete "$SMPROGRAMS\${APP}\Удалить ${APP}.lnk"   ; the name before 0.9
  CreateShortCut "$SMPROGRAMS\${APP}\$(T_UNINST).lnk" "$INSTDIR\uninstall.exe"
  ClearErrors
  CreateShortCut "$SMSTARTUP\${APP}.lnk" "$INSTDIR\${EXE}" '-tray' "$INSTDIR\${EXE}" 0
  ${If} ${Errors}
    ; some antiviruses (e.g. 360 Total Security) silently block new autostart entries
    MessageBox MB_ICONEXCLAMATION|MB_OK "$(T_AUTOSTART)" /SD IDOK
  ${EndIf}

  ; the player finds the server by UDP broadcast, the phone opens TCP 8097: allow the program
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${APP}"'
  nsExec::Exec 'netsh advfirewall firewall add rule name="${APP}" dir=in action=allow program="$INSTDIR\${EXE}" enable=yes profile=private,domain'

  WriteRegStr HKLM "${REGKEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${APP} server"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "michkar-ekb"
  WriteRegStr HKLM "${UNINSTKEY}" "URLInfoAbout" "https://github.com/michkar-ekb/esp32-hifi-player"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
SectionEnd

Section "$(T_SEC_FF)" SecFF
  ${If} ${FileExists} "$INSTDIR\ffmpeg.exe"
  ${AndIf} ${FileExists} "$INSTDIR\ffprobe.exe"
    DetailPrint "$(T_FF_HAVE)"
    Return
  ${EndIf}
  InitPluginsDir
  File "/oname=$PLUGINSDIR\get-ffmpeg.ps1" "get-ffmpeg.ps1"
  DetailPrint "$(T_FF_DL)"
  ; the installer is 32-bit: Sysnative reaches the 64-bit PowerShell (and its curl.exe)
  StrCpy $1 "$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe"
  ${IfNot} ${FileExists} "$1"
    StrCpy $1 "powershell.exe"
  ${EndIf}
  nsExec::ExecToLog '"$1" -NoProfile -ExecutionPolicy Bypass -File "$PLUGINSDIR\get-ffmpeg.ps1" -Dest "$INSTDIR"'
  Pop $0
  ${If} $0 != 0
  ${OrIfNot} ${FileExists} "$INSTDIR\ffmpeg.exe"
    MessageBox MB_ICONEXCLAMATION|MB_OK "$(T_FF_FAIL)" /SD IDOK
  ${EndIf}
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} "$(T_DESC_MAIN)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecFF} "$(T_DESC_FF)"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  SetShellVarContext all
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Sleep 500
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${APP}"'
  Delete "$SMSTARTUP\${APP}.lnk"
  RMDir /r "$SMPROGRAMS\${APP}"
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\ffmpeg.exe"
  Delete "$INSTDIR\ffprobe.exe"
  Delete "$INSTDIR\*.dll"
  Delete "$INSTDIR\ffmpeg-LICENSE.txt"
  Delete "$INSTDIR\ffmpeg-install.log"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "${REGKEY}"
  DeleteRegKey HKLM "${UNINSTKEY}"
  ; the music folder, settings, queue and cover cache stay in %USERPROFILE%\.s3hifi
SectionEnd
