@echo off
setlocal enabledelayedexpansion
REM install.bat - Instala o desinstala el COPIME Desktop Agent (CDA) en Windows.
REM
REM Uso:
REM   install.bat --server URL --enroll-code CODE
REM   install.bat --uninstall
REM
REM Debe ejecutarse desde una consola con privilegios de Administrador (requerido
REM para escribir en %ProgramFiles%, %ProgramData% y registrar el servicio en el
REM Service Control Manager). El script se detiene con un mensaje claro si no
REM corre elevado, en vez de fallar a medio camino con errores de permisos.

set "BINARY_NAME=cda.exe"
set "INSTALL_DIR=%ProgramFiles%\COPIME\copime-agent"
set "CONFIG_DIR=%ProgramData%\COPIME\copime-agent"

REM --- Verificar privilegios de Administrador ---
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo ERROR: Este script debe ejecutarse como Administrador. >&2
    echo Haz clic derecho sobre install.bat y selecciona "Ejecutar como administrador". >&2
    exit /b 1
)

REM --- Parsear argumentos ---
set "SERVER="
set "ENROLL_CODE="
set "UNINSTALL=0"

:parse_args
if "%~1"=="" goto args_done
if "%~1"=="--server" (
    set "SERVER=%~2"
    shift
    shift
    goto parse_args
)
if "%~1"=="--enroll-code" (
    set "ENROLL_CODE=%~2"
    shift
    shift
    goto parse_args
)
if "%~1"=="--uninstall" (
    set "UNINSTALL=1"
    shift
    goto parse_args
)
echo ERROR: Argumento desconocido: %~1 >&2
echo Uso: install.bat --server URL --enroll-code CODE >&2
echo      install.bat --uninstall >&2
exit /b 1

:args_done

REM --- Modo desinstalacion ---
if "%UNINSTALL%"=="1" (
    echo ==^> Desinstalando COPIME Desktop Agent...

    if exist "%INSTALL_DIR%\%BINARY_NAME%" (
        echo   Deteniendo y eliminando el servicio de Windows...
        "%INSTALL_DIR%\%BINARY_NAME%" --uninstall-service
    )

    if exist "%INSTALL_DIR%\%BINARY_NAME%" (
        del /f /q "%INSTALL_DIR%\%BINARY_NAME%"
        echo   Eliminado: %INSTALL_DIR%\%BINARY_NAME%
    )
    if exist "%INSTALL_DIR%" (
        rmdir "%INSTALL_DIR%" 2>nul
    )
    if exist "%CONFIG_DIR%" (
        rmdir /s /q "%CONFIG_DIR%"
        echo   Eliminado: %CONFIG_DIR%
    )

    echo ==^> Desinstalacion completa.
    exit /b 0
)

REM --- Modo instalacion ---
if "%SERVER%"=="" (
    set /p "SERVER=URL del servidor COPIME (ej. https://conasinf.copime.net): "
)
if "%ENROLL_CODE%"=="" (
    set /p "ENROLL_CODE=Codigo de enrolamiento: "
)

if "%SERVER%"=="" (
    echo ERROR: Se requieren --server y --enroll-code para la instalacion. >&2
    echo Uso: install.bat --server https://conasinf.copime.net --enroll-code XXXX-XXXX >&2
    exit /b 1
)
if "%ENROLL_CODE%"=="" (
    echo ERROR: Se requieren --server y --enroll-code para la instalacion. >&2
    echo Uso: install.bat --server https://conasinf.copime.net --enroll-code XXXX-XXXX >&2
    exit /b 1
)
set "SERVER_PREFIX=%SERVER:~0,4%"
if not "%SERVER_PREFIX%"=="http" (
    echo ERROR: La URL del servidor debe iniciar con http:// o https://. >&2
    exit /b 1
)

echo ==^> Instalando COPIME Desktop Agent...

set "SCRIPT_DIR=%~dp0"
set "BINARY_SRC=%SCRIPT_DIR%%BINARY_NAME%"

if not exist "%BINARY_SRC%" (
    echo ERROR: No se encontro el binario "%BINARY_SRC%". >&2
    echo Compila el agente primero: >&2
    echo   go build -o cda.exe .\cmd\agent\ >&2
    exit /b 1
)

REM 1. Instalar el binario.
echo   Instalando binario en %INSTALL_DIR%\%BINARY_NAME%...
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
copy /y "%BINARY_SRC%" "%INSTALL_DIR%\%BINARY_NAME%" >nul

REM 2. Crear directorio de configuracion.
echo   Creando directorio de configuracion %CONFIG_DIR%...
if not exist "%CONFIG_DIR%" mkdir "%CONFIG_DIR%"

REM 3. Ejecutar el enrolamiento (escribe config.json en CONFIG_DIR).
echo   Realizando enrolamiento con el servidor %SERVER%...
"%INSTALL_DIR%\%BINARY_NAME%" --server "%SERVER%" --enroll-code "%ENROLL_CODE%" --config "%CONFIG_DIR%\config.json"
if %errorlevel% neq 0 (
    echo ERROR: El enrolamiento fallo. Verifica el servidor y el codigo de enrolamiento. >&2
    exit /b 1
)

REM 4. Registrar e iniciar el servicio de Windows (SCM, inicio automatico).
echo   Registrando el servicio de Windows...
"%INSTALL_DIR%\%BINARY_NAME%" --install-service
if %errorlevel% neq 0 (
    echo ERROR: No se pudo registrar el servicio de Windows. >&2
    exit /b 1
)

echo.
echo ==^> COPIME Desktop Agent instalado y activo.
echo     Servicio: COPIMEDesktopAgent (ver services.msc)
echo     Config: %CONFIG_DIR%\config.json
echo     Para desinstalar: install.bat --uninstall

endlocal
