@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
cd /d "%~dp0"

set "FW=s3hifi-player-esp32s3-n16r8.bin"

rem Language: Russian on a Russian Windows, English everywhere else
set "UILANG=en"
for /f %%L in ('powershell -NoProfile -Command "if ((Get-UICulture).TwoLetterISOLanguageName -eq 'ru' -or (Get-Culture).TwoLetterISOLanguageName -eq 'ru') { 'ru' } else { 'en' }" 2^>nul') do set "UILANG=%%L"
if /i "%UILANG%"=="ru" goto TEXT_RU

set "T_TITLE=S3 Hi-Fi - player flashing"
set "T_HEAD=  S3 Hi-Fi: player flashing (ESP32-S3 N16R8)"
set "T_I1= 1. Connect the board to the PC with a USB-C DATA cable."
set "T_I2=    The board has two USB-C ports: for flashing use the one"
set "T_I3=    marked COM (or UART)."
set "T_I4= 2. Wait until Windows finds the device."
set "T_W1= Wi-Fi settings WILL BE ERASED: after flashing set the player up"
set "T_W2= again through the «S3 Hi-Fi Setup» Wi-Fi."
set "T_W3= You can't break anything: if it fails, just run this file again."
set "T_NOESP=[ERROR] esptool.exe is missing next to this file. Unpack the whole archive."
set "T_NOFW=[ERROR] The firmware file %FW% is missing next to this file."
set "T_PORTS=COM ports seen by the system right now:"
set "T_NOPORTS=  (no ports - check the cable and the CH343/CP210x driver)"
set "T_ASK1=Type the port number (for example 5 or COM5) and press Enter."
set "T_ASK2=If there is only one port, just press Enter."
set "T_PORT=Port: "
set "T_AUTO=--- Looking for the board, writing the firmware..."
set "T_WRITE=: writing the firmware..."
set "T_SLOW=--- Didn't work at full speed, trying slower (115200)..."
set "T_F1=  IT DIDN'T WORK. What to try:"
set "T_F2=  - another cable (often a cable is for charging only);"
set "T_F3=  - the other USB-C port on the board;"
set "T_F4=  - hold the BOOT button on the board, press and release RESET,"
set "T_F5=    release BOOT - and run this file again;"
set "T_F6=  - close other programs that hold the port"
set "T_F7=    (Arduino IDE, serial monitor)."
set "T_RETRY=Try again"
set "T_OK1=  DONE. The board restarts by itself."
set "T_OK2=  The player brings up the «S3 Hi-Fi Setup» access point:"
set "T_OK3=  connect to it from a phone and choose your Wi-Fi."
set "T_OK4=  IMPORTANT: to play music from the computer (USB DAC),"
set "T_OK5=  MOVE THE CABLE TO THE OTHER PORT - the one marked USB."
set "T_OK6=  Through the COM port the computer won't see a sound card."
set "T_OK7=  From now on the firmware can be updated over Wi-Fi, no cable."
goto TEXT_DONE

:TEXT_RU
set "T_TITLE=S3 Hi-Fi - прошивка плеера"
set "T_HEAD=  S3 Hi-Fi: прошивка плеера (ESP32-S3 N16R8)"
set "T_I1= 1. Подключите плату к ПК кабелем USB-C с ДАННЫМИ."
set "T_I2=    У платы два разъёма USB-C: для прошивки берите тот,"
set "T_I3=    что подписан COM (или UART)."
set "T_I4= 2. Подождите, пока Windows найдёт устройство."
set "T_W1= Настройки Wi-Fi СБРОСЯТСЯ: после прошивки настройте плеер"
set "T_W2= заново через Wi-Fi «S3 Hi-Fi Setup»."
set "T_W3= Сломать нельзя: при ошибке просто запустите файл ещё раз."
set "T_NOESP=[ОШИБКА] Нет esptool.exe рядом с этим файлом. Распакуйте архив целиком."
set "T_NOFW=[ОШИБКА] Нет файла прошивки %FW% рядом с этим файлом."
set "T_PORTS=Сейчас в системе видны такие COM-порты:"
set "T_NOPORTS=  (портов не видно - проверьте кабель и драйвер CH343/CP210x)"
set "T_ASK1=Впишите номер порта (например 5 или COM5) и нажмите Enter."
set "T_ASK2=Если порт один - можно просто нажать Enter."
set "T_PORT=Порт: "
set "T_AUTO=--- Ищу плату сам, записываю прошивку..."
set "T_WRITE=: записываю прошивку..."
set "T_SLOW=--- Не вышло на быстрой скорости, пробую медленнее (115200)..."
set "T_F1=  НЕ ПОЛУЧИЛОСЬ. Что попробовать:"
set "T_F2=  - другой кабель (часто кабель только для зарядки);"
set "T_F3=  - второй разъём USB-C на плате;"
set "T_F4=  - зажать на плате кнопку BOOT, нажать и отпустить RESET,"
set "T_F5=    отпустить BOOT - и запустить этот файл снова;"
set "T_F6=  - закрыть другие программы, которые держат порт"
set "T_F7=    (Arduino IDE, монитор порта)."
set "T_RETRY=Попробовать ещё раз"
set "T_OK1=  ГОТОВО. Плата перезапустится сама."
set "T_OK2=  Плеер поднимет точку доступа «S3 Hi-Fi Setup»:"
set "T_OK3=  подключитесь к ней с телефона и выберите свой Wi-Fi."
set "T_OK4=  ВАЖНО: чтобы слушать музыку с компьютера (USB-ЦАП),"
set "T_OK5=  ПЕРЕТКНИТЕ КАБЕЛЬ В ДРУГОЕ ГНЕЗДО - подписанное USB."
set "T_OK6=  Через гнездо COM компьютер звуковую карту не увидит."
set "T_OK7=  Дальше прошивку можно обновлять по Wi-Fi, без кабеля."

:TEXT_DONE
title !T_TITLE!

cls
echo ============================================================
echo !T_HEAD!
echo ============================================================
echo.
echo !T_I1!
echo !T_I2!
echo !T_I3!
echo !T_I4!
echo.
echo !T_W1!
echo !T_W2!
echo !T_W3!
echo ============================================================
echo.

if not exist "%~dp0esptool.exe" (
  echo !T_NOESP!
  echo.
  pause
  exit /b 1
)
if not exist "%~dp0%FW%" (
  echo !T_NOFW!
  echo.
  pause
  exit /b 1
)

:ASK
echo !T_PORTS!
echo ------------------------------------------------------------
powershell -NoProfile -Command "$p=Get-CimInstance Win32_PnPEntity | Where-Object { $_.Name -match '\(COM\d+\)' } | Sort-Object Name; if($p){ $p | ForEach-Object { '  ' + $_.Name } } else { $env:T_NOPORTS }"
echo ------------------------------------------------------------
echo.
echo !T_ASK1!
echo !T_ASK2!
echo.
set "PORT=%~1"
if "%PORT%"=="" set /p "PORT=!T_PORT!"

set "PORTARG="
if not "%PORT%"=="" (
  set "P=%PORT: =%"
  if /i "!P:~0,3!"=="COM" (set "PORTARG=--port !P!") else (set "PORTARG=--port COM!P!")
)

echo.
if "%PORTARG%"=="" (echo !T_AUTO!) else (echo --- %PORTARG:~7%!T_WRITE!)
echo.

"%~dp0esptool.exe" --chip esp32s3 %PORTARG% --baud 921600 write-flash 0x0 "%~dp0%FW%"
if not errorlevel 1 goto OK

echo.
echo !T_SLOW!
echo.
"%~dp0esptool.exe" --chip esp32s3 %PORTARG% --baud 115200 write-flash 0x0 "%~dp0%FW%"
if not errorlevel 1 goto OK

echo.
echo ============================================================
echo !T_F1!
echo !T_F2!
echo !T_F3!
echo !T_F4!
echo !T_F5!
echo !T_F6!
echo !T_F7!
echo ============================================================
echo.
set "PORT="
choice /c YN /m "!T_RETRY!"
if errorlevel 2 exit /b 1
cls
goto ASK

:OK
echo.
echo ============================================================
echo !T_OK1!
echo !T_OK2!
echo !T_OK3!
echo.
echo !T_OK4!
echo !T_OK5!
echo !T_OK6!
echo.
echo !T_OK7!
echo ============================================================
echo.
pause
exit /b 0
