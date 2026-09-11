package registryserver

import (
	"testing"

	"AIServeWeave/common/metrics/metricstest"
)

func TestDescriptionsCoverEveryMetric(t *testing.T) {
	descs := Descriptions()
	for _, name := range []string{
		MetricRegisterTotal, MetricCertRenewalTotal, MetricGatewayReplicasConnected,
		MetricTokenOpsTotal, MetricNodeStateChangesTotal, MetricListNodeStatesTotal,
	} {
		desc, ok := descs[name]
		if !ok {
			t.Errorf("Descriptions() missing %s", name)
			continue
		}
		if desc.Help == "" {
			t.Errorf("%s has no Help text", name)
		}
	}
}

func TestRecorderTracksRegisterOutcomes(t *testing.T) {
	mx := metricstest.New()
	rec := newRecorder(mx)

	rec.Register(ResultSuccess)
	rec.Register(ResultConflict)

	if got := mx.Sum(MetricRegisterTotal, map[string]string{"result": ResultSuccess}); got != 1 {
		t.Errorf("success count = %v, want 1", got)
	}
	if got := mx.Sum(MetricRegisterTotal, map[string]string{"result": ResultConflict}); got != 1 {
		t.Errorf("conflict count = %v, want 1", got)
	}
}

func TestRecorderTracksGatewayReplicaGauge(t *testing.T) {
	mx := metricstest.New()
	rec := newRecorder(mx)

	rec.GatewayJoined()
	rec.GatewayJoined()
	rec.GatewayLeft()

	if got := mx.Sum(MetricGatewayReplicasConnected, nil); got != 1 {
		t.Errorf("connected replicas = %v, want 1", got)
	}
}
