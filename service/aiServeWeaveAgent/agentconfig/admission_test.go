package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdmissionConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	want := AdmissionConfig{
		SampleInterval: "10s", CPUHighPercent: 85, MemoryLowPercent: 10, HysteresisPercent: 4,
		CLIMax: 4, GeneralMax: 16, QueueMaxWait: "0s", QueueMaxWaiters: 8,
	}
	if err := Save(path, &Config{Admission: want}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Admission != want {
		t.Fatalf("admission settings lost:\nwant %+v\n got %+v", want, got.Admission)
	}
}

func TestSaveOmitsAnEmptyAdmissionSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := Save(path, &Config{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "admission") {
		t.Fatalf("an unset admission section was written:\n%s", data)
	}
}

func TestAdmissionConfigValidate(t *testing.T) {
	bad := []AdmissionConfig{
		{SampleInterval: "soon"},
		{QueueMaxWait: "-1s"},
		{CPUHighPercent: 120},
		{MemoryLowPercent: -1},
		{CLIMax: -1},
		{QueueMaxWaiters: -2},
	}
	for _, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted an invalid value", cfg)
		}
	}
	if err := (AdmissionConfig{CPUHighPercent: 90, QueueMaxWait: "5s"}).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestLoadRejectsInvalidAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("admission:\n  cpu_high_percent: 150\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "cpu_high_percent") {
		t.Fatalf("Load = %v, want a cpu_high_percent error", err)
	}
}

func TestAdmissionConfigRejectsUnreachableExits(t *testing.T) {
	bad := []AdmissionConfig{
		{MemoryLowPercent: 96},                      // 96 + default 5 ≥ 100
		{CPUHighPercent: 5},                         // ≤ default hysteresis 5
		{CPUHighPercent: 20, HysteresisPercent: 30}, // ≤ explicit hysteresis
		{SampleInterval: "100ms"},
	}
	for _, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(%+v) accepted an unreachable exit or a too-short interval", cfg)
		}
	}
}
