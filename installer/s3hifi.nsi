; S3 Hi-Fi server — Windows installer (NSIS 3, build: makensis -DVERSION=x.y.z s3hifi.nsi)
; Puts s3hifi.exe into Program Files, starts the server with Windows (icon in the notification area),
; opens the firewall for the player and the phone remote. The music folder is chosen in the server
; itself (it runs as the user and sees mapped network drives, which this elevated installer does not).

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

!define MUI_WELCOMEPAGE_TEXT "Программа установит сервер ${APP}: он хранит музыку, раздаёт её плееру на ESP32-S3 и показывает пульт для телефона.$\r$\n$\r$\nСервер запускается вместе с Windows и живёт значком в трее (рядом с часами). Меню значка — по щелчку: пульт, выбор папки с музыкой, выход.$\r$\n$\r$\nПапку с музыкой сервер спросит сам при первом запуске."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY

!insertmacro MUI_PAGE_INSTFILES

!define MUI_FINISHPAGE_TEXT "Сервер установлен.$\r$\n$\r$\nПульт: http://<адрес этого компьютера>:8097 — откройте с телефона в той же сети. Плеер найдёт сервер сам.$\r$\n$\r$\nДля DTS, APE, WavPack, ALAC положите ffmpeg.exe и ffprobe.exe в папку программы."
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Запустить сервер (он спросит папку с музыкой)"
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
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

Function RunNow
  ; through explorer, so the server runs as the normal user, not elevated like the installer
  Exec '"$WINDIR\explorer.exe" "$SMPROGRAMS\${APP}\${APP}.lnk"'
FunctionEnd

Section "Install"
  SetShellVarContext all
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Sleep 500

  SetOutPath "$INSTDIR"
  File "build\${EXE}"
  File "..\LICENSE"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateDirectory "$SMPROGRAMS\${APP}"
  CreateShortCut "$SMPROGRAMS\${APP}\${APP}.lnk" "$INSTDIR\${EXE}" '-tray' "$INSTDIR\${EXE}" 0
  CreateShortCut "$SMPROGRAMS\${APP}\Удалить ${APP}.lnk" "$INSTDIR\uninstall.exe"
  ClearErrors
  CreateShortCut "$SMSTARTUP\${APP}.lnk" "$INSTDIR\${EXE}" '-tray' "$INSTDIR\${EXE}" 0
  ${If} ${Errors}
    ; some antiviruses (e.g. 360 Total Security) silently block new autostart entries
    MessageBox MB_ICONEXCLAMATION|MB_OK "Не удалось добавить сервер в автозагрузку — похоже, это заблокировал антивирус.$\r$\n$\r$\nСервер установлен и работает, но с Windows сам не запустится. Разрешите это в антивирусе и запустите установщик ещё раз, либо запускайте сервер из меню «Пуск» → ${APP}." /SD IDOK
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
  ; the music folder, settings, queue and cover cache stay in %USERPROFILE%\.s3hifi
SectionEnd
