@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
cd /d "%~dp0"
title S3 Hi-Fi - прошивка плеера

set "FW=s3hifi-player-esp32s3-n16r8.bin"

cls
echo ============================================================
echo   S3 Hi-Fi: прошивка плеера (ESP32-S3 N16R8)
echo ============================================================
echo.
echo  1. Подключите плату к ПК кабелем USB-C с ДАННЫМИ.
echo     У платы два разъёма USB-C: для прошивки берите тот,
echo     что подписан COM (или UART).
echo  2. Подождите, пока Windows найдёт устройство.
echo.
echo  Настройки Wi-Fi СБРОСЯТСЯ: после прошивки настройте плеер
echo  заново через Wi-Fi «S3 Hi-Fi Setup».
echo  Сломать нельзя: при ошибке просто запустите файл ещё раз.
echo ============================================================
echo.

if not exist "%~dp0esptool.exe" (
  echo [ОШИБКА] Нет esptool.exe рядом с этим файлом. Распакуйте архив целиком.
  echo.
  pause
  exit /b 1
)
if not exist "%~dp0%FW%" (
  echo [ОШИБКА] Нет файла прошивки %FW% рядом с этим файлом.
  echo.
  pause
  exit /b 1
)

:ASK
echo Сейчас в системе видны такие COM-порты:
echo ------------------------------------------------------------
powershell -NoProfile -Command "$p=Get-CimInstance Win32_PnPEntity | Where-Object { $_.Name -match '\(COM\d+\)' } | Sort-Object Name; if($p){ $p | ForEach-Object { '  ' + $_.Name } } else { '  (портов не видно - проверьте кабель и драйвер CH343/CP210x)' }"
echo ------------------------------------------------------------
echo.
echo Впишите номер порта (например 5 или COM5) и нажмите Enter.
echo Если порт один - можно просто нажать Enter.
echo.
set "PORT=%~1"
if "%PORT%"=="" set /p "PORT=Порт: "

set "PORTARG="
if not "%PORT%"=="" (
  set "P=%PORT: =%"
  if /i "!P:~0,3!"=="COM" (set "PORTARG=--port !P!") else (set "PORTARG=--port COM!P!")
)

echo.
if "%PORTARG%"=="" (echo --- Ищу плату сам, записываю прошивку...) else (echo --- %PORTARG:~7%: записываю прошивку...)
echo.

"%~dp0esptool.exe" --chip esp32s3 %PORTARG% --baud 921600 write-flash 0x0 "%~dp0%FW%"
if not errorlevel 1 goto OK

echo.
echo --- Не вышло на быстрой скорости, пробую медленнее (115200)...
echo.
"%~dp0esptool.exe" --chip esp32s3 %PORTARG% --baud 115200 write-flash 0x0 "%~dp0%FW%"
if not errorlevel 1 goto OK

echo.
echo ============================================================
echo   НЕ ПОЛУЧИЛОСЬ. Что попробовать:
echo   - другой кабель (часто кабель только для зарядки);
echo   - второй разъём USB-C на плате;
echo   - зажать на плате кнопку BOOT, нажать и отпустить RESET,
echo     отпустить BOOT - и запустить этот файл снова;
echo   - закрыть другие программы, которые держат порт
echo     (Arduino IDE, монитор порта).
echo ============================================================
echo.
set "PORT="
choice /c YN /m "Попробовать ещё раз"
if errorlevel 2 exit /b 1
cls
goto ASK

:OK
echo.
echo ============================================================
echo   ГОТОВО. Плата перезапустится сама.
echo   Плеер поднимет точку доступа «S3 Hi-Fi Setup»:
echo   подключитесь к ней с телефона и выберите свой Wi-Fi.
echo.
echo   ВАЖНО: чтобы слушать музыку с компьютера (USB-ЦАП),
echo   ПЕРЕТКНИТЕ КАБЕЛЬ В ДРУГОЕ ГНЕЗДО - подписанное USB.
echo   Через гнездо COM компьютер звуковую карту не увидит.
echo.
echo   Дальше прошивку можно обновлять по Wi-Fi, без кабеля.
echo ============================================================
echo.
pause
exit /b 0
