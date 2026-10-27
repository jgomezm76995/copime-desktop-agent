// Package inventory define la interfaz Collector que toda implementación
// de recolección de inventario debe satisfacer, y los tipos de datos compartidos.
package inventory

// Hardware contiene información del equipo físico.
type Hardware struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

// Bios contiene información del firmware y plataforma.
type Bios struct {
	SManufacturer string `json:"smanufacturer"`
	SModel        string `json:"smodel"`
	SSN           string `json:"ssn"`
	MManufacturer string `json:"mmanufacturer"`
	MModel        string `json:"mmodel"`
	BManufacturer string `json:"bmanufacturer"`
	BVersion      string `json:"bversion"`
	BDate         string `json:"bdate"`
}

// OperatingSystem contiene información del sistema operativo.
type OperatingSystem struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Arch     string `json:"arch"`
}

// CPU representa un procesador del equipo.
type CPU struct {
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Core         int    `json:"core"`
	Thread       int    `json:"thread"`
	Speed        int    `json:"speed"` // MHz
}

// Memory representa un módulo de RAM.
type Memory struct {
	Capacity     int    `json:"capacity"` // MB
	Type         string `json:"type"`
	Speed        string `json:"speed"`
	Manufacturer string `json:"manufacturer"`
}

// Storage representa un dispositivo de almacenamiento.
type Storage struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Name         string `json:"name"`
	DiskSize     int64  `json:"disksize"` // MB
	Type         string `json:"type"`
}

// Network representa una interfaz de red.
type Network struct {
	Description string `json:"description"`
	IPAddress   string `json:"ipaddress"`
	MACAddr     string `json:"macaddr"`
	Status      string `json:"status"`
	Type        string `json:"type"`
}

// Software representa una aplicación instalada.
type Software struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	// Category clasifica el origen/naturaleza del software para la UI (pestañas en Activos de
	// soporte): APLICACION, UTILIDAD_SISTEMA, APP_STORE, TERCERO_FIRMADO, TERCERO_SIN_IDENTIFICAR.
	Category string `json:"category"`
}

// Partition representa una partición/volumen de almacenamiento real y visible para el usuario en
// ESTE equipo (p.ej. "Macintosh HD", un disco secundario interno, una unidad externa montada).
// Deliberadamente una LISTA y no un solo total: un equipo puede tener más de un disco físico o
// partición real, y un solo número agregado no los distingue (TT11524). Cada implementación de
// plataforma (darwin, y a futuro linux/windows) decide qué cuenta como "real" vs. pseudo-volumen
// interno del propio sistema operativo — ver el comentario de `recolectarParticiones` en
// darwin.go para el criterio verificado en macOS.
type Partition struct {
	Name       string `json:"name"`        // nombre visible del volumen (p.ej. "Macintosh HD")
	MountPoint string `json:"mount_point"` // punto de montaje real (p.ej. "/", "/Volumes/Backup")
	UsedMB     int64  `json:"used_mb"`
	TotalMB    int64  `json:"total_mb"`
}

// Video representa una tarjeta gráfica (GPU) del equipo. Memory queda en 0 cuando la GPU usa
// memoria unificada/compartida con la CPU (p.ej. Apple Silicon) en vez de VRAM dedicada: no hay
// un tamaño propio que reportar en ese caso.
type Video struct {
	Name    string `json:"name"`
	Memory  int    `json:"memory"` // MB
	Chipset string `json:"chipset"`
}

// LiveMetrics es una foto del momento de uso de recursos (no histórico): CPU, RAM y disco
// actuales. GPU se omite: requiere `powermetrics` con sudo en macOS, fuera del alcance de un
// agente sin privilegios. Se envía siempre en el inventario periódico y, bajo demanda, en el
// ciclo liviano de `contact` cuando el servidor marcó una solicitud pendiente.
type LiveMetrics struct {
	CPUPercent float64     `json:"cpu_percent"`
	RAMUsedMB  int64       `json:"ram_used_mb"`
	RAMTotalMB int64       `json:"ram_total_mb"`
	Partitions []Partition `json:"disk_partitions"`
}

// Inventario agrupa todos los datos del equipo recolectados por el agente.
type Inventario struct {
	Hardware        Hardware        `json:"hardware"`
	Bios            Bios            `json:"bios"`
	OperatingSystem OperatingSystem `json:"operatingsystem"`
	CPUs            []CPU           `json:"cpus"`
	Memories        []Memory        `json:"memories"`
	Storages        []Storage       `json:"storages"`
	Networks        []Network       `json:"networks"`
	Softwares       []Software      `json:"softwares,omitempty"`
	Videos          []Video         `json:"videos,omitempty"`
	LiveMetrics     *LiveMetrics    `json:"live_metrics,omitempty"`
}

// Collector define la interfaz de recolección de inventario.
// Cada plataforma (darwin, linux, windows) implementa esta interfaz.
type Collector interface {
	// RecolectarInventario recopila los datos del sistema y los retorna como Inventario.
	// Si la recolección de software falla o está omitida, el campo Softwares puede estar vacío.
	RecolectarInventario() (*Inventario, error)

	// ObtenerDeviceID retorna un identificador de hardware estable entre reinicios.
	ObtenerDeviceID() (string, error)

	// RecolectarMetricasVivo recolecta una foto liviana de CPU/RAM/disco (sin inventario completo),
	// usada tanto dentro de RecolectarInventario como en el ciclo de telemetría bajo demanda.
	RecolectarMetricasVivo() (*LiveMetrics, error)
}
