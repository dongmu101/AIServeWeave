package main

import (
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
)

func TestAdmissionOptionsApplyKeepsExplicitFlags(t *testing.T) {
	opts := &admissionOptions{sampleInterval: 5 * time.Second, cliMax: 2, queueMaxWait: 5 * time.Second, hysteresisPercent: 5, queueMaxWaiters: 16}
	cfg := agentconfig.AdmissionConfig{CLIMax: 8, GeneralMax: 4, CPUHighPercent: 90, QueueMaxWait: "0s"}
	if err := opts.apply(cfg, map[string]bool{"admission-cli-max": true}); err != nil {
		t.Fatal(err)
	}
	if opts.cliMax != 2 {
		t.Errorf("cliMax = %d, want the explicit flag 2", opts.cliMax)
	}
	if opts.generalMax != 4 || opts.cpuHighPercent != 90 {
		t.Errorf("file values not applied: %+v", opts)
	}
	if opts.queueMaxWait != 0 {
		t.Errorf("queue_max_wait \"0s\" must set zero, got %v", opts.queueMaxWait)
	}
	if opts.sampleInterval != 5*time.Second || opts.queueMaxWaiters != 16 {
		t.Errorf("unset file values must keep flag defaults: %+v", opts)
	}
}

func TestAdmissionOptionsRejectInvalidValues(t *testing.T) {
	if err := (&admissionOptions{}).apply(agentconfig.AdmissionConfig{CPUHighPercent: 101}, nil); err == nil {
		t.Error("apply accepted cpu_high_percent 101")
	}
	if err := (&admissionOptions{queueMaxWait: -time.Second}).validate(); err == nil {
		t.Error("validate accepted a negative wait")
	}
	if err := (&admissionOptions{cliMax: -1}).validate(); err == nil {
		t.Error("validate accepted a negative cli max")
	}
	if err := (&admissionOptions{cpuHighPercent: 80, sampleInterval: time.Second}).validate(); err != nil {
		t.Errorf("validate rejected valid options: %v", err)
	}
}
