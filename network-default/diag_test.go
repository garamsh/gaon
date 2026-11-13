package networkdefault_test

import (
	"os"
	"os/exec"
	"testing"
)

func TestDiag(t *testing.T) {
	out, err := exec.Command("sh", "-c", "grep -i cap /proc/self/status; cat /proc/self/attr/current; cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns /proc/sys/kernel/unprivileged_userns_clone; cat /proc/sys/net/ipv4/ip_forward; grep ' /proc' /proc/self/mountinfo; ls -ln /proc/sys/net/ipv4/ip_forward; id; echo in=$GAON_NETWORK_DEFAULT_TEST_NAMESPACES").CombinedOutput()
	t.Errorf("%s\nerr=%v", out, err)
	werr := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o600)
	t.Fatalf("write err=%v", werr)
}
