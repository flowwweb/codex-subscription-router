package main

import "testing"

func TestUseDaemonBridgePreservesMacOSDirectMode(t *testing.T) {
	tests := []struct {
		platform string
		required string
		want     bool
	}{
		{platform: "windows", want: true},
		{platform: "darwin", want: false},
		{platform: "linux", want: false},
		{platform: "darwin", required: "1", want: true},
	}
	for _, test := range tests {
		if got := useDaemonBridge(test.platform, test.required); got != test.want {
			t.Errorf("useDaemonBridge(%q, %q) = %t, want %t", test.platform, test.required, got, test.want)
		}
	}
}
