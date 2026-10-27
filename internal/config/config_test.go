package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGuardarLeerRoundTrip verifica que Guardar + Leer preserven los datos y que el
// archivo/directorio queden con los permisos PermsArchivo/PermsDirectorio de la
// plataforma actual — en macOS, 0644/0755 (legible por cualquier usuario local, ver
// config_darwin.go) porque el LaunchAgent corre como el usuario con sesión gráfica,
// no como root.
func TestGuardarLeerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "sub", "config.json")

	original := &Config{
		ServerURL:         "https://conasinf.copime.net",
		DeviceID:          "cad_test",
		Key:               "llave-secreta",
		SyncIntervalHours: 24,
		AgentVersion:      "0.1.0-test",
	}

	if err := Guardar(ruta, original); err != nil {
		t.Fatalf("Guardar: %v", err)
	}

	leido, err := Leer(ruta)
	if err != nil {
		t.Fatalf("Leer: %v", err)
	}
	if *leido != *original {
		t.Fatalf("config leída no coincide: got %+v, want %+v", *leido, *original)
	}

	infoArchivo, err := os.Stat(ruta)
	if err != nil {
		t.Fatalf("stat archivo: %v", err)
	}
	if infoArchivo.Mode().Perm() != PermsArchivo {
		t.Errorf("permisos de archivo = %o, want %o", infoArchivo.Mode().Perm(), PermsArchivo)
	}

	infoDir, err := os.Stat(filepath.Dir(ruta))
	if err != nil {
		t.Fatalf("stat directorio: %v", err)
	}
	if infoDir.Mode().Perm() != PermsDirectorio {
		t.Errorf("permisos de directorio = %o, want %o", infoDir.Mode().Perm(), PermsDirectorio)
	}
}

// TestEstaCompleta cubre el único método de negocio de Config.
func TestEstaCompleta(t *testing.T) {
	casos := []struct {
		nombre string
		cfg    Config
		want   bool
	}{
		{"completa", Config{ServerURL: "https://x", DeviceID: "d", Key: "k"}, true},
		{"sin key", Config{ServerURL: "https://x", DeviceID: "d"}, false},
		{"vacía", Config{}, false},
	}

	for _, c := range casos {
		if got := c.cfg.EstaCompleta(); got != c.want {
			t.Errorf("%s: EstaCompleta() = %v, want %v", c.nombre, got, c.want)
		}
	}
}
