//go:build darwin

// Package inventory — implementación macOS del Collector.
// Recolecta datos del sistema usando herramientas nativas de macOS:
// system_profiler, sysctl, sw_vers, ifconfig e ioreg.
// No se usa ninguna librería externa: solo stdlib de Go y comandos del sistema.
package inventory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DarwinCollector implementa la interfaz Collector para macOS.
type DarwinCollector struct{}

// NuevoCollector crea un DarwinCollector listo para usar.
func NuevoCollector() Collector {
	return &DarwinCollector{}
}

// ObtenerDeviceID retorna el UUID de IOPlatformExpert, que es estable entre reinicios en macOS.
func (d *DarwinCollector) ObtenerDeviceID() (string, error) {
	salida, err := ejecutarComando("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	if err != nil {
		return "", fmt.Errorf("obtener UUID de plataforma: %w", err)
	}

	for _, linea := range strings.Split(salida, "\n") {
		if strings.Contains(linea, "IOPlatformUUID") {
			partes := strings.Split(linea, "\"")
			if len(partes) >= 4 {
				return partes[3], nil
			}
		}
	}

	return "", fmt.Errorf("UUID de IOPlatformExpert no encontrado en salida de ioreg")
}

// RecolectarInventario recopila todos los datos del sistema macOS.
// La recolección de software (lenta, ~5-10s) corre en paralelo con el resto.
func (d *DarwinCollector) RecolectarInventario() (*Inventario, error) {
	// Recolectar software en paralelo para no bloquear el resto del inventario.
	var wg sync.WaitGroup
	var softwares []Software
	var errSoftware error

	wg.Add(1)
	go func() {
		defer wg.Done()
		softwares, errSoftware = recolectarSoftware()
	}()

	hw, bios, err := recolectarHardware()
	if err != nil {
		return nil, fmt.Errorf("recolectar hardware: %w", err)
	}

	so, err := recolectarSO()
	if err != nil {
		return nil, fmt.Errorf("recolectar sistema operativo: %w", err)
	}

	cpus, err := recolectarCPU()
	if err != nil {
		return nil, fmt.Errorf("recolectar CPU: %w", err)
	}

	memorias, err := recolectarRAM()
	if err != nil {
		return nil, fmt.Errorf("recolectar memoria: %w", err)
	}

	almacenamiento, err := recolectarAlmacenamiento()
	if err != nil {
		return nil, fmt.Errorf("recolectar almacenamiento: %w", err)
	}

	redes, err := recolectarRedes()
	if err != nil {
		return nil, fmt.Errorf("recolectar redes: %w", err)
	}

	videos, errVideos := recolectarGPU()
	if errVideos != nil {
		// GPU es opcional — registrar el error pero continuar (no bloquea el resto del inventario).
		log.Printf("[ADVERTENCIA] No se pudo recolectar GPU: %v", errVideos)
	}

	// Esperar a que termine la recolección de software.
	wg.Wait()
	if errSoftware != nil {
		// Software es opcional — registrar el error pero continuar.
		log.Printf("[ADVERTENCIA] No se pudo recolectar software instalado: %v", errSoftware)
	}

	liveMetrics, errMetricas := d.RecolectarMetricasVivo()
	if errMetricas != nil {
		// Telemetría es opcional — no debe bloquear el envío del resto del inventario.
		log.Printf("[ADVERTENCIA] No se pudo recolectar métricas en vivo: %v", errMetricas)
	}

	return &Inventario{
		Hardware:        hw,
		Bios:            bios,
		OperatingSystem: so,
		CPUs:            cpus,
		Memories:        memorias,
		Storages:        almacenamiento,
		Networks:        redes,
		Softwares:       softwares,
		Videos:          videos,
		LiveMetrics:     liveMetrics,
	}, nil
}

// RecolectarMetricasVivo recolecta CPU%, RAM y disco actuales sin sudo. GPU omitida: requiere
// `powermetrics` con privilegios root en macOS.
func (d *DarwinCollector) RecolectarMetricasVivo() (*LiveMetrics, error) {
	m := &LiveMetrics{}

	if cpu, err := recolectarCPUPercent(); err == nil {
		m.CPUPercent = cpu
	} else {
		log.Printf("[ADVERTENCIA] No se pudo recolectar %% de CPU: %v", err)
	}

	if usedMB, totalMB, err := recolectarRAMUso(); err == nil {
		m.RAMUsedMB, m.RAMTotalMB = usedMB, totalMB
	} else {
		log.Printf("[ADVERTENCIA] No se pudo recolectar uso de RAM: %v", err)
	}

	if particiones, err := recolectarParticiones(); err == nil {
		m.Partitions = particiones
	} else {
		log.Printf("[ADVERTENCIA] No se pudo recolectar particiones de disco: %v", err)
	}

	return m, nil
}

// recolectarCPUPercent usa `top -l 1 -n 0` (una muestra, sin listar procesos) y parsea la línea
// "CPU usage: X% user, Y% sys, Z% idle". Uso = 100 - idle. Sin sudo.
func recolectarCPUPercent() (float64, error) {
	salida, err := ejecutarComando("top", "-l", "1", "-n", "0")
	if err != nil {
		return 0, fmt.Errorf("top: %w", err)
	}
	for _, linea := range strings.Split(salida, "\n") {
		if !strings.Contains(linea, "CPU usage") {
			continue
		}
		// Formato típico: "CPU usage: 12.34% user, 5.67% sys, 81.99% idle"
		partes := strings.Split(linea, ",")
		for _, p := range partes {
			p = strings.TrimSpace(p)
			if strings.Contains(p, "idle") {
				campos := strings.Fields(p)
				if len(campos) < 1 {
					continue
				}
				idleStr := strings.TrimSuffix(campos[0], "%")
				idle, err := strconv.ParseFloat(idleStr, 64)
				if err != nil {
					return 0, fmt.Errorf("parsear idle de top: %w", err)
				}
				uso := 100 - idle
				if uso < 0 {
					uso = 0
				}
				return uso, nil
			}
		}
	}
	return 0, fmt.Errorf("línea 'CPU usage' no encontrada en salida de top")
}

// recolectarRAMUso usa `sysctl hw.memsize` (total) y `vm_stat` (páginas libres) para estimar
// memoria usada/total en MB. "Usada" = total - libre - inactiva (aproximación estándar de macOS:
// páginas inactivas se consideran recuperables, no realmente "libres" para el usuario, pero
// tampoco "en uso activo" — se cuentan aquí como parte de lo usado por ser la métrica que ve
// Activity Monitor en "Memory Used").
func recolectarRAMUso() (usedMB, totalMB int64, err error) {
	totalStr, err := ejecutarComando("sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	totalBytes, err := strconv.ParseInt(strings.TrimSpace(totalStr), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsear hw.memsize: %w", err)
	}
	totalMB = totalBytes / 1024 / 1024

	vmStat, err := ejecutarComando("vm_stat")
	if err != nil {
		return 0, totalMB, fmt.Errorf("vm_stat: %w", err)
	}

	tamanoPagina := int64(4096) // valor por defecto si no se puede leer del encabezado
	paginas := map[string]int64{}
	for _, linea := range strings.Split(vmStat, "\n") {
		if strings.HasPrefix(linea, "Mach Virtual Memory Statistics") {
			// "Mach Virtual Memory Statistics: (page size of 16384 bytes)"
			inicio := strings.Index(linea, "page size of ")
			if inicio >= 0 {
				resto := linea[inicio+len("page size of "):]
				fin := strings.Index(resto, " bytes")
				if fin > 0 {
					if v, err := strconv.ParseInt(resto[:fin], 10, 64); err == nil {
						tamanoPagina = v
					}
				}
			}
			continue
		}
		partes := strings.SplitN(linea, ":", 2)
		if len(partes) != 2 {
			continue
		}
		clave := strings.TrimSpace(partes[0])
		valorStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(partes[1]), "."))
		valor, err := strconv.ParseInt(valorStr, 10, 64)
		if err != nil {
			continue
		}
		paginas[clave] = valor
	}

	libres := paginas["Pages free"]
	inactivas := paginas["Pages inactive"]
	usedMB = totalMB - (libres+inactivas)*tamanoPagina/1024/1024
	if usedMB < 0 {
		usedMB = 0
	}
	return usedMB, totalMB, nil
}

// recolectarParticiones usa `df -k` para listar TODAS las particiones/volúmenes reales y
// visibles para el usuario en este equipo, excluyendo pseudo-volúmenes internos de macOS.
//
// Causa raíz del bug original (TT11524): la implementación anterior usaba `df -k /` (solo el
// volumen raíz) para el total/uso de disco. Desde Catalina, macOS usa un "volumen de sistema
// sellado" (APFS): "/" es un snapshot de solo lectura, pequeño y casi vacío (en el equipo real
// reportado: 228Gi totales, 13Gi en uso = ~6-12%), mientras que TODOS los datos reales del
// usuario viven en el volumen de datos, montado en "/System/Volumes/Data" (en el mismo equipo:
// ~50% de uso — el número que de verdad coincide con lo que Ajustes del Sistema →
// Almacenamiento le muestra al usuario). Verificado también en este equipo de desarrollo:
// `diskutil apfs list` confirma que "/" (disk3s1s1, rol System) y "/System/Volumes/Data"
// (disk3s5, rol Data) son el MISMO contenedor APFS — exactamente el par que Finder/Ajustes
// presentan fusionado como un solo volumen lógico ("Macintosh HD").
//
// Criterio de "partición real" vs. "pseudo-volumen de sistema" (decisión de esta implementación,
// no hay una sola API de Apple que lo exponga directamente sin privilegios elevados):
//   - Se descarta cualquier filesystem que no sea un dispositivo de bloque real (`devfs`, montajes
//     autofs como "map auto_home"): nunca representan almacenamiento del equipo.
//   - Se descarta cualquier punto de montaje bajo "/System/Volumes/" EXCEPTO "Data": Preboot, VM,
//     Update, Hardware, iSCPreboot, xarts, Recovery son volúmenes internos del propio sistema
//     operativo (gestión de arranque, swap, actualizaciones) — nunca aparecen en Finder ni son
//     relevantes para el usuario.
//   - El volumen raíz "/" en sí NUNCA se reporta con sus propios números (son los del snapshot
//     sellado, casi siempre con uso real ~0): se descarta y se sustituye por
//     "/System/Volumes/Data" retitulado como "/", que es el que lleva el uso real de ESE mismo
//     volumen lógico.
//   - Cualquier OTRO punto de montaje real (p.ej. un segundo disco interno, una unidad externa en
//     "/Volumes/...") se reporta como partición adicional e independiente — este es el soporte
//     explícito para equipos con más de un disco físico o partición real.
func recolectarParticiones() ([]Partition, error) {
	salida, err := ejecutarComando("df", "-k")
	if err != nil {
		return nil, fmt.Errorf("df: %w", err)
	}

	lineas := strings.Split(strings.TrimSpace(salida), "\n")
	if len(lineas) < 2 {
		return nil, fmt.Errorf("salida de df sin datos")
	}

	var resultado []Partition
	for _, linea := range lineas[1:] {
		campos := strings.Fields(linea)
		// Formato: Filesystem 1K-blocks Used Available Capacity iused ifree %iused Mounted on
		if len(campos) < 9 {
			continue
		}
		filesystem := campos[0]
		puntoMontaje := campos[len(campos)-1]

		if !strings.HasPrefix(filesystem, "/dev/") {
			continue // devfs, montajes autofs (p.ej. "map auto_home"): no son almacenamiento real
		}
		if puntoMontaje == "/" {
			continue // volumen de sistema sellado: números no representativos, ver docstring
		}
		if strings.HasPrefix(puntoMontaje, "/System/Volumes/") && puntoMontaje != "/System/Volumes/Data" {
			continue // Preboot/VM/Update/Hardware/iSCPreboot/xarts/Recovery: internos de macOS
		}

		totalKB, errTotal := strconv.ParseInt(campos[1], 10, 64)
		usedKB, errUsed := strconv.ParseInt(campos[2], 10, 64)
		if errTotal != nil || errUsed != nil {
			continue // línea con formato inesperado: se omite en vez de abortar toda la recolección
		}

		if puntoMontaje == "/System/Volumes/Data" {
			puntoMontaje = "/" // mismo volumen lógico que macOS presenta fusionado como "Macintosh HD"
		}

		resultado = append(resultado, Partition{
			Name:       nombreVolumen(puntoMontaje),
			MountPoint: puntoMontaje,
			UsedMB:     usedKB / 1024,
			TotalMB:    totalKB / 1024,
		})
	}

	if len(resultado) == 0 {
		return nil, fmt.Errorf("no se encontraron particiones reales en la salida de df")
	}

	return resultado, nil
}

// nombreVolumen obtiene el nombre visible del volumen (p.ej. "Macintosh HD") vía `diskutil info`.
// Si el comando falla o no expone "Volume Name", se usa el punto de montaje como respaldo.
func nombreVolumen(puntoMontaje string) string {
	salida, err := ejecutarComando("diskutil", "info", puntoMontaje)
	if err != nil {
		return puntoMontaje
	}
	for _, linea := range strings.Split(salida, "\n") {
		if !strings.Contains(linea, "Volume Name:") {
			continue
		}
		partes := strings.SplitN(linea, ":", 2)
		if len(partes) == 2 {
			if nombre := strings.TrimSpace(partes[1]); nombre != "" {
				return nombre
			}
		}
	}
	return puntoMontaje
}

// --- Funciones privadas de recolección ---

// spHardwareData modela el JSON de system_profiler SPHardwareDataType.
type spHardwareData struct {
	SPHardwareDataType []struct {
		MachineName  string `json:"machine_name"`
		MachineModel string `json:"machine_model"`
		SerialNumber string `json:"serial_number"`
		PlatformUUID string `json:"platform_UUID"`
	} `json:"SPHardwareDataType"`
}

func recolectarHardware() (Hardware, Bios, error) {
	salida, err := ejecutarComando("system_profiler", "SPHardwareDataType", "-json")
	if err != nil {
		return Hardware{}, Bios{}, fmt.Errorf("system_profiler SPHardwareDataType: %w", err)
	}

	var datos spHardwareData
	if err := json.Unmarshal([]byte(salida), &datos); err != nil {
		return Hardware{}, Bios{}, fmt.Errorf("parsear JSON de hardware: %w", err)
	}

	if len(datos.SPHardwareDataType) == 0 {
		return Hardware{}, Bios{}, fmt.Errorf("no se encontraron datos de hardware")
	}

	item := datos.SPHardwareDataType[0]

	// IMPORTANTE: `machine_name` de system_profiler es el NOMBRE DEL MODELO (p.ej. "Mac mini"),
	// igual para todos los equipos del mismo modelo — NO es el hostname de esta máquina en
	// particular. El nombre real y único del equipo viene de `scutil --get ComputerName`
	// (nombre configurado por el usuario) o, si falla, de `hostname`.
	nombreEquipo := ""
	if nombre, err := ejecutarComando("scutil", "--get", "ComputerName"); err == nil {
		nombreEquipo = strings.TrimSpace(nombre)
	}
	if nombreEquipo == "" {
		if nombre, err := ejecutarComando("hostname"); err == nil {
			nombreEquipo = strings.TrimSpace(nombre)
		}
	}

	hw := Hardware{
		Name: nombreEquipo,
		UUID: item.PlatformUUID,
	}

	bios := Bios{
		SManufacturer: "Apple Inc.",
		SModel:        item.MachineModel,
		SSN:           item.SerialNumber,
		MManufacturer: "Apple",
		MModel:        item.MachineModel,
		BManufacturer: "Apple ROM",
		BVersion:      "1.0",
		BDate:         time.Now().Format("2006-01-02"),
	}

	return hw, bios, nil
}

func recolectarSO() (OperatingSystem, error) {
	version, err := ejecutarComando("sw_vers", "-productVersion")
	if err != nil {
		return OperatingSystem{}, fmt.Errorf("sw_vers -productVersion: %w", err)
	}

	arch, err := ejecutarComando("uname", "-m")
	if err != nil {
		return OperatingSystem{}, fmt.Errorf("uname -m: %w", err)
	}

	version = strings.TrimSpace(version)
	arch = strings.TrimSpace(arch)

	return OperatingSystem{
		FullName: "macOS " + version,
		Name:     "macOS",
		Version:  version,
		Arch:     arch,
	}, nil
}

func recolectarCPU() ([]CPU, error) {
	nombre, err := ejecutarComando("sysctl", "-n", "machdep.cpu.brand_string")
	if err != nil {
		// En Apple Silicon, machdep.cpu.brand_string puede no existir.
		modeloHW, err2 := ejecutarComando("sysctl", "-n", "hw.model")
		if err2 != nil {
			return nil, fmt.Errorf("obtener nombre de CPU: %w (fallback: %v)", err, err2)
		}
		nombre = modeloHW
	}
	nombre = strings.TrimSpace(nombre)

	// Núcleos físicos.
	coresStr, err := ejecutarComando("sysctl", "-n", "hw.physicalcpu")
	if err != nil {
		coresStr = "1"
	}
	cores, _ := strconv.Atoi(strings.TrimSpace(coresStr))
	if cores == 0 {
		cores = 1
	}

	// Hilos lógicos.
	threadsStr, err := ejecutarComando("sysctl", "-n", "hw.logicalcpu")
	if err != nil {
		threadsStr = strconv.Itoa(cores)
	}
	threads, _ := strconv.Atoi(strings.TrimSpace(threadsStr))
	if threads == 0 {
		threads = cores
	}

	// Velocidad en MHz (puede no estar disponible en Apple Silicon).
	velocidad := 0
	if freqStr, err := ejecutarComando("sysctl", "-n", "hw.cpufrequency"); err == nil {
		if freq, err := strconv.ParseInt(strings.TrimSpace(freqStr), 10, 64); err == nil {
			velocidad = int(freq / 1_000_000)
		}
	}

	fabricante := "Apple"
	if strings.Contains(strings.ToLower(nombre), "intel") {
		fabricante = "Intel"
	} else if strings.Contains(strings.ToLower(nombre), "amd") {
		fabricante = "AMD"
	}

	return []CPU{{
		Name:         nombre,
		Manufacturer: fabricante,
		Core:         cores,
		Thread:       threads,
		Speed:        velocidad,
	}}, nil
}

func recolectarRAM() ([]Memory, error) {
	tamBytes, err := ejecutarComando("sysctl", "-n", "hw.memsize")
	if err != nil {
		return nil, fmt.Errorf("sysctl hw.memsize: %w", err)
	}

	tam, err := strconv.ParseInt(strings.TrimSpace(tamBytes), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsear tamaño de RAM: %w", err)
	}

	tamMB := int(tam / 1024 / 1024)

	return []Memory{{
		Capacity:     tamMB,
		Type:         "LPDDR5",
		Speed:        "N/D",
		Manufacturer: "Apple",
	}}, nil
}

// spStorageData modela el JSON real de `system_profiler SPStorageDataType -json`.
// Las claves verificadas contra la salida real son `size_in_bytes` (top-level, no
// `spstorage_total_amount_absolute`) y `medium_type` dentro de `physical_drive`.
type spStorageData struct {
	SPStorageDataType []struct {
		Name          string `json:"_name"`
		SizeBytes     int64  `json:"size_in_bytes"`
		PhysicalDrive *struct {
			DeviceName string `json:"device_name"`
			MediumType string `json:"medium_type"`
		} `json:"physical_drive,omitempty"`
	} `json:"SPStorageDataType"`
}

func recolectarAlmacenamiento() ([]Storage, error) {
	salida, err := ejecutarComando("system_profiler", "SPStorageDataType", "-json")
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPStorageDataType: %w", err)
	}

	var datos spStorageData
	if err := json.Unmarshal([]byte(salida), &datos); err != nil {
		// Si no se puede parsear, retornar vacío — no es crítico.
		log.Printf("[ADVERTENCIA] No se pudo parsear datos de almacenamiento: %v", err)
		return []Storage{}, nil
	}

	// macOS lista cada VOLUMEN (Data, Preboot, etc.) como una entrada separada, pero todos
	// los volúmenes del disco físico interno comparten el mismo `device_name` — sin deduplicar
	// se reportaría el mismo disco 2-3 veces con el mismo tamaño. Nos quedamos con una entrada
	// por disco físico único (o por volumen si no hay physical_drive, caso raro).
	vistos := map[string]bool{}
	var resultado []Storage
	for _, item := range datos.SPStorageDataType {
		modelo := ""
		tipoMedio := "SSD"
		clave := item.Name
		if item.PhysicalDrive != nil {
			modelo = item.PhysicalDrive.DeviceName
			if item.PhysicalDrive.MediumType != "" {
				tipoMedio = item.PhysicalDrive.MediumType
			}
			clave = modelo // deduplicar por disco físico, no por volumen
		}
		if clave != "" && vistos[clave] {
			continue
		}
		vistos[clave] = true

		resultado = append(resultado, Storage{
			Manufacturer: "Apple",
			Model:        modelo,
			Name:         item.Name,
			DiskSize:     item.SizeBytes / 1024 / 1024, // bytes -> MB
			Type:         tipoMedio,
		})
	}

	return resultado, nil
}

func recolectarRedes() ([]Network, error) {
	salida, err := ejecutarComando("ifconfig")
	if err != nil {
		return nil, fmt.Errorf("ifconfig: %w", err)
	}

	// El mapeo nombre de interfaz -> tipo de puerto (Wi-Fi/Ethernet) NO es fijo por nombre:
	// en Mac mini/Studio/Pro, en0 suele ser Ethernet y en1 Wi-Fi — al revés que en MacBook.
	// `networksetup -listallhardwareports` da la verdad real del hardware de ESTE equipo,
	// incluida la MAC real (ifconfig puede reportar un placeholder de virtualización/sandboxing
	// idéntico en todas las interfaces).
	mapaRedes := obtenerMapaRedes()

	return parsearIfconfig(salida, mapaRedes), nil
}

// obtenerMapaRedes consulta networksetup para mapear cada interfaz (en0, en1...) a su tipo
// real de hardware ("Wi-Fi"/"Ethernet") y su MAC real. Es la fuente autoritativa: el "ether"
// que reporta `ifconfig` puede ser un placeholder de virtualización/sandboxing idéntico en
// TODAS las interfaces (visto en pruebas: 02:00:00:00:00:00 repetido en en0, en1, en3, en4...),
// mientras que networksetup expone la dirección de hardware real y única por puerto.
type infoPuertoRed struct {
	Tipo string
	MAC  string
}

func obtenerMapaRedes() map[string]infoPuertoRed {
	salida, err := ejecutarComando("networksetup", "-listallhardwareports")
	mapa := map[string]infoPuertoRed{}
	if err != nil {
		return mapa
	}

	var puertoActual string
	var dispositivoActual string
	for _, linea := range strings.Split(salida, "\n") {
		linea = strings.TrimSpace(linea)
		if strings.HasPrefix(linea, "Hardware Port: ") {
			puertoActual = strings.TrimPrefix(linea, "Hardware Port: ")
			dispositivoActual = ""
		} else if strings.HasPrefix(linea, "Device: ") && puertoActual != "" {
			dispositivoActual = strings.TrimPrefix(linea, "Device: ")
			tipo := puertoActual
			if strings.Contains(strings.ToLower(puertoActual), "wi-fi") || strings.Contains(strings.ToLower(puertoActual), "airport") {
				tipo = "WiFi"
			} else if strings.Contains(strings.ToLower(puertoActual), "ethernet") {
				tipo = "Ethernet"
			}
			mapa[dispositivoActual] = infoPuertoRed{Tipo: tipo}
		} else if strings.HasPrefix(linea, "Ethernet Address: ") && dispositivoActual != "" {
			info := mapa[dispositivoActual]
			info.MAC = strings.TrimPrefix(linea, "Ethernet Address: ")
			mapa[dispositivoActual] = info
		}
	}
	return mapa
}

// parsearIfconfig extrae interfaces de red relevantes del output de ifconfig.
func parsearIfconfig(salida string, redes map[string]infoPuertoRed) []Network {
	var resultado []Network
	var interfazActual *Network
	var flagsTraenUP bool

	for _, linea := range strings.Split(salida, "\n") {
		// Nueva interfaz: empieza sin espacio y contiene ":".
		if len(linea) > 0 && linea[0] != ' ' && linea[0] != '	' {
			if interfazActual != nil && interfazActual.IPAddress != "" {
				// Sin línea explícita "status: active/inactive" (ocurre en adaptadores USB/Thunderbolt
				// a Ethernet): usamos la bandera UP de la cabecera como respaldo. Con una IP real
				// asignada y la interfaz administrativamente UP, se considera activa.
				if interfazActual.Status == "" && flagsTraenUP {
					interfazActual.Status = "up"
				}
				resultado = append(resultado, *interfazActual)
			}
			partes := strings.SplitN(linea, ":", 2)
			nombre := strings.TrimSpace(partes[0])
			flagsTraenUP = strings.Contains(linea, "<UP,") || strings.Contains(linea, ",UP,") || strings.Contains(linea, ",UP>")
			// Filtrar solo interfaces físicas relevantes (en*, bridge*, utun* se omiten).
			if strings.HasPrefix(nombre, "en") || strings.HasPrefix(nombre, "eth") {
				info, conocida := redes[nombre]
				tipo := info.Tipo
				if tipo == "" {
					tipo = determinarTipoRed(nombre)
				}
				interfazActual = &Network{
					Description: nombre,
					Status:      "", // se decide al cerrar la interfaz: "status:" explícito o fallback de flags UP
					Type:        tipo,
				}
				// MAC real de networksetup: se precarga ya, pero si `ether` en ifconfig reporta
				// algo distinto y no-placeholder, se prioriza más abajo (ver nota en MACAddr).
				if conocida && info.MAC != "" {
					interfazActual.MACAddr = info.MAC
				}
			} else {
				interfazActual = nil
			}
			continue
		}

		if interfazActual == nil {
			continue
		}

		linea = strings.TrimSpace(linea)

		if strings.HasPrefix(linea, "inet ") && !strings.Contains(linea, "inet6") {
			partes := strings.Fields(linea)
			if len(partes) >= 2 {
				interfazActual.IPAddress = partes[1]
			}
		} else if strings.HasPrefix(linea, "ether ") {
			partes := strings.Fields(linea)
			// Solo sobrescribir la MAC de networksetup si YA no teníamos una (ifconfig puede
			// reportar un placeholder de virtualización/sandboxing como 02:00:00:00:00:00
			// idéntico en TODAS las interfaces — networksetup es más confiable cuando está
			// disponible).
			if len(partes) >= 2 && interfazActual.MACAddr == "" {
				interfazActual.MACAddr = partes[1]
			}
		} else if strings.Contains(linea, "status: active") {
			interfazActual.Status = "up"
		} else if strings.Contains(linea, "status: inactive") {
			interfazActual.Status = "down"
		}
	}

	// No olvidar la última interfaz procesada (mismo fallback de flags UP que en el bucle).
	if interfazActual != nil && interfazActual.IPAddress != "" {
		if interfazActual.Status == "" && flagsTraenUP {
			interfazActual.Status = "up"
		}
		resultado = append(resultado, *interfazActual)
	}

	return resultado
}

// determinarTipoRed es el único respaldo cuando networksetup no está disponible o no conoce
// la interfaz: asume Ethernet, ya que en0 NO es siempre Wi-Fi (falso en Mac mini/Studio/Pro).
func determinarTipoRed(nombre string) string {
	return "Ethernet"
}

// spDisplaysData modela el JSON de `system_profiler SPDisplaysDataType -json`: una entrada por
// GPU física (no por pantalla conectada). Verificado contra salida real: en Apple Silicon
// `sppci_model`/`_name` traen el nombre del chip (p.ej. "Apple M4") y no hay VRAM dedicada
// (memoria unificada); en Mac con GPU discreta aparece además `spdisplays_vram`.
type spDisplaysData struct {
	SPDisplaysDataType []struct {
		Name   string `json:"_name"`
		Model  string `json:"sppci_model"`
		VRAM   string `json:"spdisplays_vram"`
		Vendor string `json:"spdisplays_vendor"`
	} `json:"SPDisplaysDataType"`
}

// vramPattern extrae el número y la unidad de cadenas como "4 GB" o "1536 MB".
var vramPattern = regexp.MustCompile(`(?i)([\d.]+)\s*(GB|MB)`)

// parseVRAMMB convierte la VRAM reportada por system_profiler a MB. Retorna 0 si no hay VRAM
// dedicada que reportar (caso normal en GPU integrada con memoria unificada).
func parseVRAMMB(s string) int {
	m := vramPattern.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	valor, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	if strings.EqualFold(m[2], "GB") {
		return int(valor * 1024)
	}
	return int(valor)
}

// recolectarGPU usa `system_profiler SPDisplaysDataType -json`, sin sudo. Distinto de la
// telemetría en vivo (RecolectarMetricasVivo): aquí solo se reporta el hardware, no el % de uso,
// que sí requiere `powermetrics` con privilegios root.
func recolectarGPU() ([]Video, error) {
	salida, err := ejecutarComando("system_profiler", "SPDisplaysDataType", "-json")
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPDisplaysDataType: %w", err)
	}

	var datos spDisplaysData
	if err := json.Unmarshal([]byte(salida), &datos); err != nil {
		return nil, fmt.Errorf("parsear JSON de GPU: %w", err)
	}

	var resultado []Video
	for _, item := range datos.SPDisplaysDataType {
		nombre := item.Model
		if nombre == "" {
			nombre = item.Name
		}
		if nombre == "" {
			continue
		}
		resultado = append(resultado, Video{
			Name:    nombre,
			Memory:  parseVRAMMB(item.VRAM),
			Chipset: strings.TrimPrefix(item.Vendor, "sppci_vendor_"),
		})
	}

	return resultado, nil
}

// spApplicationData modela la estructura JSON de system_profiler SPApplicationsDataType.
// `obtained_from` y `path` son las claves reales que permiten clasificar el software de forma
// confiable (a diferencia de adivinar por nombre): "apple" (firmado por Apple), "mac_app_store",
// "identified_developer" (terceros firmados), "unknown" (sin firma / scripts locales).
type spApplicationData struct {
	SPApplicationsDataType []struct {
		Name         string   `json:"_name"`
		Version      string   `json:"version"`
		Path         string   `json:"path"`
		ObtainedFrom string   `json:"obtained_from"`
		SignedBy     []string `json:"signed_by"`
	} `json:"SPApplicationsDataType"`
}

// developerNamePattern extrae "NOMBRE" de "Developer ID Application: NOMBRE (TEAMID)".
var developerNamePattern = regexp.MustCompile(`^Developer ID Application:\s*(.+?)\s*\([A-Z0-9]+\)\s*$`)

// clasificarSoftware deriva publisher y category a partir de obtained_from/path/signed_by.
// Ver docstring de spApplicationData sobre por qué estos campos (y no el nombre) son la fuente.
func clasificarSoftware(obtainedFrom, path string, signedBy []string) (publisher, category string) {
	primerFirmante := ""
	if len(signedBy) > 0 {
		primerFirmante = signedBy[0]
	}

	switch obtainedFrom {
	case "apple":
		publisher = "Apple Inc."
		// Apps de uso directo (/System/Applications, /Applications) vs utilidades/helpers en
		// segundo plano (/System/Applications/Utilities, /System/Library, /Library/Apple).
		if strings.HasPrefix(path, "/System/Applications/Utilities") ||
			strings.HasPrefix(path, "/System/Library") ||
			strings.HasPrefix(path, "/Library/Apple") {
			category = "UTILIDAD_SISTEMA"
		} else {
			category = "APLICACION"
		}
	case "mac_app_store":
		publisher = "Apple Inc. (App Store)"
		category = "APP_STORE"
	case "identified_developer":
		if m := developerNamePattern.FindStringSubmatch(primerFirmante); m != nil {
			publisher = m[1]
		} else {
			publisher = primerFirmante
		}
		category = "TERCERO_FIRMADO"
	default:
		publisher = ""
		category = "TERCERO_SIN_IDENTIFICAR"
	}
	return publisher, category
}

func recolectarSoftware() ([]Software, error) {
	salida, err := ejecutarComando("system_profiler", "SPApplicationsDataType", "-json")
	if err != nil {
		return nil, fmt.Errorf("system_profiler SPApplicationsDataType: %w", err)
	}

	var datos spApplicationData
	if err := json.Unmarshal([]byte(salida), &datos); err != nil {
		return nil, fmt.Errorf("parsear JSON de aplicaciones: %w", err)
	}

	var resultado []Software
	for _, app := range datos.SPApplicationsDataType {
		if app.Name == "" {
			continue
		}
		publisher, category := clasificarSoftware(app.ObtainedFrom, app.Path, app.SignedBy)
		resultado = append(resultado, Software{
			Name:      app.Name,
			Version:   app.Version,
			Publisher: publisher,
			Category:  category,
		})
	}

	return resultado, nil
}

// ejecutarComando ejecuta un comando de sistema y retorna su salida estándar como string.
func ejecutarComando(nombre string, args ...string) (string, error) {
	cmd := exec.Command(nombre, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("comando %q falló: %w — stderr: %s", nombre, err, stderr.String())
	}

	return stdout.String(), nil
}
