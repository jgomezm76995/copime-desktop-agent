//go:build windows

// Package inventory — implementación Windows del Collector.
//
// Decisión de diseño (documentada porque no hay una única forma "obvia" de hablar
// WMI desde Go sin cgo):
//
//   - Hardware/BIOS/SO/CPU/RAM/disco/red: se invoca `powershell.exe -Command
//     Get-CimInstance ... | ConvertTo-Json` como subproceso (mismo patrón que
//     darwin.go usa `system_profiler`/`sysctl` vía exec.Command). Se descartó una
//     librería WMI de terceros porque este repo no tiene dependencias externas de
//     ese tipo y PowerShell (Get-CimInstance, el cmdlet moderno sobre WinRM/DCOM que
//     reemplazó a Get-WmiObject) está presente por defecto en todas las versiones de
//     Windows soportadas (10/11, Server 2016+), incluido el runner windows-latest de
//     GitHub Actions.
//   - Software instalado: se lee directamente el registro
//     HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall (y su variante
//     WOW6432Node para apps de 32 bits en un Windows de 64 bits) vía
//     golang.org/x/sys/windows/registry — el mismo enfoque que usa GLPI Agent (al
//     que este proyecto reemplaza) y la mayoría de agentes de inventario reales.
//     Se descartó Win32_Product: WMI documenta que recorrerlo dispara una
//     reparación/revalidación de cada paquete MSI instalado (costoso y con efectos
//     secundarios reales sobre el sistema), además de ser mucho más lento que leer
//     el registro directamente.
package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// WindowsCollector implementa la interfaz Collector para Windows.
type WindowsCollector struct{}

// NuevoCollector crea un WindowsCollector listo para usar.
func NuevoCollector() Collector {
	return &WindowsCollector{}
}

// ObtenerDeviceID retorna un identificador de hardware estable entre reinicios
// (equivalente Windows del IOPlatformUUID que usa darwin.go).
//
// Orden de respaldo (cada eslabón solo se intenta si el anterior no entregó un
// valor usable):
//
//  1. Win32_ComputerSystemProduct.UUID (SMBIOS, vía WMI): primera opción porque
//     identifica el hardware/VM real, pero algunos hipervisores — VirtualBox es
//     el caso confirmado en campo, a diferencia de VMware/Hyper-V/Parallels —
//     no siempre exponen este valor SMBIOS al sistema invitado, y entonces WMI
//     retorna una cadena vacía (no un error).
//  2. MachineGuid del registro (HKLM\SOFTWARE\Microsoft\Cryptography): lo genera
//     el propio Windows al instalarse, no depende de SMBIOS/hipervisor, es
//     legible sin privilegios de administrador y es el identificador que usan
//     varios agentes de inventario reales exactamente por este motivo. Esa
//     clave de registro, a diferencia de la mayoría de HKLM\SOFTWARE, no se
//     redirige a WOW6432Node — se lee igual en 32 y 64 bits.
//  3. Win32_BIOS.SerialNumber, descartando los valores genéricos que varios
//     fabricantes/hipervisores dejan sin configurar (ver
//     esNumeroSerieBIOSGenerico).
//
// Si ningún eslabón entrega un valor usable, se falla con un error explícito en
// vez de inventar un identificador: generar y persistir uno localmente exigiría
// ampliar el contrato de Collector (hoy sin parámetros) y tocar config.go/
// main.go/darwin.go para abrir una ruta de persistencia compartida — fuera del
// alcance de este fix puntual de Windows, y no justificado sin evidencia de que
// el caso "ningún identificador real disponible" ocurra en la práctica (los tres
// eslabones de arriba ya cubren el caso de campo reportado).
func (w *WindowsCollector) ObtenerDeviceID() (string, error) {
	if uuid, err := obtenerUUIDComputerSystemProduct(); err != nil {
		log.Printf("[ADVERTENCIA] No se pudo consultar Win32_ComputerSystemProduct.UUID: %v", err)
	} else if uuid != "" {
		return uuid, nil
	} else {
		log.Printf("[ADVERTENCIA] Win32_ComputerSystemProduct.UUID vacío (frecuente en VirtualBox); usando respaldo.")
	}

	if guid, err := obtenerMachineGUID(); err != nil {
		log.Printf("[ADVERTENCIA] No se pudo leer MachineGuid del registro: %v", err)
	} else if guid != "" {
		return guid, nil
	}

	if serial, err := obtenerNumeroSerieBIOS(); err != nil {
		log.Printf("[ADVERTENCIA] No se pudo consultar Win32_BIOS.SerialNumber: %v", err)
	} else if serial != "" {
		return serial, nil
	}

	return "", fmt.Errorf("no se encontró ningún identificador de hardware estable (UUID, MachineGuid ni número de serie de BIOS)")
}

// obtenerUUIDComputerSystemProduct consulta el UUID SMBIOS vía WMI. Retorna
// cadena vacía sin error cuando WMI respondió pero el campo viene vacío (caso de
// campo en VirtualBox): eso no es un fallo de la consulta, es la ausencia del
// dato, y el llamador decide el respaldo.
func obtenerUUIDComputerSystemProduct() (string, error) {
	uuid, err := ejecutarPowerShellTexto(
		`(Get-CimInstance Win32_ComputerSystemProduct).UUID`, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("obtener UUID de Win32_ComputerSystemProduct: %w", err)
	}
	return strings.TrimSpace(uuid), nil
}

// obtenerMachineGUID lee HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid, un
// identificador generado por Windows al instalarse (no por el hipervisor), con
// el mismo mecanismo de registro que ya usa recolectarSoftware() en este archivo.
func obtenerMachineGUID() (string, error) {
	clave, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.READ)
	if err != nil {
		return "", fmt.Errorf("abrir HKLM\\SOFTWARE\\Microsoft\\Cryptography: %w", err)
	}
	defer clave.Close()

	guid, _, err := clave.GetStringValue("MachineGuid")
	if err != nil {
		return "", fmt.Errorf("leer MachineGuid: %w", err)
	}
	return strings.TrimSpace(guid), nil
}

// obtenerNumeroSerieBIOS consulta Win32_BIOS.SerialNumber y descarta los
// valores genéricos conocidos (ver esNumeroSerieBIOSGenerico), retornando cadena
// vacía sin error en ese caso para que el llamador falle con un mensaje claro en
// vez de adoptar un identificador inútil que colisionaría entre equipos distintos.
func obtenerNumeroSerieBIOS() (string, error) {
	serial, err := ejecutarPowerShellTexto(
		`(Get-CimInstance Win32_BIOS).SerialNumber`, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("obtener Win32_BIOS.SerialNumber: %w", err)
	}
	serial = strings.TrimSpace(serial)
	if esNumeroSerieBIOSGenerico(serial) {
		return "", nil
	}
	return serial, nil
}

// numerosSerieBIOSGenericos son los valores de relleno que varios fabricantes y
// virtualizadores dejan en Win32_BIOS.SerialNumber cuando no configuran un
// número de serie real — adoptarlos como DeviceID haría que equipos distintos
// con el mismo valor de relleno colisionen en el servidor.
var numerosSerieBIOSGenericos = map[string]bool{
	"":                       true,
	"0":                      true,
	"system serial number":   true,
	"default string":         true,
	"not specified":          true,
	"to be filled by o.e.m.": true,
	"none":                   true,
	"n/a":                    true,
	"serial number":          true,
	"invalid":                true,
	"0123456789":             true,
	"123456789":              true,
}

func esNumeroSerieBIOSGenerico(serial string) bool {
	return numerosSerieBIOSGenericos[strings.ToLower(strings.TrimSpace(serial))]
}

// RecolectarInventario recopila todos los datos del sistema Windows.
// La recolección de software (lectura de registro) corre en paralelo con la
// consulta WMI combinada, igual que darwin.go hace con system_profiler.
func (w *WindowsCollector) RecolectarInventario() (*Inventario, error) {
	var wg sync.WaitGroup
	var softwares []Software
	var errSoftware error

	wg.Add(1)
	go func() {
		defer wg.Done()
		softwares, errSoftware = recolectarSoftware()
	}()

	datos, err := recolectarDatosWMI()
	if err != nil {
		return nil, fmt.Errorf("recolectar datos WMI: %w", err)
	}

	wg.Wait()
	if errSoftware != nil {
		log.Printf("[ADVERTENCIA] No se pudo recolectar software instalado: %v", errSoftware)
	}

	liveMetrics, errMetricas := w.RecolectarMetricasVivo()
	if errMetricas != nil {
		log.Printf("[ADVERTENCIA] No se pudo recolectar métricas en vivo: %v", errMetricas)
	}

	return &Inventario{
		Hardware:        construirHardware(datos),
		Bios:            construirBios(datos),
		OperatingSystem: construirSO(datos),
		CPUs:            mapearCPUs(datos.CPUs),
		Memories:        mapearMemorias(datos.Memories),
		Storages:        mapearAlmacenamiento(datos.Disks, datos.PhysicalDisks),
		Networks:        mapearRedes(datos.NICs, datos.Adapters),
		Softwares:       softwares,
		LiveMetrics:     liveMetrics,
	}, nil
}

// RecolectarMetricasVivo recolecta CPU%, RAM y disco actuales. CPU% usa
// Win32_Processor.LoadPercentage (promedio entre todos los procesadores lógicos),
// que WMI documenta como una foto del último segundo — sin privilegios elevados,
// equivalente en espíritu al `top -l 1` sin sudo que usa darwin.go.
func (w *WindowsCollector) RecolectarMetricasVivo() (*LiveMetrics, error) {
	m := &LiveMetrics{}

	datos, err := recolectarMetricasVivoWMI()
	if err != nil {
		return m, fmt.Errorf("consultar métricas en vivo: %w", err)
	}

	if len(datos.CPUs) > 0 {
		var suma, cuenta int
		for _, c := range datos.CPUs {
			if c.LoadPercentage != nil {
				suma += *c.LoadPercentage
				cuenta++
			}
		}
		if cuenta > 0 {
			m.CPUPercent = float64(suma) / float64(cuenta)
		}
	} else {
		log.Printf("[ADVERTENCIA] No se pudo recolectar %% de CPU: sin datos de LoadPercentage")
	}

	if datos.OS != nil {
		totalMB := int64(datos.OS.TotalVisibleMemorySize) / 1024
		libreMB := int64(datos.OS.FreePhysicalMemory) / 1024
		m.RAMTotalMB = totalMB
		m.RAMUsedMB = totalMB - libreMB
		if m.RAMUsedMB < 0 {
			m.RAMUsedMB = 0
		}
	} else {
		log.Printf("[ADVERTENCIA] No se pudo recolectar uso de RAM")
	}

	if len(datos.Disks) > 0 {
		for _, d := range datos.Disks {
			totalMB := int64(d.Size) / 1024 / 1024
			if totalMB <= 0 {
				continue // unidad sin tamaño reportado (p.ej. lectora vacía que igual pasó el filtro): no es representable
			}
			libreMB := int64(d.FreeSpace) / 1024 / 1024
			usedMB := totalMB - libreMB
			if usedMB < 0 {
				usedMB = 0
			}
			m.Partitions = append(m.Partitions, Partition{
				Name:       d.DeviceID,
				MountPoint: d.DeviceID,
				UsedMB:     usedMB,
				TotalMB:    totalMB,
			})
		}
	} else {
		log.Printf("[ADVERTENCIA] No se pudo recolectar uso de disco")
	}

	return m, nil
}

// --- Tipos para decodificar la salida JSON de Get-CimInstance ---

// numeroWMI decodifica un campo numérico de WMI que PowerShell puede serializar
// como número JSON o como cadena entre comillas: varios campos de WMI (p.ej.
// Capacity de Win32_PhysicalMemory, Size de Win32_DiskDrive/Win32_LogicalDisk) son
// de tipo string en el MOF original aunque solo contengan dígitos.
type numeroWMI int64

func (n *numeroWMI) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return fmt.Errorf("numeroWMI: %q no es numérico: %w", s, err)
	}
	*n = numeroWMI(v)
	return nil
}

type wmiComputerSystem struct {
	Name         string `json:"Name"`
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
}

type wmiComputerSystemProduct struct {
	UUID              string `json:"UUID"`
	Vendor            string `json:"Vendor"`
	Name              string `json:"Name"`
	IdentifyingNumber string `json:"IdentifyingNumber"`
}

type wmiBaseBoard struct {
	Manufacturer string `json:"Manufacturer"`
	Product      string `json:"Product"`
}

type wmiBIOS struct {
	Manufacturer      string `json:"Manufacturer"`
	SerialNumber      string `json:"SerialNumber"`
	SMBIOSBIOSVersion string `json:"SMBIOSBIOSVersion"`
	ReleaseDate       string `json:"ReleaseDate"`
}

type wmiOperatingSystem struct {
	Caption                string    `json:"Caption"`
	Version                string    `json:"Version"`
	OSArchitecture         string    `json:"OSArchitecture"`
	TotalVisibleMemorySize numeroWMI `json:"TotalVisibleMemorySize"`
	FreePhysicalMemory     numeroWMI `json:"FreePhysicalMemory"`
}

type wmiProcessor struct {
	Name                      string `json:"Name"`
	Manufacturer              string `json:"Manufacturer"`
	NumberOfCores             int    `json:"NumberOfCores"`
	NumberOfLogicalProcessors int    `json:"NumberOfLogicalProcessors"`
	MaxClockSpeed             int    `json:"MaxClockSpeed"`
	LoadPercentage            *int   `json:"LoadPercentage"`
}

type wmiPhysicalMemory struct {
	Capacity     numeroWMI `json:"Capacity"`
	Manufacturer string    `json:"Manufacturer"`
	Speed        numeroWMI `json:"Speed"`
	MemoryType   int       `json:"MemoryType"`
}

type wmiDiskDrive struct {
	DeviceID      string    `json:"DeviceID"`
	Model         string    `json:"Model"`
	Manufacturer  string    `json:"Manufacturer"`
	Size          numeroWMI `json:"Size"`
	MediaType     string    `json:"MediaType"`
	InterfaceType string    `json:"InterfaceType"`
}

// wmiPhysicalDiskMedia modela MSFT_PhysicalDisk (namespace ROOT\Microsoft\Windows\Storage),
// que sí distingue SSD/HDD de forma confiable — Win32_DiskDrive.MediaType normalmente
// solo reporta "Fixed hard disk media" genérico sin importar el tipo real.
type wmiPhysicalDiskMedia struct {
	DeviceId     string `json:"DeviceId"`
	MediaType    int    `json:"MediaType"`
	FriendlyName string `json:"FriendlyName"`
}

type wmiLogicalDisk struct {
	DeviceID  string    `json:"DeviceID"`
	Size      numeroWMI `json:"Size"`
	FreeSpace numeroWMI `json:"FreeSpace"`
}

type wmiNetworkAdapterConfig struct {
	Index       int      `json:"Index"`
	Description string   `json:"Description"`
	MACAddress  string   `json:"MACAddress"`
	IPAddress   []string `json:"IPAddress"`
}

// wmiNetworkAdapter modela Win32_NetworkAdapter, consultada junto con
// Win32_NetworkAdapterConfiguration (join por Index) porque solo esta clase expone
// el tipo de puerto (AdapterType/NetConnectionID) y si está administrativamente
// habilitada (NetEnabled) — equivalente Windows de `networksetup
// -listallhardwareports` que usa darwin.go para lo mismo.
type wmiNetworkAdapter struct {
	Index           int    `json:"Index"`
	NetConnectionID string `json:"NetConnectionID"`
	AdapterType     string `json:"AdapterType"`
	NetEnabled      *bool  `json:"NetEnabled"`
}

// wmiInventarioCompleto agrupa el resultado de una única consulta PowerShell que
// trae todas las clases WMI necesarias en un solo subproceso (evita pagar varias
// veces el costo de arranque de powershell.exe).
type wmiInventarioCompleto struct {
	ComputerSystem *wmiComputerSystem        `json:"computersystem"`
	Product        *wmiComputerSystemProduct `json:"product"`
	BaseBoard      *wmiBaseBoard             `json:"baseboard"`
	BIOS           *wmiBIOS                  `json:"bios"`
	OS             *wmiOperatingSystem       `json:"os"`
	CPUs           []wmiProcessor            `json:"cpus"`
	Memories       []wmiPhysicalMemory       `json:"memories"`
	Disks          []wmiDiskDrive            `json:"disks"`
	LogicalDisks   []wmiLogicalDisk          `json:"logicaldisks"`
	NICs           []wmiNetworkAdapterConfig `json:"nics"`
	Adapters       []wmiNetworkAdapter       `json:"adapters"`
	PhysicalDisks  []wmiPhysicalDiskMedia    `json:"physicaldisks"`
}

type wmiMetricasVivo struct {
	OS    *wmiOperatingSystem `json:"os"`
	CPUs  []wmiProcessor      `json:"cpus"`
	Disks []wmiLogicalDisk    `json:"disks"`
}

// scriptInventarioCompleto consulta en un solo proceso de PowerShell todas las
// clases WMI necesarias para el inventario completo. `@(...)` fuerza que los
// resultados con 0 o 1 elementos sigan serializando como arreglo JSON (sin eso,
// ConvertTo-Json colapsa una colección de un solo elemento a un objeto suelto,
// rompiendo el unmarshal del lado de Go).
const scriptInventarioCompleto = `
$ErrorActionPreference = 'Stop'
$cs  = Get-CimInstance Win32_ComputerSystem | Select-Object -First 1 Name, Manufacturer, Model
$csp = Get-CimInstance Win32_ComputerSystemProduct | Select-Object -First 1 UUID, Vendor, Name, IdentifyingNumber
$bb = $null
try { $bb = Get-CimInstance Win32_BaseBoard | Select-Object -First 1 Manufacturer, Product } catch {}
$bios = Get-CimInstance Win32_BIOS | Select-Object -First 1 Manufacturer, SerialNumber, SMBIOSBIOSVersion, ReleaseDate
$os  = Get-CimInstance Win32_OperatingSystem | Select-Object -First 1 Caption, Version, OSArchitecture, TotalVisibleMemorySize, FreePhysicalMemory
$cpus = @(Get-CimInstance Win32_Processor | Select-Object Name, Manufacturer, NumberOfCores, NumberOfLogicalProcessors, MaxClockSpeed, LoadPercentage)
$mems = @(Get-CimInstance Win32_PhysicalMemory | Select-Object Capacity, Manufacturer, Speed, MemoryType)
$disks = @(Get-CimInstance Win32_DiskDrive | Select-Object DeviceID, Model, Manufacturer, Size, MediaType, InterfaceType)
$logical = @(Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | Select-Object DeviceID, Size, FreeSpace)
$nics = @(Get-CimInstance Win32_NetworkAdapterConfiguration -Filter "IPEnabled=TRUE" | Select-Object Index, Description, MACAddress, IPAddress)
$adapters = @(Get-CimInstance Win32_NetworkAdapter | Select-Object Index, NetConnectionID, AdapterType, NetEnabled)
$physMedia = @()
try { $physMedia = @(Get-CimInstance -Namespace "ROOT\Microsoft\Windows\Storage" -ClassName MSFT_PhysicalDisk -ErrorAction Stop | Select-Object DeviceId, MediaType, FriendlyName) } catch {}

$result = [ordered]@{
  computersystem = $cs
  product        = $csp
  baseboard      = $bb
  bios           = $bios
  os             = $os
  cpus           = $cpus
  memories       = $mems
  disks          = $disks
  logicaldisks   = $logical
  nics           = $nics
  adapters       = $adapters
  physicaldisks  = $physMedia
}
$result | ConvertTo-Json -Depth 6 -Compress
`

// scriptMetricasVivo es una consulta mucho más liviana que scriptInventarioCompleto,
// pensada para repetirse con frecuencia (ciclo de contacto) sin el costo de las
// clases menos cambiantes (BIOS, discos físicos, red).
const scriptMetricasVivo = `
$ErrorActionPreference = 'Stop'
$os = Get-CimInstance Win32_OperatingSystem | Select-Object -First 1 TotalVisibleMemorySize, FreePhysicalMemory
$cpus = @(Get-CimInstance Win32_Processor | Select-Object LoadPercentage)
# DriveType=3 = unidad de disco fijo local (excluye CD-ROM, removibles y unidades de red
# mapeadas — ninguna de esas es una partición real del equipo). Todas las unidades, no solo
# la del sistema (TT11524): un equipo puede tener más de un disco físico o partición real.
$disks = @(Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | Select-Object DeviceID, Size, FreeSpace)
([ordered]@{ os = $os; cpus = $cpus; disks = $disks }) | ConvertTo-Json -Depth 4 -Compress
`

func recolectarDatosWMI() (*wmiInventarioCompleto, error) {
	salida, err := ejecutarPowerShellTexto(scriptInventarioCompleto, 30*time.Second)
	if err != nil {
		return nil, err
	}

	var datos wmiInventarioCompleto
	if err := json.Unmarshal([]byte(salida), &datos); err != nil {
		return nil, fmt.Errorf("parsear JSON de inventario WMI: %w", err)
	}
	return &datos, nil
}

func recolectarMetricasVivoWMI() (*wmiMetricasVivo, error) {
	salida, err := ejecutarPowerShellTexto(scriptMetricasVivo, 15*time.Second)
	if err != nil {
		return nil, err
	}

	var datos wmiMetricasVivo
	if err := json.Unmarshal([]byte(salida), &datos); err != nil {
		return nil, fmt.Errorf("parsear JSON de métricas vivas: %w", err)
	}
	return &datos, nil
}

// ejecutarPowerShellTexto ejecuta un script de PowerShell como subproceso y
// retorna su stdout. Equivalente Windows de ejecutarComando() en darwin.go.
func ejecutarPowerShellTexto(script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("powershell.exe falló: %w — stderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// --- Mapeo de datos WMI a los tipos compartidos de Collector ---

func construirHardware(d *wmiInventarioCompleto) Hardware {
	hw := Hardware{}
	if d.ComputerSystem != nil {
		hw.Name = strings.TrimSpace(d.ComputerSystem.Name)
	}
	if d.Product != nil {
		hw.UUID = strings.TrimSpace(d.Product.UUID)
	}
	return hw
}

func construirBios(d *wmiInventarioCompleto) Bios {
	bios := Bios{}

	if d.Product != nil {
		bios.SManufacturer = strings.TrimSpace(d.Product.Vendor)
		bios.SModel = strings.TrimSpace(d.Product.Name)
	}
	if bios.SManufacturer == "" && d.ComputerSystem != nil {
		bios.SManufacturer = strings.TrimSpace(d.ComputerSystem.Manufacturer)
	}
	if bios.SModel == "" && d.ComputerSystem != nil {
		bios.SModel = strings.TrimSpace(d.ComputerSystem.Model)
	}

	if d.BaseBoard != nil {
		bios.MManufacturer = strings.TrimSpace(d.BaseBoard.Manufacturer)
		bios.MModel = strings.TrimSpace(d.BaseBoard.Product)
	}
	if bios.MManufacturer == "" && d.ComputerSystem != nil {
		bios.MManufacturer = strings.TrimSpace(d.ComputerSystem.Manufacturer)
	}
	if bios.MModel == "" && d.ComputerSystem != nil {
		bios.MModel = strings.TrimSpace(d.ComputerSystem.Model)
	}

	if d.BIOS != nil {
		bios.SSN = strings.TrimSpace(d.BIOS.SerialNumber)
		bios.BManufacturer = strings.TrimSpace(d.BIOS.Manufacturer)
		bios.BVersion = strings.TrimSpace(d.BIOS.SMBIOSBIOSVersion)
		bios.BDate = formatearFechaBIOS(d.BIOS.ReleaseDate)
	}
	if bios.SSN == "" && d.Product != nil {
		bios.SSN = strings.TrimSpace(d.Product.IdentifyingNumber)
	}

	return bios
}

// formatearFechaBIOS normaliza ReleaseDate a "YYYY-MM-DD". Get-CimInstance (a
// diferencia del legado Get-WmiObject) ya convierte el CIM_DATETIME crudo de WMI a
// un DateTime de .NET, que ConvertTo-Json serializa como texto ISO-8601 — se
// intentan los formatos más comunes y, si ninguno aplica, se deja el valor crudo
// tal cual (nunca se inventa una fecha de hoy: a diferencia del caso sintético de
// darwin.go para Apple, en Windows normalmente sí hay un dato real que reportar).
func formatearFechaBIOS(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.9999999",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return raw
}

func construirSO(d *wmiInventarioCompleto) OperatingSystem {
	so := OperatingSystem{Name: "Windows", Arch: runtime.GOARCH}
	if d.OS != nil {
		so.FullName = strings.TrimSpace(d.OS.Caption)
		so.Version = strings.TrimSpace(d.OS.Version)
	}
	return so
}

func mapearCPUs(cpus []wmiProcessor) []CPU {
	resultado := make([]CPU, 0, len(cpus))
	for _, c := range cpus {
		resultado = append(resultado, CPU{
			Name:         strings.TrimSpace(c.Name),
			Manufacturer: normalizarFabricanteCPU(c.Manufacturer, c.Name),
			Core:         c.NumberOfCores,
			Thread:       c.NumberOfLogicalProcessors,
			Speed:        c.MaxClockSpeed,
		})
	}
	return resultado
}

// normalizarFabricanteCPU homologa el Manufacturer crudo de WMI ("GenuineIntel",
// "AuthenticAMD") al mismo formato legible que usa darwin.go ("Intel"/"AMD").
func normalizarFabricanteCPU(manufacturer, name string) string {
	texto := strings.ToLower(manufacturer + " " + name)
	switch {
	case strings.Contains(texto, "intel"):
		return "Intel"
	case strings.Contains(texto, "amd"):
		return "AMD"
	default:
		return strings.TrimSpace(manufacturer)
	}
}

func mapearMemorias(mems []wmiPhysicalMemory) []Memory {
	resultado := make([]Memory, 0, len(mems))
	for _, m := range mems {
		resultado = append(resultado, Memory{
			Capacity:     int(int64(m.Capacity) / 1024 / 1024),
			Type:         tipoMemoriaWMI(m.MemoryType),
			Speed:        fmt.Sprintf("%d", int64(m.Speed)),
			Manufacturer: strings.TrimSpace(m.Manufacturer),
		})
	}
	return resultado
}

// tipoMemoriaWMI mapea los códigos DMTF/SMBIOS de Win32_PhysicalMemory.MemoryType
// relevantes en equipos actuales. Códigos no listados (obsoletos o "Unknown")
// retornan "N/D", igual convención que darwin.go usa para Speed cuando no aplica.
func tipoMemoriaWMI(codigo int) string {
	switch codigo {
	case 20:
		return "DDR"
	case 21:
		return "DDR2"
	case 24:
		return "DDR3"
	case 26:
		return "DDR4"
	case 34:
		return "DDR5"
	default:
		return "N/D"
	}
}

// numeroFinalRegex extrae el índice numérico final de un DeviceID de disco físico
// (p.ej. "\\.\PHYSICALDRIVE0" -> "0"), usado para cruzar Win32_DiskDrive con
// MSFT_PhysicalDisk (que identifica discos por el mismo número de índice).
var numeroFinalRegex = regexp.MustCompile(`(\d+)$`)

func mapearAlmacenamiento(disks []wmiDiskDrive, mediaInfo []wmiPhysicalDiskMedia) []Storage {
	mediaPorIndice := map[string]int{}
	for _, pd := range mediaInfo {
		mediaPorIndice[pd.DeviceId] = pd.MediaType
	}

	resultado := make([]Storage, 0, len(disks))
	for _, d := range disks {
		tipo := strings.TrimSpace(d.MediaType)
		if indice := numeroFinalRegex.FindString(d.DeviceID); indice != "" {
			if codigo, ok := mediaPorIndice[indice]; ok {
				switch codigo {
				case 3:
					tipo = "HDD"
				case 4:
					tipo = "SSD"
				case 5:
					tipo = "SCM"
				}
			}
		}

		modelo := strings.TrimSpace(d.Model)
		resultado = append(resultado, Storage{
			Manufacturer: strings.TrimSpace(d.Manufacturer),
			Model:        modelo,
			Name:         modelo,
			DiskSize:     int64(d.Size) / 1024 / 1024,
			Type:         tipo,
		})
	}
	return resultado
}

func mapearRedes(nics []wmiNetworkAdapterConfig, adapters []wmiNetworkAdapter) []Network {
	adaptadoresPorIndice := map[int]wmiNetworkAdapter{}
	for _, a := range adapters {
		adaptadoresPorIndice[a.Index] = a
	}

	resultado := make([]Network, 0, len(nics))
	for _, n := range nics {
		ip := primeraIPv4(n.IPAddress)

		// Ethernet por defecto si no se puede determinar — mismo respaldo que
		// determinarTipoRed() en darwin.go, por la misma razón: asumir Wi-Fi sin
		// evidencia real produce más falsos positivos que asumir Ethernet.
		tipo := "Ethernet"
		estado := ""
		if a, ok := adaptadoresPorIndice[n.Index]; ok {
			textoTipo := strings.ToLower(a.AdapterType + " " + a.NetConnectionID)
			if strings.Contains(textoTipo, "wireless") || strings.Contains(textoTipo, "wi-fi") || strings.Contains(textoTipo, "802.11") {
				tipo = "WiFi"
			} else if strings.Contains(textoTipo, "ethernet") {
				tipo = "Ethernet"
			}
			if a.NetEnabled != nil {
				if *a.NetEnabled {
					estado = "up"
				} else {
					estado = "down"
				}
			}
		}
		if estado == "" {
			// Sin NetEnabled disponible: una IP real asignada es la mejor señal de
			// actividad que queda, igual fallback de flags que usa parsearIfconfig().
			if ip != "" {
				estado = "up"
			} else {
				estado = "down"
			}
		}

		resultado = append(resultado, Network{
			Description: strings.TrimSpace(n.Description),
			IPAddress:   ip,
			MACAddr:     strings.TrimSpace(n.MACAddress),
			Status:      estado,
			Type:        tipo,
		})
	}
	return resultado
}

func primeraIPv4(direcciones []string) string {
	for _, d := range direcciones {
		if strings.Count(d, ".") == 3 {
			return d
		}
	}
	if len(direcciones) > 0 {
		return direcciones[0]
	}
	return ""
}

// --- Software instalado: lectura directa del registro (ver docstring del paquete) ---

// rutasDesinstalacion son las dos ubicaciones estándar del registro donde Windows
// guarda una entrada por cada programa instalado (la segunda solo existe en
// Windows de 64 bits, para apps de 32 bits — su ausencia no es un error).
var rutasDesinstalacion = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

func recolectarSoftware() ([]Software, error) {
	vistos := map[string]bool{}
	var resultado []Software
	var ultimoError error

	for _, ruta := range rutasDesinstalacion {
		claveRaiz, err := registry.OpenKey(registry.LOCAL_MACHINE, ruta, registry.READ)
		if err != nil {
			// WOW6432Node no existe en Windows de 32 bits puro — no es un error real,
			// solo se registra si AMBAS rutas fallan (ver más abajo).
			ultimoError = err
			continue
		}

		nombresSubclaves, err := claveRaiz.ReadSubKeyNames(-1)
		claveRaiz.Close()
		if err != nil {
			ultimoError = err
			continue
		}

		for _, nombreSubclave := range nombresSubclaves {
			sw, ok := leerEntradaDesinstalacion(ruta, nombreSubclave)
			if !ok {
				continue
			}
			clave := sw.Name + "|" + sw.Version + "|" + sw.Publisher
			if vistos[clave] {
				continue
			}
			vistos[clave] = true
			resultado = append(resultado, sw)
		}
	}

	if resultado == nil && ultimoError != nil {
		return nil, ultimoError
	}
	return resultado, nil
}

// leerEntradaDesinstalacion lee una subclave de Uninstall y decide si representa
// software "instalado" visible para el usuario (criterio: tener DisplayName, no
// estar marcada SystemComponent=1 y no ser un parche con ParentKeyName — el mismo
// filtro que aplica el propio Panel de control de Windows a este registro).
func leerEntradaDesinstalacion(rutaPadre, nombreSubclave string) (Software, bool) {
	ruta := rutaPadre + `\` + nombreSubclave
	clave, err := registry.OpenKey(registry.LOCAL_MACHINE, ruta, registry.READ)
	if err != nil {
		return Software{}, false
	}
	defer clave.Close()

	nombre, _, err := clave.GetStringValue("DisplayName")
	if err != nil || strings.TrimSpace(nombre) == "" {
		return Software{}, false
	}

	if systemComponent, _, err := clave.GetIntegerValue("SystemComponent"); err == nil && systemComponent == 1 {
		return Software{}, false
	}

	if _, _, err := clave.GetStringValue("ParentKeyName"); err == nil {
		return Software{}, false
	}

	version, _, _ := clave.GetStringValue("DisplayVersion")
	publisher, _, _ := clave.GetStringValue("Publisher")

	return Software{
		Name:      strings.TrimSpace(nombre),
		Version:   strings.TrimSpace(version),
		Publisher: strings.TrimSpace(publisher),
		Category:  clasificarSoftware(publisher),
	}, true
}

// clasificarSoftware es deliberadamente más simple que su equivalente en
// darwin.go: el registro de Windows no expone una firma de código ni un origen
// ("App Store"/"identified developer") por entrada sin abrir y verificar cada
// binario (costoso en una máquina con cientos de programas instalados), así que
// solo se distingue software propio de Microsoft (UTILIDAD_SISTEMA) de terceros
// (APLICACION) o sin publisher declarado (TERCERO_SIN_IDENTIFICAR). No hay
// equivalente Windows de APP_STORE/TERCERO_FIRMADO con esta fuente de datos.
func clasificarSoftware(publisher string) string {
	p := strings.ToLower(strings.TrimSpace(publisher))
	switch {
	case p == "":
		return "TERCERO_SIN_IDENTIFICAR"
	case strings.Contains(p, "microsoft"):
		return "UTILIDAD_SISTEMA"
	default:
		return "APLICACION"
	}
}
