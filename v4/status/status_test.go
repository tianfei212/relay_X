package status

import (
	"encoding/json"
	"testing"
)

// TestStatusParseAndJSON 验证 status 系列枚举的解析与 JSON 编解码。
func TestStatusParseAndJSON(t *testing.T) {
	check := func(v any, out any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}

	for _, v := range []string{"Starting", "Running", "Stopping", "Stopped"} {
		if _, err := ParseGatewayStatus(v); err != nil {
			t.Fatalf("gateway parse %q: %v", v, err)
		}
	}
	for _, v := range []string{"Idle", "Occupied", "Reserved", "Blocked"} {
		if _, err := ParsePortStatus(v); err != nil {
			t.Fatalf("port parse %q: %v", v, err)
		}
	}
	for _, v := range []string{"Init", "Handshake", "Established", "Disconnected", "Error"} {
		if _, err := ParseConnStatus(v); err != nil {
			t.Fatalf("conn parse %q: %v", v, err)
		}
	}
	for _, v := range []string{"Pending", "Approved", "Rejected", "Expired"} {
		if _, err := ParseAuthStatus(v); err != nil {
			t.Fatalf("auth parse %q: %v", v, err)
		}
	}
	for _, v := range []string{"Unknown", "Sending", "Receiving", "Duplex"} {
		if _, err := ParseFlowDirection(v); err != nil {
			t.Fatalf("flow parse %q: %v", v, err)
		}
	}

	gs, err := ParseGatewayStatus("Running")
	if err != nil {
		t.Fatal(err)
	}
	var gs2 GatewayStatus
	check(gs, &gs2)
	if gs2 != GatewayRunning {
		t.Fatalf("gs2=%s", gs2)
	}

	ps, err := ParsePortStatus("Occupied")
	if err != nil {
		t.Fatal(err)
	}
	var ps2 PortStatus
	check(ps, &ps2)
	if ps2 != PortOccupied {
		t.Fatalf("ps2=%s", ps2)
	}

	cs, err := ParseConnStatus("Established")
	if err != nil {
		t.Fatal(err)
	}
	var cs2 ConnStatus
	check(cs, &cs2)
	if cs2 != ConnEstablished {
		t.Fatalf("cs2=%s", cs2)
	}

	as, err := ParseAuthStatus("Approved")
	if err != nil {
		t.Fatal(err)
	}
	var as2 AuthStatus
	check(as, &as2)
	if as2 != AuthApproved {
		t.Fatalf("as2=%s", as2)
	}

	fd, err := ParseFlowDirection("Duplex")
	if err != nil {
		t.Fatal(err)
	}
	var fd2 FlowDirection
	check(fd, &fd2)
	if fd2 != FlowDuplex {
		t.Fatalf("fd2=%s", fd2)
	}

	if _, err := ParseGatewayStatus("X"); err == nil {
		t.Fatalf("expected error")
	}
	if _, err := ParsePortStatus("X"); err == nil {
		t.Fatalf("expected error")
	}
	if _, err := ParseConnStatus("X"); err == nil {
		t.Fatalf("expected error")
	}
	if _, err := ParseAuthStatus("X"); err == nil {
		t.Fatalf("expected error")
	}
	if _, err := ParseFlowDirection("X"); err == nil {
		t.Fatalf("expected error")
	}

	var bad GatewayStatus
	if err := json.Unmarshal([]byte(`"X"`), &bad); err == nil {
		t.Fatalf("expected unmarshal error")
	}

	var bad2 PortStatus
	if err := json.Unmarshal([]byte(`123`), &bad2); err == nil {
		t.Fatalf("expected unmarshal error")
	}
	var bad2b GatewayStatus
	if err := json.Unmarshal([]byte(`123`), &bad2b); err == nil {
		t.Fatalf("expected unmarshal error")
	}

	var bad3 ConnStatus
	if err := json.Unmarshal([]byte(`"X"`), &bad3); err == nil {
		t.Fatalf("expected unmarshal error")
	}
	var bad3b ConnStatus
	if err := json.Unmarshal([]byte(`123`), &bad3b); err == nil {
		t.Fatalf("expected unmarshal error")
	}
	var bad4 AuthStatus
	if err := json.Unmarshal([]byte(`"X"`), &bad4); err == nil {
		t.Fatalf("expected unmarshal error")
	}
	var bad4b AuthStatus
	if err := json.Unmarshal([]byte(`123`), &bad4b); err == nil {
		t.Fatalf("expected unmarshal error")
	}
	var bad5 FlowDirection
	if err := json.Unmarshal([]byte(`"X"`), &bad5); err == nil {
		t.Fatalf("expected unmarshal error")
	}
	var bad5b FlowDirection
	if err := json.Unmarshal([]byte(`123`), &bad5b); err == nil {
		t.Fatalf("expected unmarshal error")
	}

	_ = GatewayRunning.String()
	_ = PortIdle.String()
	_ = ConnInit.String()
	_ = AuthPending.String()
	_ = FlowUnknown.String()
}
