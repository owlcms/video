package jobutil

import "testing"

func TestParseSSUDPPortOwners(t *testing.T) {
	output := []byte("UNCONN 0 0 0.0.0.0:9005 0.0.0.0:* users:((\"ffplay\",pid=1234,fd=4))\n")
	owners := parseSSUDPPortOwners(output, 9005)
	if len(owners) != 1 {
		t.Fatalf("expected 1 owner, got %d", len(owners))
	}
	if owners[0].PID != 1234 || owners[0].Command != "ffplay" {
		t.Fatalf("unexpected owner: %+v", owners[0])
	}
}

func TestParseLsofUDPPortOwners(t *testing.T) {
	output := []byte("COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\nffplay   5678 user    4u  IPv4 123456      0t0  UDP *:9005\n")
	owners := parseLsofUDPPortOwners(output, 9005)
	if len(owners) != 1 {
		t.Fatalf("expected 1 owner, got %d", len(owners))
	}
	if owners[0].PID != 5678 || owners[0].Command != "ffplay" {
		t.Fatalf("unexpected owner: %+v", owners[0])
	}
}

func TestParseWindowsNetstatUDPPortOwners(t *testing.T) {
	output := []byte("  UDP    0.0.0.0:9005           *:*                                    3456\r\n")
	owners := parseWindowsNetstatUDPPortOwners(output, 9005)
	if len(owners) != 1 {
		t.Fatalf("expected 1 owner, got %d", len(owners))
	}
	if owners[0].PID != 3456 {
		t.Fatalf("unexpected owner: %+v", owners[0])
	}
}

func TestParseWindowsTasklistName(t *testing.T) {
	output := []byte("\"ffplay.exe\",\"3456\",\"Console\",\"1\",\"12,000 K\"\r\n")
	if got := parseWindowsTasklistName(output); got != "ffplay.exe" {
		t.Fatalf("parseWindowsTasklistName() = %q, want ffplay.exe", got)
	}
}