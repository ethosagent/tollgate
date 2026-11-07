package tollgate_test

import (
	"encoding/json"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

type sessionsStatus struct {
	Live          int   `json:"live"`
	Evicted       int64 `json:"evicted"`
	Expired       int64 `json:"expired"`
	Agree         int64 `json:"predict_agree"`
	Disagree      int64 `json:"predict_disagree"`
	ClosedDropped int64 `json:"closed_dropped"`
}

func sessionsStatusz(t *testing.T, admin string) *sessionsStatus {
	t.Helper()
	var doc struct {
		Sessions *sessionsStatus `json:"sessions"`
	}
	if err := json.Unmarshal(readAll(t, get(t, admin+"/statusz")), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Sessions
}

func TestSession_StatuszCountsPredictionAccuracyForHeaderCalls(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	base, p, _ := newSessionProxy(t, tollgate.Options{})
	admin := adminServer(t, p)
	for step := 1; step <= 6; step++ {
		_ = readAll(t, post(t, base, msgsPath, agentStep(t, step), via(up, "x-session-id", "run-1")))
	}
	s := sessionsStatusz(t, admin)
	if s == nil || s.Agree != 6 || s.Disagree != 0 || s.Live != 1 {
		t.Fatalf("statusz sessions = %+v, want 6 agree, 0 disagree, 1 live", s)
	}
	_ = readAll(t, post(t, base, msgsPath, agentStep(t, 6), via(up, "x-session-id", "run-2")))
	if s := sessionsStatusz(t, admin); s.Disagree != 1 {
		t.Fatalf("a different header id on the same conversation must count as a disagreement: %+v", s)
	}
}

func TestSession_MaxSessionsCapsLiveSessionsAndCountsEvictions(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	base, p, _ := newSessionProxy(t, tollgate.Options{Limits: tollgate.Limits{MaxSessions: 2}})
	admin := adminServer(t, p)
	for i := range 6 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up, "x-session-id", string(rune('a'+i)))))
	}
	s := sessionsStatusz(t, admin)
	if s.Live > 2 || s.Live+int(s.Evicted) != 6 {
		t.Fatalf("sessions = %+v, want at most 2 live and the rest counted as evicted", s)
	}
}

func TestSession_StatuszOmitsSessionsForAHostStrategy(t *testing.T) {
	_, p, _ := newSessionProxy(t, tollgate.Options{Sessions: &fakeStrategy{}})
	if s := sessionsStatusz(t, adminServer(t, p)); s != nil {
		t.Fatalf("sessions = %+v, want none: the default store's counters do not describe a host strategy", s)
	}
}
