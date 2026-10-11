; S3 Hi-Fi server — Windows installer (NSIS 3, build: makensis -DVERSION=x.y.z s3hifi.nsi)
; Puts s3hifi.exe into Program Files, asks for the music folder, starts the server with Windows
; (icon in the notification area), opens the firewall for the player and the phone remote.

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

Var MusicDir

!define MUI_ICON   "..\server\winres\s3hifi.ico"
!define MUI_UNICON "..\server\winres\s3hifi.ico"
!define MUI_ABORTWARNING

!define MUI_WELCOMEPAGE_TEXT "Программа установит сервер ${APP}: он хранит музыку, раздаёт её плееру на ESP32-S3 и показывает пульт для телефона.$\r$\n$\r$\nСервер запускается вместе с Windows и живёт значком в трее (рядом с часами). Выключить — правой кнопкой по значку → «Выход»."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY

; second directory page: the music folder
!define MUI_PAGE_HEADER_TEXT "Папка с музыкой"
!define MUI_PAGE_HEADER_SUBTEXT "Где лежат ваши альбомы (FLAC, MP3, DSD, APE, CUE…)."
!define MUI_DIRECTORYPAGE_TEXT_TOP "Сервер покажет в пульте все папки и альбомы из этой папки. Сетевой диск тоже подойдёт. Поменять потом — запустите установщик ещё раз."
!define MUI_DIRECTORYPAGE_TEXT_DESTINATION "Папка с музыкой"
!define MUI_DIRECTORYPAGE_VARIABLE $MusicDir
!define MUI_DIRECTORYPAGE_VERIFYONEXIT
!insertmacro MUI_PAGE_DIRECTORY

!insertmacro MUI_PAGE_INSTFILES

!define MUI_FINISHPAGE_TEXT "Сервер установлен.$\r$\n$\r$\nПульт: http://<адрес этого компьютера>:8097 — откройте с телефона в той же сети. Плеер найдёт сервер сам.$\r$\n$\r$\nДля DTS, APE, WavPack, ALAC положите ffmpeg.exe и ffprobe.exe в папку программы."
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Запустить сервер и открыть пульт"
!define MUI_FINISHPAGE_RUN_FUNCTION RunNow
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "Russian"

VIProductVersion "0.0.0.0"
VIAddVersionKey /LANG=${LANG_RUSSIAN} "ProductName" "${APP}"
VIAddVersionKey /LANG=${LANG_RUSSIAN} "FileDescription" "${APP} server setup"
VIAddVersionKey /LANG=${LANG_RUSSIAN} "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_RUSSIAN} "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=${LANG_RUSSIAN} "LegalCopyright" "GPL-3.0, github.com/michkar-ekb/esp32-hifi-player"

Function .onInit
  SetRegView 64
  ReadRegStr $MusicDir HKLM "${REGKEY}" "MusicDir"
  ${If} $MusicDir == ""
    StrCpy $MusicDir "$PROFILE\Music"
  ${EndIf}
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

; command-line arguments for the server; a root like C:\ must not end with \ before the quote
Function MusicArg
  StrCpy $0 $MusicDir 1 -1
  ${If} $0 == "\"
    StrCpy $0 "$MusicDir."
  ${Else}
    StrCpy $0 $MusicDir
  ${EndIf}
FunctionEnd

Function RunNow
  ; through explorer, so the server runs as the normal user, not elevated like the installer
  Exec '"$WINDIR\explorer.exe" "$SMPROGRAMS\${APP}\${APP}.lnk"'
  Sleep 2000
  ExecShell "open" "http://localhost:8097/"
FunctionEnd

Section "Install"
  SetShellVarContext all
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Sleep 500

  SetOutPath "$INSTDIR"
  File "build\${EXE}"
  File "..\LICENSE"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  Call MusicArg
  CreateDirectory "$SMPROGRAMS\${APP}"
  CreateShortCut "$SMPROGRAMS\${APP}\${APP}.lnk" "$INSTDIR\${EXE}" '-tray -music "$0"' "$INSTDIR\${EXE}" 0
  CreateShortCut "$SMPROGRAMS\${APP}\Удалить ${APP}.lnk" "$INSTDIR\uninstall.exe"
  ClearErrors
  CreateShortCut "$SMSTARTUP\${APP}.lnk" "$INSTDIR\${EXE}" '-tray -music "$0"' "$INSTDIR\${EXE}" 0
  ${If} ${Errors}
    ; some antiviruses (e.g. 360 Total Security) silently block new autostart entries
    MessageBox MB_ICONEXCLAMATION|MB_OK "Не удалось добавить сервер в автозагрузку — похоже, это заблокировал антивирус.$\r$\n$\r$\nСервер установлен и работает, но с Windows сам не запустится. Разрешите это в антивирусе и запустите установщик ещё раз, либо запускайте сервер из меню «Пуск» → ${APP}." /SD IDOK
  ${EndIf}

  ; the player finds the server by UDP broadcast, the phone opens TCP 8097: allow the program
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${APP}"'
  nsExec::Exec 'netsh advfirewall firewall add rule name="${APP}" dir=in action=allow program="$INSTDIR\${EXE}" enable=yes profile=private,domain'

  WriteRegStr HKLM "${REGKEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${REGKEY}" "MusicDir" "$MusicDir"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${APP} server"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "michkar-ekb"
  WriteRegStr HKLM "${UNINSTKEY}" "URLInfoAbout" "https://github.com/michkar-ekb/esp32-hifi-player"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext all
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Sleep 500
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${APP}"'
  Delete "$SMSTARTUP\${APP}.lnk"
  RMDir /r "$SMPROGRAMS\${APP}"
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "${REGKEY}"
  DeleteRegKey HKLM "${UNINSTKEY}"
  ; settings, queue and cover cache stay in %USERPROFILE%\.s3hifi
SectionEnd
