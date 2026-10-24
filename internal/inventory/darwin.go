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

	// Esperar a que termine la recolección de software.
	wg.Wait()
	if errSoftware != nil {
		// Software es opcional — registrar el error pero continuar.
		log.Printf("[ADVERTENCIA] No se pudo recolectar software instalado: %v", errSoftware)
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
	}, nil
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

	hw := Hardware{
		Name: item.MachineName,
		UUID: item.PlatformUUID,
	}

	// Obtener hostname del sistema para el campo name si está vacío.
	if hw.Name == "" {
		if nombre, err := ejecutarComando("hostname"); err == nil {
			hw.Name = strings.TrimSpace(nombre)
		}
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

// spStorageData modela el JSON de system_profiler SPStorageDataType.
type spStorageData struct {
	SPStorageDataType []struct {
		Name              string `json:"_name"`
		MediaType         string `json:"spstorage_media_type"`
		SizeBytes         int64  `json:"spstorage_total_amount_absolute"`
		PhysicalDrive     *struct {
			DeviceName  string `json:"device_name"`
			MediaType   string `json:"medium_type"`
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

	var resultado []Storage
	for _, item := range datos.SPStorageDataType {
		modelo := ""
		tipoMedio := "SSD"
		if item.PhysicalDrive != nil {
			modelo = item.PhysicalDrive.DeviceName
			if item.PhysicalDrive.MediaType != "" {
				tipoMedio = item.PhysicalDrive.MediaType
			}
		}

		resultado = append(resultado, Storage{
			Manufacturer: "Apple",
			Model:        modelo,
			Name:         item.Name,
			DiskSize:     item.SizeBytes / 1024 / 1024, // convertir a MB
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

	return parsearIfconfig(salida), nil
}

// parsearIfconfig extrae interfaces de red relevantes del output de ifconfig.
func parsearIfconfig(salida string) []Network {
	var resultado []Network
	var interfazActual *Network

	for _, linea := range strings.Split(salida, "\n") {
		// Nueva interfaz: empieza sin espacio y contiene ":".
		if len(linea) > 0 && linea[0] != ' ' && linea[0] != '\t' {
			if interfazActual != nil && interfazActual.IPAddress != "" {
				resultado = append(resultado, *interfazActual)
			}
			partes := strings.SplitN(linea, ":", 2)
			nombre := strings.TrimSpace(partes[0])
			// Filtrar solo interfaces físicas relevantes (en*, bridge*, utun* se omiten).
			if strings.HasPrefix(nombre, "en") || strings.HasPrefix(nombre, "eth") {
				interfazActual = &Network{
					Description: nombre,
					Status:      "down",
					Type:        determinarTipoRed(nombre),
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
			if len(partes) >= 2 {
				interfazActual.MACAddr = partes[1]
			}
		} else if strings.Contains(linea, "status: active") {
			interfazActual.Status = "up"
		}
	}

	// No olvidar la última interfaz procesada.
	if interfazActual != nil && interfazActual.IPAddress != "" {
		resultado = append(resultado, *interfazActual)
	}

	return resultado
}

func determinarTipoRed(nombre string) string {
	if nombre == "en0" {
		return "WiFi"
	}
	return "Ethernet"
}

// spApplicationData modela la estructura JSON de system_profiler SPApplicationsDataType.
type spApplicationData struct {
	SPApplicationsDataType []struct {
		Name    string `json:"_name"`
		Version string `json:"version"`
		// El campo "info" a veces contiene el publisher, pero no es consistente.
	} `json:"SPApplicationsDataType"`
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
		resultado = append(resultado, Software{
			Name:    app.Name,
			Version: app.Version,
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
