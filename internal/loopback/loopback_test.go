package loopback

import "testing"

func TestIsHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},
		{"[::1]", true},
		{"::ffff:127.0.0.1", true},
		{"0.0.0.0", false},
		{"192.168.1.10", false},
		{"aladdin.example.test", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsHost(c.host); got != c.want {
			t.Errorf("IsHost(%q) = %v，期望 %v", c.host, got, c.want)
		}
	}
}

func TestIsAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9090", true},
		{"localhost:9090", true},
		{"[::1]:9090", true},
		{"127.0.0.2:9090", true},
		{"aladdin.example.test:443", false},
		{"192.168.1.10:9090", false},
		{"0.0.0.0:9090", false},
		{"127.0.0.1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsAddress(c.addr); got != c.want {
			t.Errorf("IsAddress(%q) = %v，期望 %v", c.addr, got, c.want)
		}
	}
}
